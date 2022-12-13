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

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/client"
	api "github.com/zmb3/teleport/lib/teleterm/api/protogen/golang/v1"
	"github.com/zmb3/teleport/lib/teleterm/api/uri"
)

// Kube describes kubernetes service
type Kube struct {
	// URI is the kube URI
	URI uri.ResourceURI

	KubernetesCluster types.KubeCluster
}

// GetAllKubes returns kube services
func (c *Cluster) GetAllKubes(ctx context.Context) ([]Kube, error) {
	var authClient auth.ClientI
	var proxyClient *client.ProxyClient
	var err error

	err = addMetadataToRetryableError(ctx, func() error {
		proxyClient, err = c.clusterClient.ConnectToProxy(ctx)
		if err != nil {
			return trace.Wrap(err)
		}

		return nil
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer proxyClient.Close()

	authClient, err = proxyClient.ConnectToCluster(ctx, c.clusterClient.SiteName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	defer authClient.Close()

	servers, err := authClient.GetKubernetesServers(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	kubeMap := map[string]Kube{}
	for _, server := range servers {
		kube := server.GetCluster()
		if kube == nil {
			continue
		}

		kubeMap[kube.GetName()] = Kube{
			URI:               c.URI.AppendKube(kube.GetName()),
			KubernetesCluster: kube,
		}

	}

	kubes := make([]Kube, 0, len(kubeMap))
	for _, value := range kubeMap {
		kubes = append(kubes, value)
	}

	return kubes, nil
}

// GetKubes returns a paginated kubes list
func (c *Cluster) GetKubes(ctx context.Context, r *api.GetKubesRequest) (*GetKubesResponse, error) {
	var (
		resp *types.ListResourcesResponse

		authClient  auth.ClientI
		proxyClient *client.ProxyClient
		err         error
	)

	sortBy := types.GetSortByFromString(r.SortBy)
	err = addMetadataToRetryableError(ctx, func() error {
		proxyClient, err = c.clusterClient.ConnectToProxy(ctx)
		if err != nil {
			return trace.Wrap(err)
		}
		defer proxyClient.Close()

		authClient, err = proxyClient.ConnectToCluster(ctx, c.clusterClient.SiteName)
		if err != nil {
			return trace.Wrap(err)
		}
		defer authClient.Close()

		resp, err = authClient.ListResources(ctx, proto.ListResourcesRequest{
			Namespace:           defaults.Namespace,
			ResourceType:        types.KindKubernetesCluster,
			Limit:               r.Limit,
			SortBy:              sortBy,
			StartKey:            r.StartKey,
			PredicateExpression: r.Query,
			SearchKeywords:      client.ParseSearchKeywords(r.Search, ' '),
			UseSearchAsRoles:    r.SearchAsRoles == "yes",
		})
		if err != nil {
			return trace.Wrap(err)
		}

		return nil
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	kubeClusters, err := types.ResourcesWithLabels(resp.Resources).AsKubeClusters()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	results := []Kube{}
	for _, cluster := range kubeClusters {
		results = append(results, Kube{
			URI:               c.URI.AppendKube(cluster.GetName()),
			KubernetesCluster: cluster,
		})
	}

	return &GetKubesResponse{
		Kubes:      results,
		StartKey:   resp.NextKey,
		TotalCount: resp.TotalCount,
	}, nil
}

type GetKubesResponse struct {
	Kubes []Kube
	// StartKey is the next key to use as a starting point.
	StartKey string
	// // TotalCount is the total number of resources available as a whole.
	TotalCount int
}
