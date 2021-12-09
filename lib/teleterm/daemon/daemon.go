// Copyright 2021 Gravitational, Inc
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

package daemon

import (
	"context"

	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	"github.com/gravitational/teleport/lib/teleterm/clusters"
	"github.com/gravitational/teleport/lib/teleterm/gateway"

	"github.com/gravitational/trace"
)

type Cluster = clusters.Cluster
type Gateway = gateway.Gateway
type Database = clusters.Database
type Server = clusters.Server

// Start creates and starts a Teleport Terminal service.
func New(cfg Config) (*Service, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Service{
		Config:   cfg,
		clusters: map[string]*Cluster{},
	}, nil
}

// Init loads clusters from saved profiles
func (s *Service) Init() error {
	clusters, err := s.Storage.ReadAll()
	if err != nil {
		return trace.Wrap(err)
	}

	for _, cluster := range clusters {
		s.clusters[cluster.URI] = cluster
	}

	return nil
}

// GetClusters returns a list of existing clusters
func (s *Service) GetClusters(ctx context.Context) ([]*Cluster, error) {
	clusters := make([]*Cluster, 0, len(s.clusters))
	for _, item := range s.clusters {
		clusters = append(clusters, item)
	}

	return clusters, nil
}

// AddCluster adds a cluster
func (s *Service) AddCluster(ctx context.Context, webProxyAddress string) (*Cluster, error) {
	cluster, err := s.Storage.Add(ctx, webProxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	s.clusters[cluster.URI] = cluster

	return cluster, nil
}

// RemoveCluster removes cluster
func (s *Service) RemoveCluster(ctx context.Context, clusterURI string) error {
	cluster, err := s.GetCluster(clusterURI)
	if err != nil {
		return trace.Wrap(err)
	}

	if cluster.Connected() {
		if err := cluster.Logout(ctx); err != nil {
			return trace.Wrap(err)
		}
	}

	if err := s.Storage.Remove(ctx, cluster.Name); err != nil {
		return trace.Wrap(err)
	}

	// remote from map
	delete(s.clusters, clusterURI)

	return nil
}

// GetCluster returns a cluster by its name
func (s *Service) GetCluster(clusterURI string) (*Cluster, error) {
	if cluster, exists := s.clusters[clusterURI]; exists {
		return cluster, nil
	}

	return nil, trace.NotFound("cluster is not found: %v", clusterURI)
}

func (s *Service) ClusterLogout(ctx context.Context, clusterURI string) error {
	cluster, err := s.GetCluster(clusterURI)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := cluster.Logout(ctx); err != nil {
		return trace.Wrap(err)
	}

	// Re-init the cluster from its profile because logout has many side-effects
	if s.clusters[cluster.URI], err = s.Storage.Read(cluster.Name); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// CreateGateway creates a gateway
func (s *Service) CreateGateway(ctx context.Context, targetURI string, port string) (*Gateway, error) {
	clusterUri := uri.Cluster(uri.Parse(targetURI).Cluster()).String()
	cluster, err := s.GetCluster(clusterUri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	gateway, err := cluster.CreateGateway(ctx, targetURI, port, "")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return gateway, nil
}

// ListServers returns cluster servers
func (s *Service) ListServers(ctx context.Context, clusterURI string) ([]Server, error) {
	cluster, err := s.GetCluster(clusterURI)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	servers, err := cluster.GetServers(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return servers, nil
}

// RemoveGateway removes cluster gateway
func (s *Service) RemoveGateway(ctx context.Context, gatewayURI string) error {
	clusterID := uri.Parse(gatewayURI).Cluster()
	cluster, err := s.GetCluster(uri.Cluster(clusterID).String())
	if err != nil {
		return trace.Wrap(err)
	}

	if err := cluster.RemoveGateway(ctx, gatewayURI); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (s *Service) ListGateways(ctx context.Context) ([]*Gateway, error) {
	gws := []*Gateway{}
	for _, cluster := range s.clusters {
		gws = append(gws, cluster.GetGateways()...)
	}

	return gws, nil
}

// Stop terminates all cluster open connections
func (s *Service) Stop() {
	for _, cluster := range s.clusters {
		cluster.CloseConnections()
	}
}

// Service is the cluster service
type Service struct {
	Config
	clusters map[string]*Cluster
}
