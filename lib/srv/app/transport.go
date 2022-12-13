/*
Copyright 2020 Gravitational, Inc.

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

package app

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"path"

	"github.com/gravitational/oxy/forward"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/api/types/wrappers"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/lib"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv/app/common"
	"github.com/zmb3/teleport/lib/utils"
)

// transportConfig is configuration for a rewriting transport.
type transportConfig struct {
	app          types.Application
	publicPort   string
	cipherSuites []uint16
	jwt          string
	w            events.StreamWriter
	traits       wrappers.Traits
	log          logrus.FieldLogger
	user         string
}

// Check validates configuration.
func (c *transportConfig) Check() error {
	if c.w == nil {
		return trace.BadParameter("stream writer missing")
	}
	if c.app == nil {
		return trace.BadParameter("app missing")
	}
	if c.publicPort == "" {
		return trace.BadParameter("public port missing")
	}
	if c.jwt == "" {
		return trace.BadParameter("jwt missing")
	}
	if c.log == nil {
		c.log = logrus.WithField(trace.Component, "transport")
	}

	return nil
}

// transport is a rewriting http.RoundTripper that can audit and forward
// requests to an internal application.
type transport struct {
	closeContext context.Context

	c *transportConfig

	tr http.RoundTripper

	uri *url.URL

	ws *websocketTransport
}

// newTransport creates a new transport.
func newTransport(ctx context.Context, c *transportConfig) (*transport, error) {
	if err := c.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	// Parse the target address once then inject it into all requests.
	uri, err := url.Parse(c.app.GetURI())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Clone and configure the transport.
	tr, err := defaults.Transport()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tr.TLSClientConfig, err = configureTLS(c)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &transport{
		closeContext: ctx,
		c:            c,
		uri:          uri,
		tr:           tr,
		ws:           newWebsocketTransport(uri, tr.TLSClientConfig, c),
	}, nil
}

// RoundTrip will rewrite the request, forward the request to the target
// application, emit an event to the audit log, then rewrite the response.
func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Check if the request path needs re-writing. This occurs when the URI
	// contains a path like http://localhost:8080/app/acme, but the request comes
	// to https://publicAddr. In that case do a 302 to the correct path instead
	// of doing path re-writing on all requests. This is a workaround to make
	// sure Teleport does not break SPA.
	if location, ok := t.needsPathRedirect(r); ok {
		return &http.Response{
			Status:     http.StatusText(http.StatusFound),
			StatusCode: http.StatusFound,
			Proto:      r.Proto,
			ProtoMajor: r.ProtoMajor,
			ProtoMinor: r.ProtoMinor,
			Body:       http.NoBody,
			Header: http.Header{
				"Location": []string{location},
			},
			TLS: r.TLS,
		}, nil
	}

	// Perform any request rewriting needed before forwarding the request.
	if err := t.rewriteRequest(r); err != nil {
		return nil, trace.Wrap(err)
	}

	// Forward the request to the target application and emit an audit event.
	resp, err := t.tr.RoundTrip(r)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Emit the event to the audit log.
	if err := t.emitAuditEvent(r, resp); err != nil {
		return nil, trace.Wrap(err)
	}

	// Perform any response rewriting needed before returning the request.
	if err := t.rewriteResponse(resp); err != nil {
		return nil, trace.Wrap(err)
	}

	return resp, nil
}

// rewriteRequest applies any rewriting rules to the request before it's forwarded.
func (t *transport) rewriteRequest(r *http.Request) error {
	// Update the target address of the request so it's forwarded correctly.
	r.URL.Scheme = t.uri.Scheme
	r.URL.Host = t.uri.Host

	// Add headers from rewrite configuration.
	rewriteHeaders(r, t.c)

	return nil
}

// rewriteHeaders applies headers rewrites from the application configuration.
func rewriteHeaders(r *http.Request, c *transportConfig) {
	// Add in JWT headers.
	r.Header.Set(teleport.AppJWTHeader, c.jwt)
	r.Header.Set(teleport.AppCFHeader, c.jwt)

	if c.app.GetRewrite() == nil || len(c.app.GetRewrite().Headers) == 0 {
		return
	}
	for _, header := range c.app.GetRewrite().Headers {
		if common.IsReservedHeader(header.Name) {
			c.log.Debugf("Not rewriting Teleport header %q.", header.Name)
			continue
		}
		values, err := services.ApplyValueTraits(header.Value, c.traits)
		if err != nil {
			c.log.Debugf("Failed to apply traits to %q: %v.", header.Value, err)
			continue
		}
		r.Header.Del(header.Name)
		for _, value := range values {
			switch http.CanonicalHeaderKey(header.Name) {
			case teleport.HostHeader:
				r.Host = value
			default:
				r.Header.Add(header.Name, value)
			}
		}
	}
}

// needsPathRedirect checks if the request should be redirected to a different path.
// At the moment, the only time a redirect happens is if URI specified is not
// "/" and the public address being requested is "/".
func (t *transport) needsPathRedirect(r *http.Request) (string, bool) {
	// If the URI for the application has no path specified, nothing to be done.
	uriPath := path.Clean(t.uri.Path)
	if uriPath == "." {
		uriPath = "/"
	}
	if uriPath == "/" {
		return "", false
	}

	// For simplicity, only support redirecting to the URI path if the root path
	// is requested.
	reqPath := path.Clean(r.URL.Path)
	if reqPath == "." {
		reqPath = "/"
	}
	if reqPath != "/" {
		return "", false
	}

	u := url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(t.c.app.GetPublicAddr(), t.c.publicPort),
		Path:   uriPath,
	}
	return u.String(), true
}

// rewriteResponse applies any rewriting rules to the response before returning it.
func (t *transport) rewriteResponse(resp *http.Response) error {
	switch {
	case t.c.app.GetRewrite() != nil && len(t.c.app.GetRewrite().Redirect) > 0:
		err := t.rewriteRedirect(resp)
		if err != nil {
			return trace.Wrap(err)
		}
	default:
	}
	return nil
}

// rewriteRedirect applies redirect rules to the response.
func (t *transport) rewriteRedirect(resp *http.Response) error {
	if isRedirect(resp.StatusCode) {
		// Parse the "Location" header.
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return trace.Wrap(err)
		}

		// If the redirect location is one of the hosts specified in the list of
		// redirects, rewrite the header.
		if apiutils.SliceContainsStr(t.c.app.GetRewrite().Redirect, host(u.Host)) {
			u.Scheme = "https"
			u.Host = net.JoinHostPort(t.c.app.GetPublicAddr(), t.c.publicPort)
		}
		resp.Header.Set("Location", u.String())
	}
	return nil
}

// emitAuditEvent writes the request and response to audit stream.
func (t *transport) emitAuditEvent(req *http.Request, resp *http.Response) error {
	appSessionRequestEvent := &apievents.AppSessionRequest{
		Metadata: apievents.Metadata{
			Type: events.AppSessionRequestEvent,
			Code: events.AppSessionRequestCode,
		},
		Method:     req.Method,
		Path:       req.URL.Path,
		RawQuery:   req.URL.RawQuery,
		StatusCode: uint32(resp.StatusCode),
		AppMetadata: apievents.AppMetadata{
			AppURI:        t.c.app.GetURI(),
			AppPublicAddr: t.c.app.GetPublicAddr(),
			AppName:       t.c.app.GetName(),
		},
	}
	if err := t.c.w.EmitAuditEvent(t.closeContext, appSessionRequestEvent); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// configureTLS creates and configures a *tls.Config that will be used for
// mutual authentication.
func configureTLS(c *transportConfig) (*tls.Config, error) {
	tlsConfig := utils.TLSConfig(c.cipherSuites)

	// Don't verify the server's certificate if Teleport was started with
	// the --insecure flag, or 'insecure_skip_verify' was specifically requested in
	// the application config.
	tlsConfig.InsecureSkipVerify = (lib.IsInsecureDevMode() || c.app.GetInsecureSkipVerify())

	return tlsConfig, nil
}

// host returns the host from a host:port string.
func host(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// isRedirect returns true if the status code is a 3xx code.
func isRedirect(code int) bool {
	if code >= http.StatusMultipleChoices && code <= http.StatusPermanentRedirect {
		return true
	}
	return false
}

// websocketTransport combines parameters for websockets transport.
//
// Implements forward.ReqRewriter.
type websocketTransport struct {
	uri    *url.URL
	dialer forward.Dialer
	c      *transportConfig
}

// newWebsocketTransport returns transport that knows how to rewrite and
// dial websocket requests.
func newWebsocketTransport(uri *url.URL, tlsConfig *tls.Config, c *transportConfig) *websocketTransport {
	return &websocketTransport{
		uri: uri,
		dialer: func(network, address string) (net.Conn, error) {
			// Request is going to "wss://".
			if uri.Scheme == "https" {
				return tls.Dial(network, address, tlsConfig)
			}
			// Request is going to "ws://".
			return net.Dial(network, address)
		},
		c: c,
	}
}

// Rewrite rewrites the websocket request.
func (r *websocketTransport) Rewrite(req *http.Request) {
	// Update scheme and host to those of the target app's to make sure
	// it's forwarded correctly.
	req.URL.Scheme = "ws"
	if r.uri.Scheme == "https" {
		req.URL.Scheme = "wss"
	}
	req.URL.Host = r.uri.Host
	req.Host = r.uri.Host

	rewriteHeaders(req, r.c)
}
