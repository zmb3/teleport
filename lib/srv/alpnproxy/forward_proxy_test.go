/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package alpnproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/tlsca"
)

func TestForwardProxy(t *testing.T) {
	caKey, caCert, err := tlsca.GenerateSelfSignedCA(pkix.Name{
		CommonName: "localhost",
	}, []string{"localhost"}, defaults.CATTL)
	require.NoError(t, err)

	ca, err := tls.X509KeyPair(caCert, caKey)
	require.NoError(t, err)

	// Use a different status code for each destination.
	receiverCode := http.StatusAccepted
	originalHostCode := http.StatusCreated

	// Setup a receiver that wants a specific domain. The receiver uses
	// CertGenListener to generate certificate for this domain on the fly.
	receiverListener := mustCreateCertGenListener(t, ca)
	receiverHandler := NewForwardToHostHandler(ForwardToHostHandlerConfig{
		MatchFunc: func(req *http.Request) bool {
			return req.Host == "receiver.wanted.com:443"
		},
		Host: receiverListener.Addr().String(),
	})
	go http.Serve(receiverListener, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(receiverCode)
	}))

	// Setup a HTTPS server to simulate original host.
	originalHostListener := mustCreateCertGenListener(t, ca)
	originalHostAddress := strings.ReplaceAll(originalHostListener.Addr().String(), "127.0.0.1", "localhost")
	go http.Serve(originalHostListener, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(originalHostCode)
	}))

	// client -> forward proxy -> receiver
	t.Run("to receiver", func(t *testing.T) {
		forwardProxy := createForwardProxy(t, receiverHandler, NewForwardToOriginalHostHandler())
		client := httpsClientWithProxyURL(forwardProxy.GetAddr(), caCert)

		mustCallHTTPSServerAndReceiveCode(t, "receiver.wanted.com:443", *client, receiverCode)
	})

	// client -> forward proxy -> original host
	t.Run("to original host", func(t *testing.T) {
		forwardProxy := createForwardProxy(t, receiverHandler, NewForwardToOriginalHostHandler())
		client := httpsClientWithProxyURL(forwardProxy.GetAddr(), caCert)

		mustCallHTTPSServerAndReceiveCode(t, originalHostAddress, *client, originalHostCode)
	})

	// client -> forward proxy -> system proxy -> original host
	t.Run("to system proxy", func(t *testing.T) {
		systemProxyHTTPServer := createSystemProxy(t, mustCreateLocalListener(t))

		forwardToSystemProxyHandler := NewForwardToSystemProxyHandler(ForwardToSystemProxyHandlerConfig{
			SystemProxyFunc: func(*url.URL) (*url.URL, error) {
				return url.Parse("http://" + systemProxyHTTPServer.GetAddr())
			},
		})

		forwardProxy := createForwardProxy(t, forwardToSystemProxyHandler)
		client := httpsClientWithProxyURL(forwardProxy.GetAddr(), caCert)

		mustCallHTTPSServerAndReceiveCode(t, originalHostAddress, *client, originalHostCode)
	})

	// client -> forward proxy -> system proxy (HTTPS) -> original host
	t.Run("to system proxy (HTTPS)", func(t *testing.T) {
		// This test is the same as previous one except the system proxy is a
		// HTTPS server.
		systemProxyHTTPSServer := createSystemProxy(t, mustCreateCertGenListener(t, ca))

		forwardToSystemProxyHandler := NewForwardToSystemProxyHandler(ForwardToSystemProxyHandlerConfig{
			InsecureSystemProxy: true,
			SystemProxyFunc: func(*url.URL) (*url.URL, error) {
				return url.Parse("https://" + systemProxyHTTPSServer.GetAddr())
			},
		})

		forwardProxy := createForwardProxy(t, forwardToSystemProxyHandler)
		client := httpsClientWithProxyURL(forwardProxy.GetAddr(), caCert)

		mustCallHTTPSServerAndReceiveCode(t, originalHostAddress, *client, originalHostCode)
	})
}

// createForwardProxy creates a ForwardProxy with provided handlers.
func createForwardProxy(t *testing.T, handlers ...ConnectRequestHandler) *ForwardProxy {
	return createForwardProxyWithConfig(t, ForwardProxyConfig{
		Listener: mustCreateLocalListener(t),
		Handlers: handlers,
	})
}

// createSystemProxy creates a ForwardProxy to simulate a system proxy.
func createSystemProxy(t *testing.T, listener net.Listener) *ForwardProxy {
	return createForwardProxyWithConfig(t, ForwardProxyConfig{
		Listener: listener,
		Handlers: []ConnectRequestHandler{
			NewForwardToOriginalHostHandler(),
		},
	})
}

// createForwardProxyWithConfig creates a ForwardProxy with provided config.
func createForwardProxyWithConfig(t *testing.T, config ForwardProxyConfig) *ForwardProxy {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	config.CloseContext = ctx

	forwardProxy, err := NewForwardProxy(config)
	require.NoError(t, err)

	t.Cleanup(func() {
		forwardProxy.Close()
	})

	go func() {
		assert.NoError(t, forwardProxy.Start())
	}()
	return forwardProxy
}
