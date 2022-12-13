/*
Copyright 2020 Gravitational, Inc.

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

package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/backend/memory"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/services/local"
)

const clusterName = "bench.example.com"

func BenchmarkGetClusterDetails(b *testing.B) {
	ctx := context.Background()

	const authCount = 6
	const proxyCount = 6

	type testCase struct {
		memory bool
		nodes  int
	}

	var tts []testCase

	for _, memory := range []bool{true, false} {
		for _, nodes := range []int{100, 1000, 10000} {
			tts = append(tts, testCase{
				memory: memory,
				nodes:  nodes,
			})
		}
	}

	for _, tt := range tts {
		// create a descriptive name for the sub-benchmark.
		name := fmt.Sprintf("tt(memory=%v,nodes=%d)", tt.memory, tt.nodes)

		// run the sub benchmark
		b.Run(name, func(sb *testing.B) {

			sb.StopTimer() // stop timer while running setup

			// configure the backend instance
			var bk backend.Backend
			var err error
			if tt.memory {
				bk, err = memory.New(memory.Config{})
				require.NoError(b, err)
			} else {
				bk, err = memory.New(memory.Config{
					Context: ctx,
				})
				require.NoError(b, err)
			}
			defer bk.Close()

			svc := local.NewPresenceService(bk)

			// seed the test nodes
			insertServers(ctx, b, svc, types.KindNode, tt.nodes)
			insertServers(ctx, b, svc, types.KindProxy, proxyCount)
			insertServers(ctx, b, svc, types.KindAuthServer, authCount)

			site := &mockRemoteSite{
				accessPoint: &mockAccessPoint{
					presence: svc,
				},
			}

			sb.StartTimer() // restart timer for benchmark operations

			benchmarkGetClusterDetails(ctx, sb, site, tt.nodes)

			sb.StopTimer() // stop timer to exclude deferred cleanup
		})
	}
}

// insertServers inserts a collection of servers into a backend.
func insertServers(ctx context.Context, b *testing.B, svc services.Presence, kind string, count int) {
	const labelCount = 10
	labels := make(map[string]string, labelCount)
	for i := 0; i < labelCount; i++ {
		labels[fmt.Sprintf("label-key-%d", i)] = fmt.Sprintf("label-val-%d", i)
	}
	for i := 0; i < count; i++ {
		name := uuid.New().String()
		addr := fmt.Sprintf("%s.%s", name, clusterName)
		server := &types.ServerV2{
			Kind:    kind,
			Version: types.V2,
			Metadata: types.Metadata{
				Name:      name,
				Namespace: apidefaults.Namespace,
				Labels:    labels,
			},
			Spec: types.ServerSpecV2{
				Addr:       addr,
				PublicAddr: addr,
				Version:    teleport.Version,
			},
		}
		var err error
		switch kind {
		case types.KindNode:
			_, err = svc.UpsertNode(ctx, server)
		case types.KindProxy:
			err = svc.UpsertProxy(server)
		case types.KindAuthServer:
			err = svc.UpsertAuthServer(server)
		default:
			b.Errorf("Unexpected server kind: %s", kind)
		}
		require.NoError(b, err)
	}
}

func benchmarkGetClusterDetails(ctx context.Context, b *testing.B, site reversetunnel.RemoteSite, nodes int, opts ...services.MarshalOption) {
	var cluster *Cluster
	var err error
	for i := 0; i < b.N; i++ {
		cluster, err = GetClusterDetails(ctx, site, opts...)
		require.NoError(b, err)
	}
	require.NotNil(b, cluster)
	require.Equal(b, nodes, cluster.NodeCount)
}

type mockRemoteSite struct {
	reversetunnel.RemoteSite
	accessPoint auth.ProxyAccessPoint
}

func (m *mockRemoteSite) CachingAccessPoint() (auth.RemoteProxyAccessPoint, error) {
	return m.accessPoint, nil
}

func (m *mockRemoteSite) GetName() string {
	return clusterName
}

func (m *mockRemoteSite) GetLastConnected() time.Time {
	return time.Now()
}

func (m *mockRemoteSite) GetStatus() string {
	return teleport.RemoteClusterStatusOnline
}

type mockAccessPoint struct {
	auth.ProxyAccessPoint
	presence *local.PresenceService
}

func (m *mockAccessPoint) GetNodes(ctx context.Context, namespace string) ([]types.Server, error) {
	return m.presence.GetNodes(ctx, namespace)
}

func (m *mockAccessPoint) GetProxies() ([]types.Server, error) {
	return m.presence.GetProxies()
}

func (m *mockAccessPoint) GetAuthServers() ([]types.Server, error) {
	return m.presence.GetAuthServers()
}
