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

package local

import (
	"context"
	"testing"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/backend/memory"
)

func TestSystemRoleAssertions(t *testing.T) {
	const serverID = "test-server"
	const assertionID = "test-assertion"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backend, err := memory.New(memory.Config{
		Context: ctx,
		Clock:   clockwork.NewFakeClock(),
	})
	require.NoError(t, err)

	defer backend.Close()

	assertion := NewAssertionReplayService(backend)
	unstable := NewUnstableService(backend, assertion)

	_, err = unstable.GetSystemRoleAssertions(ctx, serverID, assertionID)
	require.True(t, trace.IsNotFound(err))

	roles := []types.SystemRole{
		types.RoleNode,
		types.RoleAuth,
		types.RoleProxy,
	}

	expect := make(map[types.SystemRole]struct{})

	for _, role := range roles {
		expect[role] = struct{}{}
		err = unstable.AssertSystemRole(ctx, proto.UnstableSystemRoleAssertion{
			ServerID:    serverID,
			AssertionID: assertionID,
			SystemRole:  role,
		})
		require.NoError(t, err)

		assertions, err := unstable.GetSystemRoleAssertions(ctx, serverID, assertionID)
		require.NoError(t, err)

		require.Equal(t, len(expect), len(assertions.SystemRoles))

		for _, r := range assertions.SystemRoles {
			_, ok := expect[r]
			require.True(t, ok)
		}
	}
}
