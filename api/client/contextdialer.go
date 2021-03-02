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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"time"

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/utils"

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

// NewDialer makes a new dialer from a single address
func NewDialer(keepAliveInterval, dialTimeout time.Duration) ContextDialer {
	return &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}
}

// NewAddrsDialer makes a new dialer from a list of addresses
func NewAddrsDialer(addrs []string, keepAliveInterval, dialTimeout time.Duration) (ContextDialer, error) {
	if len(addrs) == 0 {
		return nil, trace.BadParameter("no addresses to dial")
	}
	dialer := NewDialer(keepAliveInterval, dialTimeout)
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

// NewTunnelDialer make a new dialer from a list of addresses over ssh
func NewTunnelDialer(ssh ssh.ClientConfig, keepAliveInterval, dialTimeout time.Duration) ContextDialer {
	dialer := NewDialer(keepAliveInterval, dialTimeout)
	return ContextDialerFunc(func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
		conn, err = dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		sconn, err := NewClientConnWithDeadline(conn, addr, &ssh)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		// Build a net.Conn over the tunnel. Make this an exclusive connection:
		// close the net.Conn as well as the channel upon close.
		conn, _, err = ConnectProxyTransport(sconn.Conn, &DialReq{
			Address: constants.RemoteAuthServer,
		}, true)
		if err != nil {
			err2 := sconn.Close()
			return nil, trace.NewAggregate(err, err2)
		}
		return conn, nil
	})
}

// NewClientConnWithDeadline establishes new client connection with specified deadline
func NewClientConnWithDeadline(conn net.Conn, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	if config.Timeout > 0 {
		conn.SetReadDeadline(time.Now().Add(config.Timeout))
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return nil, err
	}
	if config.Timeout > 0 {
		conn.SetReadDeadline(time.Time{})
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// ConnectProxyTransport opens a channel over the remote tunnel and connects
// to the requested host.
func ConnectProxyTransport(sconn ssh.Conn, req *DialReq, exclusive bool) (*utils.ChConn, bool, error) {
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, false, trace.Wrap(err)
	}

	channel, _, err := sconn.OpenChannel(constants.ChanTransport, nil)
	if err != nil {
		return nil, false, trace.Wrap(err)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, false, trace.Wrap(err)
	}

	// Send a special SSH out-of-band request called "teleport-transport"
	// the agent on the other side will create a new TCP/IP connection to
	// 'addr' on its network and will start proxying that connection over
	// this SSH channel.
	ok, err := channel.SendRequest(constants.ChanTransportDialReq, true, payload)
	if err != nil {
		return nil, true, trace.Wrap(err)
	}
	if !ok {
		defer channel.Close()

		// Pull the error message from the tunnel client (remote cluster)
		// passed to us via stderr.
		errMessage, _ := ioutil.ReadAll(channel.Stderr())
		errMessage = bytes.TrimSpace(errMessage)
		if len(errMessage) == 0 {
			errMessage = []byte(fmt.Sprintf("failed connecting to %v [%v]", req.Address, req.ServerID))
		}
		return nil, false, trace.Errorf(string(errMessage))
	}

	if exclusive {
		return utils.NewExclusiveChConn(sconn, channel), false, nil
	}
	return utils.NewChConn(sconn, channel), false, nil
}

// DialReq is a request for the address to connect to. Supports special
// non-resolvable addresses and search names if connection over a tunnel.
type DialReq struct {
	// Address is the target host to make a connection to.
	Address string `json:"address,omitempty"`

	// ServerID is the hostUUID.clusterName of the node. ServerID is used when
	// dialing through a tunnel to SSH and application nodes.
	ServerID string `json:"server_id,omitempty"`

	// ConnType is the type of connection requested, either node or application.
	ConnType types.TunnelType `json:"conn_type"`
}

// CheckAndSetDefaults verifies all the values are valid.
func (d *DialReq) CheckAndSetDefaults() error {
	if d.ConnType == "" {
		d.ConnType = types.NodeTunnel
	}

	if d.Address == "" && d.ServerID == "" {
		return trace.BadParameter("serverID or address required")
	}
	return nil
}
