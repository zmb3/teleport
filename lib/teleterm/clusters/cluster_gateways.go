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

package clusters

import (
	"context"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/lib/teleterm/gateway"
	"github.com/zmb3/teleport/lib/tlsca"
)

type CreateGatewayParams struct {
	// TargetURI is the cluster resource URI
	TargetURI string
	// TargetUser is the target user name
	TargetUser string
	// TargetSubresourceName points at a subresource of the remote resource, for example a database
	// name on a database server.
	TargetSubresourceName string
	// LocalPort is the gateway local port
	LocalPort          string
	CLICommandProvider gateway.CLICommandProvider
	TCPPortAllocator   gateway.TCPPortAllocator
	OnExpiredCert      gateway.OnExpiredCertFunc
}

// CreateGateway creates a gateway
func (c *Cluster) CreateGateway(ctx context.Context, params CreateGatewayParams) (*gateway.Gateway, error) {
	db, err := c.GetDatabase(ctx, params.TargetURI)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	routeToDatabase := tlsca.RouteToDatabase{
		ServiceName: db.GetName(),
		Protocol:    db.GetProtocol(),
		Username:    params.TargetUser,
	}

	if err := c.ReissueDBCerts(ctx, routeToDatabase); err != nil {
		return nil, trace.Wrap(err)
	}

	gw, err := gateway.New(gateway.Config{
		LocalPort:             params.LocalPort,
		TargetURI:             params.TargetURI,
		TargetUser:            params.TargetUser,
		TargetName:            db.GetName(),
		TargetSubresourceName: params.TargetSubresourceName,
		Protocol:              db.GetProtocol(),
		KeyPath:               c.status.KeyPath(),
		CertPath:              c.status.DatabaseCertPathForCluster(c.clusterClient.SiteName, db.GetName()),
		Insecure:              c.clusterClient.InsecureSkipVerify,
		WebProxyAddr:          c.clusterClient.WebProxyAddr,
		Log:                   c.Log,
		CLICommandProvider:    params.CLICommandProvider,
		TCPPortAllocator:      params.TCPPortAllocator,
		OnExpiredCert:         params.OnExpiredCert,
		Clock:                 c.clock,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return gw, nil
}
