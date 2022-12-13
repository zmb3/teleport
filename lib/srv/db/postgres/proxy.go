/*
Copyright 2020-2021 Gravitational, Inc.

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

package postgres

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/gravitational/trace"
	"github.com/jackc/pgproto3/v2"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/limiter"
	"github.com/zmb3/teleport/lib/srv/alpnproxy"
	"github.com/zmb3/teleport/lib/srv/db/common"
	"github.com/zmb3/teleport/lib/utils"
)

// Proxy proxies connections from Postgres clients to database services
// over reverse tunnel. It runs inside Teleport proxy service.
//
// Implements common.Proxy.
type Proxy struct {
	// TLSConfig is the proxy TLS configuration.
	TLSConfig *tls.Config
	// Middleware is the auth middleware.
	Middleware *auth.Middleware
	// Service is used to connect to a remote database service.
	Service common.Service
	// Log is used for logging.
	Log logrus.FieldLogger
	// Limiter limits the number of active connections per client IP.
	Limiter *limiter.Limiter
}

// HandleConnection accepts connection from a Postgres client, authenticates
// it and proxies it to an appropriate database service.
func (p *Proxy) HandleConnection(ctx context.Context, clientConn net.Conn) (err error) {
	startupMessage, tlsConn, backend, err := p.handleStartup(ctx, clientConn)
	if err != nil {
		return trace.Wrap(err)
	}
	defer func() {
		if err != nil {
			if err := backend.Send(toErrorResponse(err)); err != nil {
				p.Log.WithError(err).Warn("Failed to send error to backend.")
			}
		}
	}()

	clientIP, err := utils.ClientIPFromConn(clientConn)
	if err != nil {
		return trace.Wrap(err)
	}

	// Apply connection and rate limiting.
	releaseConn, err := p.Limiter.RegisterRequestAndConnection(clientIP)
	if err != nil {
		return trace.Wrap(err)
	}
	defer releaseConn()

	proxyCtx, err := p.Service.Authorize(ctx, tlsConn, common.ConnectParams{
		ClientIP: clientIP,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	serviceConn, err := p.Service.Connect(ctx, proxyCtx)
	if err != nil {
		return trace.Wrap(err)
	}
	defer serviceConn.Close()
	// Frontend acts as a client for the Postgres wire protocol.
	frontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(serviceConn), serviceConn)
	// Pass the startup message along to the Teleport database server.
	err = frontend.Send(startupMessage)
	if err != nil {
		return trace.Wrap(err)
	}
	err = p.Service.Proxy(ctx, proxyCtx, tlsConn, serviceConn)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// handleStartup handles the initial protocol exchange between the Postgres
// client (e.g. psql) and this proxy.
//
// Returns the startup message that contains initial connect parameters and
// the upgraded TLS connection.
func (p *Proxy) handleStartup(ctx context.Context, clientConn net.Conn) (*pgproto3.StartupMessage, utils.TLSConn, *pgproto3.Backend, error) {
	receivedSSLRequest := false
	receivedGSSEncRequest := false
	for {
		// Backend acts as a server for the Postgres wire protocol.
		backend := pgproto3.NewBackend(pgproto3.NewChunkReader(clientConn), clientConn)
		startupMessage, err := backend.ReceiveStartupMessage()
		if err != nil {
			return nil, nil, nil, trace.Wrap(err)
		}
		p.Log.Debugf("Received startup message: %#v.", startupMessage)
		// When initiating an encrypted connection, psql will first check with
		// the server whether it supports TLS by sending an SSLRequest message.
		//
		// Once the server has indicated the support (by sending 'S' in reply),
		// it will send a StartupMessage with the connection parameters such as
		// user name, database name, etc.
		//
		// https://www.postgresql.org/docs/13/protocol-flow.html#id-1.10.5.7.11
		switch m := startupMessage.(type) {
		case *pgproto3.SSLRequest:
			if receivedSSLRequest {
				// avoid receiving the same request over and over.
				return nil, nil, nil, trace.BadParameter("received more than one SSLRequest")
			}
			receivedSSLRequest = true
			if p.TLSConfig == nil {
				// Send 'N' back to make the client connect without TLS. Happens
				// when client connects through the local TLS proxy.
				_, err := clientConn.Write([]byte("N"))
				if err != nil {
					return nil, nil, nil, trace.Wrap(err)
				}
			} else {
				// Send 'S' back to indicate TLS support to the client.
				_, err := clientConn.Write([]byte("S"))
				if err != nil {
					return nil, nil, nil, trace.Wrap(err)
				}
				// Upgrade the connection to TLS and wait for the next message
				// which should be of the StartupMessage type.
				clientConn = tls.Server(clientConn, p.TLSConfig)
			}
			// The client may then choose to either close the connection or issue
			// another request.
			// We loop again to receive another message.
			continue
		case *pgproto3.GSSEncRequest:
			if receivedGSSEncRequest {
				// avoid receiving the same request over and over.
				return nil, nil, nil, trace.BadParameter("received more than one GSSEncRequest")
			}
			receivedGSSEncRequest = true
			// Send 'N' back to make the client connect without GSS encryption.
			_, err := clientConn.Write([]byte("N"))
			if err != nil {
				return nil, nil, nil, trace.Wrap(err)
			}
			// The client may then choose to either close the connection or issue
			// another request.
			// We loop again to receive another message.
			continue
		case *pgproto3.StartupMessage:
			// TLS connection between the client and this proxy has been
			// established, just return the startup message.
			switch tlsConn := clientConn.(type) {
			case *tls.Conn:
				return m, tlsConn, backend, nil
			case *alpnproxy.PingConn:
				return m, tlsConn, backend, nil
			default:
				return nil, nil, nil, trace.BadParameter(
					"expected tls connection, got %T", clientConn)
			}
		}
		return nil, nil, nil, trace.BadParameter(
			"unsupported startup message: %#v", startupMessage)
	}
}
