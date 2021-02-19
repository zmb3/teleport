/*
Copyright 2021 Gravitational, Inc.

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

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/utils/proxy"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
)

// TunnelAuthDialer connects to the Auth Server through the proxy SSH tunnel.
type TunnelAuthDialer struct {
	// ProxyAddr is the address of the proxy
	ProxyAddr string
	// ClientConfig is SSH tunnel client config
	ClientConfig *ssh.ClientConfig
}

// DialContext dials auth server via SSH tunnel
func (t *TunnelAuthDialer) DialContext(ctx context.Context, network string, _ string) (net.Conn, error) {
	// Connect to the reverse tunnel server.
	dialer := proxy.DialerFromEnvironment(t.ProxyAddr)
	sconn, err := dialer.Dial("tcp", t.ProxyAddr, t.ClientConfig)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Build a net.Conn over the tunnel. Make this an exclusive connection:
	// close the net.Conn as well as the channel upon close.
	conn, _, err := ConnectProxyTransport(sconn.Conn, &DialReq{
		Address: constants.RemoteAuthServer,
	}, true)
	if err != nil {
		err2 := sconn.Close()
		return nil, trace.NewAggregate(err, err2)
	}
	return conn, nil
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
	ConnType services.TunnelType `json:"conn_type"`
}

// CheckAndSetDefaults verifies all the values are valid.
func (d *DialReq) CheckAndSetDefaults() error {
	if d.ConnType == "" {
		d.ConnType = services.NodeTunnel
	}

	if d.Address == "" && d.ServerID == "" {
		return trace.BadParameter("serverID or address required")
	}
	return nil
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
