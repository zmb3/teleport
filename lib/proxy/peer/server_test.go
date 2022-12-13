// Copyright 2022 Gravitational, Inc
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

package peer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
)

// TestServerTLS ensures that only trusted certificates with the proxy role
// are accepted by the server.
func TestServerTLS(t *testing.T) {
	ca1 := newSelfSignedCA(t)
	ca2 := newSelfSignedCA(t)

	// trusted certificates with proxy roles.
	client1 := setupClient(t, ca1, ca1, types.RoleProxy)
	_, serverDef1 := setupServer(t, "s1", ca1, ca1, types.RoleProxy)
	err := client1.updateConnections([]types.Server{serverDef1})
	require.NoError(t, err)
	stream, _, err := client1.dial([]string{"s1"}, &proto.DialRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream)
	stream.CloseSend()

	// trusted certificates with incorrect server role.
	client2 := setupClient(t, ca1, ca1, types.RoleNode)
	_, serverDef2 := setupServer(t, "s2", ca1, ca1, types.RoleProxy)
	err = client2.updateConnections([]types.Server{serverDef2})
	require.NoError(t, err) // connection succeeds but is in transient failure state
	_, _, err = client2.dial([]string{"s2"}, &proto.DialRequest{})
	require.Error(t, err)

	// certificates with correct role from different CAs
	client3 := setupClient(t, ca1, ca2, types.RoleProxy)
	_, serverDef3 := setupServer(t, "s3", ca2, ca1, types.RoleProxy)
	err = client3.updateConnections([]types.Server{serverDef3})
	require.NoError(t, err)
	stream, _, err = client3.dial([]string{"s3"}, &proto.DialRequest{})
	require.NoError(t, err)
	require.NotNil(t, stream)
	stream.CloseSend()
}
