// Copyright 2022 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/gravitational/trace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport/api/observability/tracing"
)

// Client is a wrapper around ssh.Client that adds tracing support.
type Client struct {
	*ssh.Client
	opts []tracing.Option

	// mu protects capability and rejectedError which may change based
	// on the outcome probing the server for tracing capabilities that
	// may occur trying to establish a session
	mu            sync.RWMutex
	capability    tracingCapability
	rejectedError error
}

type tracingCapability int

const (
	tracingUnknown tracingCapability = iota
	tracingUnsupported
	tracingSupported
)

// NewClient creates a new Client.
//
// The server being connected to is probed to determine if it supports
// ssh tracing. This is done by attempting to open a TracingChannel channel.
// If the channel is successfully opened then all payloads delivered to the
// server will be wrapped in an Envelope with tracing context. All Session
// and Channel created from the returned Client will honor the clients view
// of whether they should provide tracing context.
func NewClient(c ssh.Conn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request, opts ...tracing.Option) *Client {
	clt := &Client{
		Client: ssh.NewClient(c, chans, reqs),
		opts:   opts,
	}

	clt.capability, clt.rejectedError = isTracingSupported(clt.Client)

	return clt
}

// isTracingSupported determines whether the ssh server supports
// tracing payloads by trying to open a TracingChannel.
//
// Note: a channel is used instead of a global request in order prevent blocking
// forever in the event that the connection is rejected. In that case, the server
// doesn't service any global requests and writes the error to the first opened
// channel.
func isTracingSupported(clt *ssh.Client) (tracingCapability, error) {
	srvVer := clt.ServerVersion()
	if !strings.HasPrefix(string(srvVer), "SSH-2.0-Teleport") {
		// Tracing is only supported by Teleport Nodes. Skip the check for
		// all other implementations.
		// Note: If tracing is not implemented SSH server should return ssh.UnknownChannelType,
		//       but OpenSSH 7.x returns ssh.Prohibited, hence the check here.
		return tracingUnsupported, nil
	}

	ch, _, err := clt.OpenChannel(TracingChannel, nil)
	if err != nil {
		var openError *ssh.OpenChannelError
		// prohibited errors due to locks and session control are expected by callers of NewSession
		if errors.As(err, &openError) {
			switch openError.Reason {
			case ssh.Prohibited:
				return tracingUnknown, err
			case ssh.UnknownChannelType:
				return tracingUnsupported, nil
			}
		}

		return tracingUnknown, nil
	}

	_ = ch.Close()
	return tracingSupported, nil
}

// DialContext initiates a connection to the addr from the remote host.
// The resulting connection has a zero LocalAddr() and RemoteAddr().
func (c *Client) DialContext(ctx context.Context, n, addr string) (net.Conn, error) {
	tracer := tracing.NewConfig(c.opts).TracerProvider.Tracer(instrumentationName)
	ctx, span := tracer.Start(
		ctx,
		"ssh.DialContext",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			append(
				peerAttr(c.Conn.RemoteAddr()),
				attribute.String("network", n),
				attribute.String("address", addr),
				semconv.RPCServiceKey.String("ssh.Client"),
				semconv.RPCMethodKey.String("Dial"),
				semconv.RPCSystemKey.String("ssh"),
			)...,
		),
	)
	defer span.End()

	c.mu.RLock()
	// create the wrapper while the lock is held
	wrapper := &clientWrapper{
		capability: c.capability,
		Conn:       c.Client.Conn,
		opts:       c.opts,
		ctx:        ctx,
		contexts:   make(map[string][]context.Context),
	}
	c.mu.RUnlock()

	conn, err := wrapper.Dial(n, addr)
	return conn, trace.Wrap(err)
}

