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

package handler_test

import (
	"context"
	"testing"

	api "github.com/gravitational/teleport/lib/teleterm/api/protogen/golang/v1"
	"github.com/gravitational/teleport/lib/teleterm/apiserver/handler"
	"github.com/gravitational/teleport/lib/teleterm/daemon"

	"github.com/stretchr/testify/require"
)

func TestHandler(t *testing.T) {
	d, err := daemon.New(daemon.Config{
		Dir: t.TempDir(),
	})
	require.NoError(t, err)

	h, err := handler.New(handler.Config{
		DaemonService: d,
	})
	require.NoError(t, err)

	cluster1, err := h.CreateCluster(context.TODO(), &api.CreateClusterRequest{
		Name: "cluster1",
	})
	require.NoError(t, err)

	require.Equal(t, cluster1.Name, "cluster1")
	require.False(t, cluster1.Connected)

	response, err := h.ListClusters(context.TODO(), &api.ListClustersRequest{})
	require.NoError(t, err)
	require.Len(t, response.Clusters, 1)
}
