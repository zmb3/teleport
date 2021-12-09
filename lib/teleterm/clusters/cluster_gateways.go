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

package clusters

import (
	"context"

	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	"github.com/gravitational/teleport/lib/teleterm/gateway"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
)

type Gateway = gateway.Gateway

// CreateGateway creates a gateway to the Teleport proxy
func (c *Cluster) CreateGateway(ctx context.Context, dbURI, port, user string) (*Gateway, error) {
	db, err := c.GetDatabase(ctx, dbURI)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := c.ReissueDBCerts(ctx, user, db); err != nil {
		return nil, trace.Wrap(err)
	}

	gwURI := uri.Cluster(uri.Parse(dbURI).Cluster()).Gateway(uuid.NewString())
	gw, err := gateway.New(gateway.Config{
		URI:                gwURI,
		HostID:             uri.Parse(dbURI).DB(),
		LocalPort:          port,
		ResourceName:       db.GetName(),
		Protocol:           db.GetProtocol(),
		KeyPath:            c.status.KeyPath(),
		CACertPath:         c.status.CACertPath(),
		DBCertPath:         c.status.DatabaseCertPath(db.GetName()),
		InsecureSkipVerify: c.clusterClient.InsecureSkipVerify,
		WebProxyAddr:       c.clusterClient.WebProxyAddr,
		Log:                c.Log.WithField("gateway", gwURI),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.gateways = append(c.gateways, gw)

	gw.Open()

	return gw, nil
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
		if gateway.URI.String() == gatewayURI {
			return gateway, nil
		}
	}

	return nil, trace.NotFound("gateway is not found: %v", gatewayURI)
}

// GetGateways returns a list of cluster gateways
func (c *Cluster) GetGateways() []*gateway.Gateway {
	return c.gateways
}
