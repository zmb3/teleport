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

package multiplexer

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/utils"
)

// TLSListenerConfig specifies listener configuration
type TLSListenerConfig struct {
	// Listener is the listener returning *tls.Conn
	// connections on Accept
	Listener net.Listener
	// ID is an identifier used for debugging purposes
	ID string
	// ReadDeadline is a connection read deadline during the TLS handshake (start
	// of the connection). It is set to defaults.HandshakeReadDeadline if
	// unspecified.
	ReadDeadline time.Duration
	// Clock is a clock to override in tests, set to real time clock
	// by default
	Clock clockwork.Clock
}

// CheckAndSetDefaults verifies configuration and sets defaults
func (c *TLSListenerConfig) CheckAndSetDefaults() error {
	if c.Listener == nil {
		return trace.BadParameter("missing parameter Listener")
	}
	if c.ReadDeadline == 0 {
		c.ReadDeadline = defaults.HandshakeReadDeadline
	}
	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}
	return nil
}

// NewTLSListener returns a new TLS listener
func NewTLSListener(cfg TLSListenerConfig) (*TLSListener, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	context, cancel := context.WithCancel(context.TODO())
	return &TLSListener{
		log: log.WithFields(log.Fields{
			trace.Component: teleport.Component("mxtls", cfg.ID),
		}),
		cfg:           cfg,
		http2Listener: newListener(context, cfg.Listener.Addr()),
		httpListener:  newListener(context, cfg.Listener.Addr()),
		cancel:        cancel,
		context:       context,
	}, nil
}

// TLSListener wraps tls.Listener and detects negotiated protocol
// (assuming it's either http/1.1 or http/2)
// and forwards the appropriate responses to either HTTP/1.1 or HTTP/2
// listeners
type TLSListener struct {
	log           *log.Entry
	cfg           TLSListenerConfig
	http2Listener *Listener
	httpListener  *Listener
	cancel        context.CancelFunc
	context       context.Context
}

// HTTP2 returns HTTP2 listener
func (l *TLSListener) HTTP2() net.Listener {
	return l.http2Listener
}

// HTTP returns HTTP listener
func (l *TLSListener) HTTP() net.Listener {
	return l.httpListener
}

// Serve accepts and forwards tls.Conn connections
func (l *TLSListener) Serve() error {
	for {
		conn, err := l.cfg.Listener.Accept()
		if err == nil {
			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				conn.Close()
				log.Errorf("Expected tls.Conn, got %T, internal usage error.", conn)
				continue
			}
			go l.detectAndForward(tlsConn)
			continue
		}
		if utils.IsUseOfClosedNetworkError(err) {
			<-l.context.Done()
			return trace.Wrap(err, "listener is closed")
		}
		select {
		case <-l.context.Done():
			return trace.Wrap(net.ErrClosed, "listener is closed")
		case <-time.After(5 * time.Second):
		}
	}
}

func (l *TLSListener) detectAndForward(conn *tls.Conn) {
	err := conn.SetReadDeadline(l.cfg.Clock.Now().Add(l.cfg.ReadDeadline))
	if err != nil {
		l.log.WithError(err).Debugf("Failed to set connection deadline.")
		conn.Close()
		return
	}

	start := l.cfg.Clock.Now()
	if err := conn.Handshake(); err != nil {
		if trace.Unwrap(err) != io.EOF {
			l.log.WithError(err).Warning("Handshake failed.")
		}
		conn.Close()
		return
	}

	// Log warning if TLS handshake takes more than one second to help debug
	// latency issues.
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		l.log.Warnf("Slow TLS handshake from %v, took %v.", conn.RemoteAddr(), time.Since(start))
	}

	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		l.log.WithError(err).Warning("Failed to reset read deadline")
		conn.Close()
		return
	}

	switch conn.ConnectionState().NegotiatedProtocol {
	case http2.NextProtoTLS:
		l.http2Listener.HandleConnection(l.context, conn)
	case teleport.HTTPNextProtoTLS, "":
		l.httpListener.HandleConnection(l.context, conn)
	default:
		conn.Close()
		l.log.WithError(err).Errorf("unsupported protocol: %v", conn.ConnectionState().NegotiatedProtocol)
	}
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *TLSListener) Close() error {
	defer l.cancel()
	return l.cfg.Listener.Close()
}

// Addr returns the listener's network address.
func (l *TLSListener) Addr() net.Addr {
	return l.cfg.Listener.Addr()
}
