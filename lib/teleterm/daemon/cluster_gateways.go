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

package daemon

import (
	"context"
	"strconv"

	"net"

	"github.com/gravitational/teleport/lib/client/db/postgres"
	dbprofile "github.com/gravitational/teleport/lib/client/db/profile"
	"github.com/gravitational/teleport/lib/srv/alpnproxy"

	alpncommon "github.com/gravitational/teleport/lib/srv/alpnproxy/common"
	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// CreateGatewayParams describes gateway parameters
type GatewayParams struct {
	// ResourceName is the resource name
	ResourceName string
	// HostID is the hostID of the resource agent
	HostID string
	// Port is the gateway port
	Port string
	// Protocol is the gateway protocol
	Protocol string
}

// Gateway describes local proxy that creates a gateway to the remote Teleport resource.
type Gateway struct {
	// URI is gateway URI
	URI string
	// ResourceName is the Teleport resource name
	ResourceName string
	// ClusterID is the cluster ID of the gateway
	ClusterID string
	// HostID is the gateway remote host ID
	HostID string
	// LocalPort the gateway local port
	LocalPort string
	// LocalAddress is the local address
	LocalAddress string
	// Protocol is the gateway protocol
	Protocol string
	// CACertPath
	CACertPath string
	// DBCertPath
	DBCertPath string
	// KeyPath
	KeyPath string
	// NativeClientPath is the path to native client program for quick access
	NativeClientPath string
	// NativeClientArgs is the arguments of the native client for quick access
	NativeClientArgs string

	localProxy *alpnproxy.LocalProxy
	// closeContext and closeCancel are used to signal to any waiting goroutines
	// that the local proxy is now closed and to release any resources.
	closeContext context.Context
	closeCancel  context.CancelFunc
}

func (g *Gateway) Close() {
	g.closeCancel()
}

// Open opens a gateway to Teleport proxy
func (g *Gateway) Open() {
	go func() {
		if err := g.localProxy.Start(g.closeContext); err != nil {
			log.WithError(err).Warn("Failed to open a connection.")
		}
	}()
}

// CreateGateway creates a gateway to the Teleport proxy
func (c *Cluster) CreateGateway(ctx context.Context, dbURI, port, user string) (*Gateway, error) {
	db, err := c.GetDatabase(ctx, dbURI)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := c.ReissueDBCerts(ctx, user, db); err != nil {
		return nil, trace.Wrap(err)
	}

	gateway, err := c.createGateway(GatewayParams{
		ResourceName: db.GetName(),
		Protocol:     db.GetProtocol(),
		HostID:       uri.Parse(dbURI).DB(),
		Port:         port,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.gateways = append(c.gateways, gateway)

	gateway.Open()

	return gateway, nil
}

// RemoveGateway removes Cluster gateway
func (c *Cluster) RemoveGateway(ctx context.Context, gatewayURI string) error {
	gateway, err := c.FindGateway(gatewayURI)
	if err != nil {
		return trace.Wrap(err)
	}

	gateway.Close()

	// remove closed gateway from list
	for index := range c.gateways {
		if c.gateways[index] == gateway {
			c.gateways = append(c.gateways[:index], c.gateways[index+1:]...)
			return nil
		}
	}

	return nil
}

// FindGateway finds gateway by URI
func (c *Cluster) FindGateway(gatewayURI string) (*Gateway, error) {
	for _, gateway := range c.GetGateways() {
		if gateway.URI == gatewayURI {
			return gateway, nil
		}
	}

	return nil, trace.NotFound("gateway is not found: %v", gatewayURI)
}

// GetGateways returns a list of cluster gateways
func (c *Cluster) GetGateways() []*Gateway {
	return c.gateways
}

func (c *Cluster) createGateway(params GatewayParams) (*Gateway, error) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// retreive automatically assigned port number
	_, port, err := net.SplitHostPort(listener.Addr().String())
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
			log.WithError(err).Warn("Failed to close listener.")
		}
	}()

	localProxy, err := c.newLocalProxy(closeContext, params.Protocol, listener)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ok = true
	gateway := &Gateway{
		URI:          uri.Cluster(c.status.Name).Gateway(uuid.NewString()).String(),
		LocalPort:    port,
		LocalAddress: "localhost",
		Protocol:     params.Protocol,
		HostID:       params.HostID,
		ResourceName: params.ResourceName,
		ClusterID:    c.status.Name,
		KeyPath:      c.status.KeyPath(),
		CACertPath:   c.status.CACertPath(),
		DBCertPath:   c.status.DatabaseCertPath(params.ResourceName),
		closeContext: closeContext,
		closeCancel:  closeCancel,
		localProxy:   localProxy,
	}

	if err := c.setNativeClientParameters(gateway); err != nil {
		return nil, trace.Wrap(err)
	}

	return gateway, nil
}

func (c *Cluster) newLocalProxy(ctx context.Context, protocol string, listener net.Listener) (*alpnproxy.LocalProxy, error) {
	alpnProtocol, err := alpncommon.ToALPNProtocol(protocol)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	address, err := utils.ParseAddr(c.clusterClient.WebProxyAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	lp, err := alpnproxy.NewLocalProxy(alpnproxy.LocalProxyConfig{
		InsecureSkipVerify: c.clusterClient.InsecureSkipVerify,
		RemoteProxyAddr:    c.clusterClient.WebProxyAddr,
		Protocol:           alpnProtocol,
		Listener:           listener,
		ParentContext:      ctx,
		SNI:                address.Host(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return lp, nil
}

func (c *Cluster) setNativeClientParameters(gateway *Gateway) error {
	port, err := strconv.Atoi(gateway.LocalPort)
	if err != nil {
		return trace.Wrap(err)
	}

	args := postgres.GetConnString(dbprofile.ConnectProfile{
		Host:       gateway.LocalAddress,
		Port:       port,
		Insecure:   c.clusterClient.InsecureSkipVerify,
		CACertPath: gateway.CACertPath,
		CertPath:   gateway.DBCertPath,
		KeyPath:    gateway.KeyPath,
	})

	gateway.NativeClientPath = "psql"
	gateway.NativeClientArgs = args + "&user=[DB_USER]&dbname=[DB_NAME]"
	return nil
}
