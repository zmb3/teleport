/*
Copyright 2022 Gravitational, Inc.

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

package gcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	containerpb "cloud.google.com/go/container/apiv1/containerpb"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"k8s.io/client-go/rest"

	"github.com/zmb3/teleport/api/types"
)

func Test_gcpGKEClient_ListClusters(t *testing.T) {
	type fields struct {
		client      *fakeGKEClient
		tokenSource *fakeTokenSource
	}
	type args struct {
		ctx       context.Context
		projectID string
		location  string
	}

	tests := []struct {
		name          string
		fields        fields
		args          args
		want          []GKECluster
		errValidation func(t require.TestingT, err error, msgAndArgs ...interface{})
	}{
		{
			name: "list wildcard region",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "",
				},
			},
			args: args{
				ctx:       context.Background(),
				projectID: "p1",
				location:  types.Wildcard,
			},
			want: []GKECluster{
				{
					Name:        "cluster1",
					Description: "desc1",
					Status:      containerpb.Cluster_RUNNING,
					Location:    "region1",
					ProjectID:   "p1",
					Labels:      labels,
				},
				{
					Name:        "cluster2",
					Description: "desc2",
					Status:      containerpb.Cluster_RUNNING,
					Location:    "region1",
					ProjectID:   "p1",
					Labels:      labels,
				},
				{
					Name:        "cluster3",
					Description: "desc3",
					Status:      containerpb.Cluster_RUNNING,
					Location:    "region3",
					ProjectID:   "p1",
					Labels:      labels,
				},
			},
			errValidation: require.NoError,
		},
		{
			name: "list specifc region",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "",
				},
			},
			args: args{
				ctx:       context.Background(),
				projectID: "p1",
				location:  "region1",
			},
			want: []GKECluster{
				{
					Name:        "cluster1",
					Description: "desc1",
					Status:      containerpb.Cluster_RUNNING,
					Location:    "region1",
					ProjectID:   "p1",
					Labels:      labels,
				},
				{
					Name:        "cluster2",
					Description: "desc2",
					Status:      containerpb.Cluster_RUNNING,
					Location:    "region1",
					ProjectID:   "p1",
					Labels:      labels,
				},
			},
			errValidation: require.NoError,
		},
		{
			name: "list invalid region",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "",
				},
			},
			args: args{
				ctx:       context.Background(),
				projectID: "p1",
				location:  "region99",
			},
			want:          nil,
			errValidation: require.Error,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewGKEClientWithConfig(
				tt.args.ctx,
				GKEClientConfig{
					ClusterClient: tt.fields.client,
					TokenSource:   tt.fields.tokenSource,
				},
			)
			require.NoError(t, err)

			got, err := client.ListClusters(tt.args.ctx, tt.args.projectID, tt.args.location)
			tt.errValidation(t, err)

			sort.Slice(got, func(i, j int) bool {
				return got[i].Name < got[j].Name
			})

			require.Equal(t, got, tt.want)
		})
	}
}

type fakeGKEClient struct {
	clusters map[string]*containerpb.Cluster
}

func (f *fakeGKEClient) ListClusters(ctx context.Context, req *containerpb.ListClustersRequest, opts ...gax.CallOption) (*containerpb.ListClustersResponse, error) {
	prefix := strings.TrimSuffix(req.Parent, "-")
	clusters := &containerpb.ListClustersResponse{}

	for k, v := range f.clusters {
		if strings.HasPrefix(k, prefix) {
			clusters.Clusters = append(clusters.Clusters, v)
		}
	}
	if len(clusters.Clusters) == 0 {
		return nil, fmt.Errorf("no clusters found")
	}
	return clusters, nil
}

func (f *fakeGKEClient) GetCluster(ctx context.Context, req *containerpb.GetClusterRequest, opts ...gax.CallOption) (*containerpb.Cluster, error) {
	if c, ok := f.clusters[req.Name]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("cluster not found")
}

type fakeTokenSource struct {
	token string
	exp   time.Time
}

func (f *fakeTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		Expiry:      f.exp,
		AccessToken: f.token,
	}, nil
}

var clusters = map[string]*containerpb.Cluster{
	"projects/p1/locations/region1/clusters/cluster1": {
		Name:           "cluster1",
		Description:    "desc1",
		Status:         containerpb.Cluster_RUNNING,
		Location:       "region1",
		Endpoint:       "foobar1.com",
		ResourceLabels: labels,
		MasterAuth: &containerpb.MasterAuth{
			ClusterCaCertificate: ca,
		},
	},
	"projects/p1/locations/region1/clusters/cluster2": {
		Name:           "cluster2",
		Description:    "desc2",
		Status:         containerpb.Cluster_RUNNING,
		Location:       "region1",
		Endpoint:       "foobar2.com",
		ResourceLabels: labels,
		MasterAuth: &containerpb.MasterAuth{
			ClusterCaCertificate: ca,
		},
	},
	"projects/p1/locations/region3/clusters/cluster3": {
		Name:           "cluster3",
		Description:    "desc3",
		Status:         containerpb.Cluster_RUNNING,
		Location:       "region3",
		Endpoint:       "foobar3.com",
		ResourceLabels: labels,
		MasterAuth: &containerpb.MasterAuth{
			ClusterCaCertificate: ca,
		},
	},
}

var labels = map[string]string{
	"labels": "labels",
}

func Test_gcpGKEClient_GetClusterRestConfig(t *testing.T) {
	type fields struct {
		client      *fakeGKEClient
		tokenSource *fakeTokenSource
	}
	type args struct {
		ctx context.Context
		cfg ClusterDetails
	}

	tests := []struct {
		name               string
		fields             fields
		args               args
		expectedCfg        *rest.Config
		expectedExpiration time.Time
		errValidation      func(t require.TestingT, err error, msgAndArgs ...interface{})
	}{
		{
			name: "missing cluster",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "fake_token",
				},
			},
			args: args{
				ctx: context.Background(),
				cfg: ClusterDetails{
					ProjectID: "p1",
					Location:  "region1",
					Name:      "missing",
				},
			},
			errValidation: require.Error,
		},
		{
			name: "cluster1",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "fake_token",
					exp:   time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
				},
			},
			args: args{
				ctx: context.Background(),
				cfg: ClusterDetails{
					ProjectID: "p1",
					Location:  "region1",
					Name:      "cluster1",
				},
			},
			errValidation:      require.NoError,
			expectedExpiration: time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
			expectedCfg: &rest.Config{
				Host:        "https://foobar1.com",
				BearerToken: "fake_token",
				TLSClientConfig: rest.TLSClientConfig{
					CAData: caBytes,
				},
			},
		},
		{
			name: "cluster2",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "fake_token2",
					exp:   time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
				},
			},
			args: args{
				ctx: context.Background(),
				cfg: ClusterDetails{
					ProjectID: "p1",
					Location:  "region1",
					Name:      "cluster2",
				},
			},
			errValidation:      require.NoError,
			expectedExpiration: time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
			expectedCfg: &rest.Config{
				Host:        "https://foobar2.com",
				BearerToken: "fake_token2",
				TLSClientConfig: rest.TLSClientConfig{
					CAData: caBytes,
				},
			},
		},
		{
			name: "cluster3",
			fields: fields{
				client: &fakeGKEClient{
					clusters: clusters,
				},
				tokenSource: &fakeTokenSource{
					token: "fake_token3",
					exp:   time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
				},
			},
			args: args{
				ctx: context.Background(),
				cfg: ClusterDetails{
					ProjectID: "p1",
					Location:  "region3",
					Name:      "cluster3",
				},
			},
			errValidation:      require.NoError,
			expectedExpiration: time.Date(2022, 10, 25, 14, 0o0, 0o0, 0o0, time.Local),
			expectedCfg: &rest.Config{
				Host:        "https://foobar3.com",
				BearerToken: "fake_token3",
				TLSClientConfig: rest.TLSClientConfig{
					CAData: caBytes,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewGKEClientWithConfig(
				tt.args.ctx,
				GKEClientConfig{
					ClusterClient: tt.fields.client,
					TokenSource:   tt.fields.tokenSource,
				},
			)
			require.NoError(t, err)

			got, exp, err := client.GetClusterRestConfig(tt.args.ctx, tt.args.cfg)
			tt.errValidation(t, err)
			require.Equal(t, tt.expectedCfg, got)
			require.Equal(t, tt.expectedExpiration, exp)
		})
	}
}

var (
	ca      = base64.StdEncoding.EncodeToString(caBytes)
	caBytes = []byte("test")
)
