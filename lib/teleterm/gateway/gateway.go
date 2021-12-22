/*
Copyright 2015 Gravitational, Inc.

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

package gateway

import (
	"context"
	"strconv"

	"net"

	"github.com/gravitational/teleport/lib/client/db/postgres"
	dbprofile "github.com/gravitational/teleport/lib/client/db/profile"
	alpn "github.com/gravitational/teleport/lib/srv/alpnproxy"
	alpncommon "github.com/gravitational/teleport/lib/srv/alpnproxy/common"
	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"
)

func New(cfg Config) (*Gateway, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	closeContext, closeCancel := context.WithCancel(context.Background())
	// make sure the listener is closed if gateway creation failed
	ok := false
	defer func() {
		if ok {
			return
		}

		closeCancel()
		if err := listener.Close(); err != nil {
			cfg.Log.WithError(err).Warn("Failed to close listener.")
		}
	}()

	// retreive automatically assigned port number
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	protocol, err := alpncommon.ToALPNProtocol(cfg.Protocol)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	address, err := utils.ParseAddr(cfg.WebProxyAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	localProxy, err := alpn.NewLocalProxy(alpn.LocalProxyConfig{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		RemoteProxyAddr:    cfg.WebProxyAddr,
		Protocol:           protocol,
		Listener:           listener,
		ParentContext:      closeContext,
		SNI:                address.Host(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cfg.LocalPort = port

	gateway := &Gateway{
		Config:       cfg,
		Kind:         uri.KindGateway,
		closeContext: closeContext,
		closeCancel:  closeCancel,
		localProxy:   localProxy,
	}

	if err := setNativeCommand(gateway); err != nil {
		return nil, trace.Wrap(err)
	}

	ok = true
	return gateway, nil
}

func (g *Gateway) Close() {
	g.closeCancel()
	g.localProxy.Close()
}

// Open opens a gateway to Teleport proxy
func (g *Gateway) Open() {
	go func() {
		g.Log.Info("Gateway is open.")
		if err := g.localProxy.Start(g.closeContext); err != nil {
			g.Log.WithError(err).Warn("Failed to open a connection.")
		}

		g.Log.Info("Gateway has closed.")
	}()
}

func setNativeCommand(g *Gateway) error {
	port, err := strconv.Atoi(g.LocalPort)
	if err != nil {
		return trace.Wrap(err)
	}

	args := postgres.GetConnString(dbprofile.ConnectProfile{
		Port:       port,
		Host:       g.LocalAddress,
		Insecure:   g.InsecureSkipVerify,
		CACertPath: g.CACertPath,
		CertPath:   g.DBCertPath,
		KeyPath:    g.KeyPath,
	})

	g.NativeClientCommand.Path = "psql"
	g.NativeClientCommand.Args = args + "&user=[DB_USER]&dbname=[DB_NAME]"

	return nil
}

// Gateway describes local proxy that creates a gateway to the remote Teleport resource.
type Gateway struct {
	// URIKind is the resource kind
	URIKind uri.Kind
	// GatewayParams is the gateway parameters
	Config
	// NativeClientCommand is the native client commands
	NativeClientCommand NativeClientCommand

	localProxy *alpn.LocalProxy
	// closeContext and closeCancel are used to signal to any waiting goroutines
	// that the local proxy is now closed and to release any resources.
	closeContext context.Context
	closeCancel  context.CancelFunc
}

// NativeClientCommand describes native command parameters of the native clients
type NativeClientCommand struct {
	// Path is the native client path
	Path string
	// Args is the native client args
	Args string
}