// SendRequest sends a global request, and returns the
// reply. If tracing is enabled, the provided payload
// is wrapped in an Envelope to forward any tracing context.
func (c *Client) SendRequest(ctx context.Context, name string, wantReply bool, payload []byte) (bool, []byte, error) {
	config := tracing.NewConfig(c.opts)
	tracer := config.TracerProvider.Tracer(instrumentationName)

	ctx, span := tracer.Start(
		ctx,
		fmt.Sprintf("ssh.GlobalRequest/%s", name),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			append(
				peerAttr(c.Conn.RemoteAddr()),
				attribute.Bool("want_reply", wantReply),
				semconv.RPCServiceKey.String("ssh.Client"),
				semconv.RPCMethodKey.String("SendRequest"),
				semconv.RPCSystemKey.String("ssh"),
			)...,
		),
	)
	defer span.End()

	c.mu.RLock()
	// If the TracingChannel was rejected when the client was created,
	// the connection was prohibited due to a lock or session control.
	// Callers to SendRequest are expecting to receive the reason the request
	// was rejected, so we need to propagate the rejectedError here.
	if c.rejectedError != nil {
		err := c.rejectedError
		c.mu.RUnlock()

		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return false, nil, trace.Wrap(err)
	}

	capability := c.capability
	c.mu.RUnlock()

	ok, resp, err := c.Client.SendRequest(name, wantReply, wrapPayload(ctx, capability, config.TextMapPropagator, payload))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	return ok, resp, err
}

// OpenChannel tries to open a channel. If tracing is enabled,
// the provided payload is wrapped in an Envelope to forward
// any tracing context.
func (c *Client) OpenChannel(ctx context.Context, name string, data []byte) (*Channel, <-chan *ssh.Request, error) {
	config := tracing.NewConfig(c.opts)
	tracer := config.TracerProvider.Tracer(instrumentationName)
	ctx, span := tracer.Start(
		ctx,
		fmt.Sprintf("ssh.OpenChannel/%s", name),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			append(
				peerAttr(c.Conn.RemoteAddr()),
				semconv.RPCServiceKey.String("ssh.Client"),
				semconv.RPCMethodKey.String("OpenChannel"),
				semconv.RPCSystemKey.String("ssh"),
			)...,
		),
	)
	defer span.End()

	c.mu.RLock()
	capability := c.capability
	c.mu.RUnlock()

	ch, reqs, err := c.Client.OpenChannel(name, wrapPayload(ctx, capability, config.TextMapPropagator, data))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	return &Channel{
		Channel: ch,
		opts:    c.opts,
	}, reqs, err
}

// NewSession creates a new SSH session that is passed tracing context
// so that spans may be correlated properly over the ssh connection.
func (c *Client) NewSession(ctx context.Context) (*Session, error) {
	tracer := tracing.NewConfig(c.opts).TracerProvider.Tracer(instrumentationName)

	ctx, span := tracer.Start(
		ctx,
		"ssh.NewSession",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			append(
				peerAttr(c.Conn.RemoteAddr()),
				semconv.RPCServiceKey.String("ssh.Client"),
				semconv.RPCMethodKey.String("NewSession"),
				semconv.RPCSystemKey.String("ssh"),
			)...,
		),
	)
	defer span.End()

	c.mu.Lock()

	// If the TracingChannel was rejected when the client was created,
	// the connection was prohibited due to a lock or session control.
	// Callers to NewSession are expecting to receive the reason the session
	// was rejected, so we need to propagate the rejectedError here.
	if c.rejectedError != nil {
		err := c.rejectedError
		c.rejectedError = nil
		c.capability = tracingUnknown
		c.mu.Unlock()

		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return nil, trace.Wrap(err)
	}

	// If the tracing capabilities of the server are unknown due to
	// prohibited errors from previous attempts to check, we need to
	// do another check to see if our connection will be permitted
	// this time.
	if c.capability == tracingUnknown {
		capability, err := isTracingSupported(c.Client)
		if err != nil {
			c.mu.Unlock()

			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return nil, trace.Wrap(err)
		}
		c.capability = capability
	}

	// create the wrapper while the lock is still held
	wrapper := &clientWrapper{
		capability: c.capability,
		Conn:       c.Client.Conn,
		opts:       c.opts,
		ctx:        ctx,
		contexts:   make(map[string][]context.Context),
	}

	c.mu.Unlock()

	// get a session from the wrapper
	session, err := wrapper.NewSession()
	return session, trace.Wrap(err)
}

