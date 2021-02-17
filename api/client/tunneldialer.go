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

// TunnelAuthDialer connects to the Auth Server through the reverse tunnel.
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
	channel, _, err := sconn.OpenChannel(constants.ChanTransport, nil)
	if err != nil {
		return nil, false, trace.Wrap(err)
	}

	payload, err := marshalDialReq(req)
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

// marshalDialReq marshals the dial request to send over the wire.
func marshalDialReq(req *DialReq) ([]byte, error) {
	bytes, err := json.Marshal(req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return bytes, nil
}

// // FindReverseTunnel uses the web proxy to discover where the SSH reverse tunnel
// // server is running.
// func FindReverseTunnel(ctx context.Context, addrs []utils.NetAddr, insecureTLS bool) (string, error) {
// 	var errs []error
// 	for _, addr := range addrs {
// 		// In insecure mode, any certificate is accepted. In secure mode the hosts
// 		// CAs are used to validate the certificate on the proxy.
// 		resp, err := client.Find(ctx, addr.String(), insecureTLS, nil)
// 		if err == nil {
// 			return tunnelAddr(addr, resp.Proxy)
// 		}
// 		errs = append(errs, err)
// 	}
// 	return "", trace.NewAggregate(errs...)
// }

// // tunnelAddr returns the tunnel address in the following preference order:
// //  1. Reverse Tunnel Public Address.
// //  2. SSH Proxy Public Address.
// //  3. HTTP Proxy Public Address.
// //  4. Tunnel Listen Address.
// func tunnelAddr(webAddr utils.NetAddr, settings client.ProxySettings) (string, error) {
// 	// Extract the port the tunnel server is listening on.
// 	netAddr, err := utils.ParseHostPortAddr(settings.SSH.TunnelListenAddr, defaults.SSHProxyTunnelListenPort)
// 	if err != nil {
// 		return "", trace.Wrap(err)
// 	}
// 	tunnelPort := netAddr.Port(defaults.SSHProxyTunnelListenPort)

// 	// If a tunnel public address is set, nothing else has to be done, return it.
// 	if settings.SSH.TunnelPublicAddr != "" {
// 		return settings.SSH.TunnelPublicAddr, nil
// 	}

// 	// If a tunnel public address has not been set, but a related HTTP or SSH
// 	// public address has been set, extract the hostname but use the port from
// 	// the tunnel listen address.
// 	if settings.SSH.SSHPublicAddr != "" {
// 		addr, err := utils.ParseHostPortAddr(settings.SSH.SSHPublicAddr, tunnelPort)
// 		if err != nil {
// 			return "", trace.Wrap(err)
// 		}
// 		return net.JoinHostPort(addr.Host(), strconv.Itoa(tunnelPort)), nil
// 	}
// 	if settings.SSH.PublicAddr != "" {
// 		addr, err := utils.ParseHostPortAddr(settings.SSH.PublicAddr, tunnelPort)
// 		if err != nil {
// 			return "", trace.Wrap(err)
// 		}
// 		return net.JoinHostPort(addr.Host(), strconv.Itoa(tunnelPort)), nil
// 	}

// 	// If nothing is set, fallback to the address we dialed.
// 	return net.JoinHostPort(webAddr.Host(), strconv.Itoa(tunnelPort)), nil
// }
