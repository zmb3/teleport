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

package handler

import (
	"context"

	"github.com/gravitational/trace"

	api "github.com/gravitational/teleport/lib/teleterm/api/protogen/golang/v1"
	"github.com/gravitational/teleport/lib/teleterm/daemon"
)

// Lists all existing clusters
func (s *Handler) ListClusters(ctx context.Context, r *api.ListClustersRequest) (*api.ListClustersResponse, error) {
	result := []*api.Cluster{}
	for _, cluster := range s.DaemonService.GetClusters() {
		result = append(result, newAPICluster(cluster))
	}

	return &api.ListClustersResponse{
		Clusters: result,
	}, nil
}

// CreateCluster creates a new cluster
func (s *Handler) CreateCluster(ctx context.Context, req *api.CreateClusterRequest) (*api.Cluster, error) {
	cluster, err := s.DaemonService.CreateCluster(ctx, req.Name)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return newAPICluster(cluster), nil
}

// GetCluster returns a cluster
func (s *Handler) GetCluster(ctx context.Context, req *api.GetClusterRequest) (*api.Cluster, error) {
	cluster, err := s.DaemonService.GetCluster(req.ClusterUri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return newAPICluster(cluster), nil
}

func newAPICluster(cluster *daemon.Cluster) *api.Cluster {
	return &api.Cluster{
		Uri:       cluster.URI,
		Name:      cluster.Name,
		Connected: cluster.Connected()}
}
