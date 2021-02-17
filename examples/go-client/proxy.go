package main

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/gravitational/teleport/api/client"
	libclient "github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/reversetunnel"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/tool/tctl/common"
	tshcommon "github.com/gravitational/teleport/tool/tsh/common"

	"github.com/gravitational/trace"
)

// cfg *service.Config
// - AuthServers

// clientConfig *common.AuthServiceClientConfig
// - SSH
// - TLS (.InsecureSkipVerify)

func connectToProxy(ctx context.Context) (*client.Config, error) {
	var err error

	// load cfg
	cfg := new(service.Config)
	cfg.CipherSuites = utils.DefaultCipherSuites()
	cfg.AuthServers, err = utils.ParseAddrs([]string{"proxy.example.com:3025", "proxy.example.com:3080"})
	fmt.Println(cfg.AuthServers)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Load clientConfig
	identityFilePath := "certs/access-admin-identity"
	clientConfig := new(common.AuthServiceClientConfig)
	key, err := tshcommon.LoadIdentity(identityFilePath)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clientConfig.TLS, err = key.TeleportClientTLSConfig(cfg.CipherSuites)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	clientConfig.SSH, err = key.ClientSSHConfig()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	clientConfig.TLS.InsecureSkipVerify = true

	// If direct dial failed, we may have a proxy address in
	// cfg.AuthServers. Try connecting to the reverse tunnel endpoint and
	// make a client over that.
	//
	// TODO(awly): this logic should be implemented once, in the auth
	// package, and reused in IoT nodes.

	errs := []error{}

	// Figure out the reverse tunnel address on the proxy first.
	tunAddr, err := findReverseTunnel(ctx, cfg.AuthServers, clientConfig.TLS.InsecureSkipVerify)
	if err != nil {
		errs = append(errs, trace.Wrap(err, "failed lookup of proxy reverse tunnel address: %v", err))
		return nil, trace.NewAggregate(errs...)
	}

	return &client.Config{
		Dialer: &reversetunnel.TunnelAuthDialer{
			ProxyAddr:    tunAddr,
			ClientConfig: clientConfig.SSH,
		},
		Credentials: []client.Credentials{
			client.LoadTLS(clientConfig.TLS),
		},
	}, nil
}

// findReverseTunnel uses the web proxy to discover where the SSH reverse tunnel
// server is running.
func findReverseTunnel(ctx context.Context, addrs []utils.NetAddr, insecureTLS bool) (string, error) {
	var errs []error
	for _, addr := range addrs {
		// In insecure mode, any certificate is accepted. In secure mode the hosts
		// CAs are used to validate the certificate on the proxy.
		resp, err := libclient.Find(ctx, addr.String(), insecureTLS, nil)
		if err == nil {
			return tunnelAddr(addr, resp.Proxy)
		}
		errs = append(errs, err)
	}
	return "", trace.NewAggregate(errs...)
}

// tunnelAddr returns the tunnel address in the following preference order:
//  1. Reverse Tunnel Public Address.
//  2. SSH Proxy Public Address.
//  3. HTTP Proxy Public Address.
//  4. Tunnel Listen Address.
func tunnelAddr(webAddr utils.NetAddr, settings libclient.ProxySettings) (string, error) {
	// Extract the port the tunnel server is listening on.
	netAddr, err := utils.ParseHostPortAddr(settings.SSH.TunnelListenAddr, defaults.SSHProxyTunnelListenPort)
	if err != nil {
		return "", trace.Wrap(err)
	}
	tunnelPort := netAddr.Port(defaults.SSHProxyTunnelListenPort)

	// If a tunnel public address is set, nothing else has to be done, return it.
	if settings.SSH.TunnelPublicAddr != "" {
		return settings.SSH.TunnelPublicAddr, nil
	}

	// If a tunnel public address has not been set, but a related HTTP or SSH
	// public address has been set, extract the hostname but use the port from
	// the tunnel listen address.
	if settings.SSH.SSHPublicAddr != "" {
		addr, err := utils.ParseHostPortAddr(settings.SSH.SSHPublicAddr, tunnelPort)
		if err != nil {
			return "", trace.Wrap(err)
		}
		return net.JoinHostPort(addr.Host(), strconv.Itoa(tunnelPort)), nil
	}
	if settings.SSH.PublicAddr != "" {
		addr, err := utils.ParseHostPortAddr(settings.SSH.PublicAddr, tunnelPort)
		if err != nil {
			return "", trace.Wrap(err)
		}
		return net.JoinHostPort(addr.Host(), strconv.Itoa(tunnelPort)), nil
	}

	// If nothing is set, fallback to the address we dialed.
	return net.JoinHostPort(webAddr.Host(), strconv.Itoa(tunnelPort)), nil
}
