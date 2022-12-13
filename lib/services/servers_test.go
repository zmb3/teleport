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

package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/defaults"
)

// TestServersCompare tests comparing two servers
func TestServersCompare(t *testing.T) {
	t.Parallel()

	node := &types.ServerV2{
		Kind:    types.KindNode,
		Version: types.V2,
		Metadata: types.Metadata{
			Name:      "node1",
			Namespace: apidefaults.Namespace,
			Labels:    map[string]string{"a": "b"},
		},
		Spec: types.ServerSpecV2{
			Addr:      "localhost:3022",
			CmdLabels: map[string]types.CommandLabelV2{"a": {Period: types.Duration(time.Minute), Command: []string{"ls", "-l"}}},
			Version:   "4.0.0",
		},
	}
	node.SetExpiry(time.Date(2018, 1, 2, 3, 4, 5, 6, time.UTC))
	// Server is equal to itself
	require.Equal(t, CompareServers(node, node), Equal)

	// Only timestamps are different
	node2 := *node
	node2.SetExpiry(time.Date(2018, 1, 2, 3, 4, 5, 8, time.UTC))
	require.Equal(t, CompareServers(node, &node2), OnlyTimestampsDifferent)

	// Labels are different
	node2 = *node
	node2.Metadata.Labels = map[string]string{"a": "d"}
	require.Equal(t, CompareServers(node, &node2), Different)

	// Command labels are different
	node2 = *node
	node2.Spec.CmdLabels = map[string]types.CommandLabelV2{"a": {Period: types.Duration(time.Minute), Command: []string{"ls", "-lR"}}}
	require.Equal(t, CompareServers(node, &node2), Different)

	// Address has changed
	node2 = *node
	node2.Spec.Addr = "localhost:3033"
	require.Equal(t, CompareServers(node, &node2), Different)

	// Public addr has changed
	node2 = *node
	node2.Spec.PublicAddr = "localhost:3033"
	require.Equal(t, CompareServers(node, &node2), Different)

	// Hostname has changed
	node2 = *node
	node2.Spec.Hostname = "luna2"
	require.Equal(t, CompareServers(node, &node2), Different)

	// TeleportVersion has changed
	node2 = *node
	node2.Spec.Version = "5.0.0"
	require.Equal(t, CompareServers(node, &node2), Different)

	// Rotation has changed
	node2 = *node
	node2.Spec.Rotation = types.Rotation{
		State:       types.RotationStateInProgress,
		Phase:       types.RotationPhaseUpdateClients,
		CurrentID:   "1",
		Started:     time.Date(2018, 3, 4, 5, 6, 7, 8, time.UTC),
		GracePeriod: types.Duration(3 * time.Hour),
		LastRotated: time.Date(2017, 2, 3, 4, 5, 6, 7, time.UTC),
		Schedule: types.RotationSchedule{
			UpdateClients: time.Date(2018, 3, 4, 5, 6, 7, 8, time.UTC),
			UpdateServers: time.Date(2018, 3, 4, 7, 6, 7, 8, time.UTC),
			Standby:       time.Date(2018, 3, 4, 5, 6, 13, 8, time.UTC),
		},
	}
	require.Equal(t, CompareServers(node, &node2), Different)
}

// TestGuessProxyHostAndVersion checks that the GuessProxyHostAndVersion
// correctly guesses the public address of the proxy (Teleport Cluster).
func TestGuessProxyHostAndVersion(t *testing.T) {
	t.Parallel()

	// No proxies passed in.
	host, version, err := GuessProxyHostAndVersion(nil)
	require.Equal(t, host, "")
	require.Equal(t, version, "")
	require.True(t, trace.IsNotFound(err))

	// No proxies have public address set.
	proxyA := types.ServerV2{}
	proxyA.Spec.Hostname = "test-A"
	proxyA.Spec.Version = "test-A"

	host, version, err = GuessProxyHostAndVersion([]types.Server{&proxyA})
	require.Equal(t, host, fmt.Sprintf("%v:%v", proxyA.Spec.Hostname, defaults.HTTPListenPort))
	require.Equal(t, version, proxyA.Spec.Version)
	require.NoError(t, err)

	// At least one proxy has public address set.
	proxyB := types.ServerV2{}
	proxyB.Spec.PublicAddr = "test-B"
	proxyB.Spec.Version = "test-B"

	host, version, err = GuessProxyHostAndVersion([]types.Server{&proxyA, &proxyB})
	require.Equal(t, host, proxyB.Spec.PublicAddr)
	require.Equal(t, version, proxyB.Spec.Version)
	require.NoError(t, err)
}