// clientWrapper wraps the ssh.Conn for individual ssh.Client
// operations to intercept internal calls by the ssh.Client to
// OpenChannel. This allows for internal operations within the
// ssh.Client to have their payload wrapped in an Envelope to
// forward tracing context when tracing is enabled.
type clientWrapper struct {
	// Conn is the ssh.Conn that requests will be forwarded to
	ssh.Conn
	// capability the tracingCapability of the ssh server
	capability tracingCapability
	// ctx the context which should be used to create spans from
	ctx context.Context
	// opts the tracing options to use for creating spans with
	opts []tracing.Option

	// lock protects the context queue
	lock sync.Mutex
	// contexts a LIFO queue of context.Context per channel name.
	contexts map[string][]context.Context
}

// NewSession opens a new Session for this client.
func (c *clientWrapper) NewSession() (*Session, error) {
	// create a client that will defer to us when
	// opening the "session" channel so that we
	// can add an Envelope to the request
	client := &ssh.Client{
		Conn: c,
	}

	session, err := client.NewSession()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// wrap the session so all session requests on the channel
	// can be traced
	return &Session{
		Session: session,
		wrapper: c,
	}, nil
}

// Dial initiates a connection to the addr from the remote host.
func (c *clientWrapper) Dial(n, addr string) (net.Conn, error) {
	// create a client that will defer to us when
	// opening the "direct-tcpip" channel so that we
	// can add an Envelope to the request
	client := &ssh.Client{
		Conn: c,
	}

	return client.Dial(n, addr)
}

// addContext adds the provided context.Context to the end of
// the list for the provided channel name
func (c *clientWrapper) addContext(ctx context.Context, name string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.contexts[name] = append(c.contexts[name], ctx)
}

// nextContext returns the first context.Context for the provided
// channel name
func (c *clientWrapper) nextContext(name string) context.Context {
	c.lock.Lock()
	defer c.lock.Unlock()

	contexts, ok := c.contexts[name]
	switch {
	case !ok, len(contexts) <= 0:
		return context.Background()
	case len(contexts) == 1:
		delete(c.contexts, name)
		return contexts[0]
	default:
		c.contexts[name] = contexts[1:]
		return contexts[0]
	}
}

// OpenChannel tries to open a channel. If tracing is enabled,
// the provided payload is wrapped in an Envelope to forward
// any tracing context.
func (c *clientWrapper) OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	config := tracing.NewConfig(c.opts)
	tracer := config.TracerProvider.Tracer(instrumentationName)
	ctx, span := tracer.Start(
		c.ctx,
		fmt.Sprintf("ssh.OpenChannel/%s", name),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			append(
				peerAttr(c.Conn.RemoteAddr()),
				semconv.RPCServiceKey.String("ssh.Client"),
				semconv.RPCMethodKey.String("OpenChannel"),
				semconv.RPCSystemKey.String("ssh"),
			)...,
		),
	)
	defer span.End()

	ch, reqs, err := c.Conn.OpenChannel(name, wrapPayload(ctx, c.capability, config.TextMapPropagator, data))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	return channelWrapper{
		Channel: ch,
		manager: c,
	}, reqs, err
}

// channelWrapper wraps an ssh.Channel to allow for requests to
// contain tracing context.
type channelWrapper struct {
	ssh.Channel
	manager *clientWrapper
}

// SendRequest sends a channel request. If tracing is enabled,
// the provided payload is wrapped in an Envelope to forward
// any tracing context.
//
// It is the callers' responsibility to ensure that addContext is
// called with the appropriate context.Context prior to any
// requests being sent along the channel.
func (c channelWrapper) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	config := tracing.NewConfig(c.manager.opts)
	ctx, span := config.TracerProvider.Tracer(instrumentationName).Start(
		c.manager.nextContext(name),
		fmt.Sprintf("ssh.ChannelRequest/%s", name),
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.Bool("want_reply", wantReply),
			semconv.RPCServiceKey.String("ssh.Channel"),
			semconv.RPCMethodKey.String("SendRequest"),
			semconv.RPCSystemKey.String("ssh"),
		),
	)
	defer span.End()

	ok, err := c.Channel.SendRequest(name, wantReply, wrapPayload(ctx, c.manager.capability, config.TextMapPropagator, payload))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	return ok, err
}
