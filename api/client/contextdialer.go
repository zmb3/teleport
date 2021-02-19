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

package client

import (
	"context"
	"net"
	"time"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
)

// ContextDialer represents network dialer interface that uses context
type ContextDialer interface {
	// DialContext is a function that dials the specified address
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// ContextDialerFunc is a function wrapper that implements the ContextDialer interface
type ContextDialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// DialContext is a function that dials to the specified address
func (f ContextDialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// NewAddrsDialer makes a new dialer from a list of addresses
func NewAddrsDialer(addrs []string, keepAliveInterval, dialTimeout time.Duration) (ContextDialer, error) {
	if len(addrs) == 0 {
		return nil, trace.BadParameter("no addreses to dial")
	}
	dialer := net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}
	return ContextDialerFunc(func(ctx context.Context, network, _ string) (conn net.Conn, err error) {
		for _, addr := range addrs {
			conn, err = dialer.DialContext(ctx, network, addr)
			if err == nil {
				return conn, nil
			}
		}
		// not wrapping on purpose to preserve the original error
		return nil, err
	}), nil
}

// NewAuthDialer makes a new dialer from a single address
func NewAuthDialer(keepAliveInterval, dialTimeout time.Duration) (ContextDialer, error) {
	dialer := net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}
	return ContextDialerFunc(func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
		conn, err = dialer.DialContext(ctx, network, addr)
		if err == nil {
			return conn, nil
		}
		// not wrapping on purpose to preserve the original error
		return nil, err
	}), nil
}

// NewProxyDialer make a new dialer from a list of addresses over ssh
func NewProxyDialer(ssh *ssh.ClientConfig, keepAliveInterval, dialTimeout time.Duration) (ContextDialer, error) {
	if ssh == nil {
		return nil, trace.BadParameter("no ssh config")
	}
	return ContextDialerFunc(func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
		proxyDialer := &TunnelAuthDialer{
			ClientConfig: ssh,
			ProxyAddr:    addr,
		}
		conn, err = proxyDialer.DialContext(ctx, network, addr)
		if err == nil {
			return conn, nil
		}
		// TODO: try dialing it as a web-proxy addr, by looking
		// for the tunaddr - tctl.findReverseTunnel
		return nil, err
	}), nil
}