func TestUnmarshalServerKubernetes(t *testing.T) {
	t.Parallel()

	// Regression test for
	// https://github.com/gravitational/teleport/issues/4862
	//
	// Verifies unmarshaling succeeds, when provided a 4.4 server JSON
	// definition.
	tests := []struct {
		desc string
		in   string
		want *types.ServerV2
	}{
		{
			desc: "4.4 kubernetes_clusters field",
			in: `{
	"version": "v2",
	"kind": "kube_service",
	"metadata": {
		"name": "foo"
	},
	"spec": {
		"kubernetes_clusters": ["a", "b", "c"]
	}
}`,
			want: &types.ServerV2{
				Version: types.V2,
				Kind:    types.KindKubeService,
				Metadata: types.Metadata{
					Name:      "foo",
					Namespace: apidefaults.Namespace,
				},
			},
		},
		{
			desc: "5.0 kubernetes_clusters field",
			in: `{
	"version": "v2",
	"kind": "kube_service",
	"metadata": {
		"name": "foo"
	},
	"spec": {
		"kube_clusters": [{"name": "a"}, {"name": "b"}, {"name": "c"}]
	}
}`,
			want: &types.ServerV2{
				Version: types.V2,
				Kind:    types.KindKubeService,
				Metadata: types.Metadata{
					Name:      "foo",
					Namespace: apidefaults.Namespace,
				},
				Spec: types.ServerSpecV2{
					KubernetesClusters: []*types.KubernetesCluster{
						{Name: "a"},
						{Name: "b"},
						{Name: "c"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got, err := UnmarshalServer([]byte(tt.in), types.KindKubeService)
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(got, tt.want))
		})
	}
}

// TestOnlyTimestampsDifferent tests that OnlyTimestampsDifferent is returned
// after checking that whether KubernetesClusters and Apps are different.
func TestOnlyTimestampsDifferent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	later := now.Add(time.Minute)

	tests := []struct {
		desc   string
		a      types.Resource
		b      types.Resource
		expect int
	}{
		{
			desc: "Kube cluster change returns Different",
			a: &types.ServerV2{
				Spec: types.ServerSpecV2{KubernetesClusters: []*types.KubernetesCluster{}},
				Metadata: types.Metadata{
					Expires: &now,
				},
			},
			b: &types.ServerV2{
				Spec: types.ServerSpecV2{KubernetesClusters: []*types.KubernetesCluster{{
					Name: "test-cluster",
				}}},
				Metadata: types.Metadata{
					Expires: &later,
				},
			},
			expect: Different,
		},
		{
			desc: "No kube cluster change returns OnlyTimestampsDifferent",
			a: &types.ServerV2{
				Spec: types.ServerSpecV2{KubernetesClusters: []*types.KubernetesCluster{}},
				Metadata: types.Metadata{
					Expires: &now,
				},
			},
			b: &types.ServerV2{
				Spec: types.ServerSpecV2{KubernetesClusters: []*types.KubernetesCluster{}},
				Metadata: types.Metadata{
					Expires: &later,
				},
			},
			expect: OnlyTimestampsDifferent,
		},
		{
			desc: "Apps change returns Different",
			a: &types.ServerV2{
				Spec: types.ServerSpecV2{Apps: []*types.App{}},
				Metadata: types.Metadata{
					Expires: &now,
				},
			},
			b: &types.ServerV2{
				Spec: types.ServerSpecV2{Apps: []*types.App{{
					Name: "test-app",
				}}},
				Metadata: types.Metadata{
					Expires: &later,
				},
			},
			expect: Different,
		},
		{
			desc: "No apps change returns OnlyTimestampsDifferent",
			a: &types.ServerV2{
				Spec: types.ServerSpecV2{Apps: []*types.App{}},
				Metadata: types.Metadata{
					Expires: &now,
				},
			},
			b: &types.ServerV2{
				Spec: types.ServerSpecV2{Apps: []*types.App{}},
				Metadata: types.Metadata{
					Expires: &later,
				},
			},
			expect: OnlyTimestampsDifferent,
		},
	}

	for _, tc := range tests {
		got := CompareServers(tc.a, tc.b)
		require.Equal(t, tc.expect, got, tc.desc)
	}
}
