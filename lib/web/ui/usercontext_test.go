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

package ui

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/api/client/proto"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/services"
)

func TestNewUserContext(t *testing.T) {
	t.Parallel()

	user := &types.UserV2{
		Metadata: types.Metadata{
			Name: "root",
		},
	}

	// set some rules
	role1 := &types.RoleV6{}
	role1.SetNamespaces(types.Allow, []string{apidefaults.Namespace})
	role1.SetRules(types.Allow, []types.Rule{
		{
			Resources: []string{types.KindAuthConnector},
			Verbs:     services.RW(),
		},
		{
			Resources: []string{types.KindWindowsDesktop},
			Verbs:     services.RW(),
		},
	})

	// not setting the rule, or explicitly denying, both denies access
	role1.SetRules(types.Deny, []types.Rule{
		{
			Resources: []string{types.KindEvent},
			Verbs:     services.RW(),
		},
	})

	role2 := &types.RoleV6{}
	role2.SetNamespaces(types.Allow, []string{apidefaults.Namespace})
	role2.SetRules(types.Allow, []types.Rule{
		{
			Resources: []string{types.KindTrustedCluster},
			Verbs:     services.RW(),
		},
		{
			Resources: []string{types.KindBilling},
			Verbs:     services.RO(),
		},
	})

	// set some windows desktop logins
	role1.SetWindowsLogins(types.Allow, []string{"a", "b"})
	role1.SetWindowsLogins(types.Deny, []string{"c"})
	role2.SetWindowsLogins(types.Allow, []string{"d"})

	roleSet := []types.Role{role1, role2}
	userContext, err := NewUserContext(user, roleSet, proto.Features{}, true)
	require.NoError(t, err)

	allowed := access{true, true, true, true, true}
	denied := access{false, false, false, false, false}

	// test user name and acl
	require.Equal(t, userContext.Name, "root")
	require.Empty(t, cmp.Diff(userContext.ACL.AuthConnectors, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.TrustedClusters, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.AppServers, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.DBServers, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.KubeServers, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Events, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.RecordedSessions, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Roles, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Users, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Tokens, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Nodes, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.AccessRequests, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.ConnectionDiagnostic, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Desktops, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.WindowsLogins, []string{"a", "b", "d"}))
	require.Empty(t, cmp.Diff(userContext.AccessStrategy, accessStrategy{
		Type:   types.RequestStrategyOptional,
		Prompt: "",
	}))

	require.Empty(t, cmp.Diff(userContext.ACL.Billing, denied))
	require.Equal(t, userContext.ACL.Clipboard, true)
	require.Equal(t, userContext.ACL.DesktopSessionRecording, true)
	require.Empty(t, cmp.Diff(userContext.ACL.License, denied))
	require.Empty(t, cmp.Diff(userContext.ACL.Download, denied))

	// test local auth type
	require.Equal(t, userContext.AuthType, authLocal)

	// test sso auth type
	user.Spec.GithubIdentities = []types.ExternalIdentity{{ConnectorID: "foo", Username: "bar"}}
	userContext, err = NewUserContext(user, roleSet, proto.Features{}, true)
	require.NoError(t, err)
	require.Equal(t, userContext.AuthType, authSSO)

	userContext, err = NewUserContext(user, roleSet, proto.Features{Cloud: true}, true)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(userContext.ACL.Billing, access{true, true, false, false, false}))

	// test that desktopRecordingEnabled being false overrides the roleSet.RecordDesktopSession() returning true
	userContext, err = NewUserContext(user, roleSet, proto.Features{}, false)
	require.NoError(t, err)
	require.Equal(t, userContext.ACL.DesktopSessionRecording, false)
}

func TestNewUserContextCloud(t *testing.T) {
	t.Parallel()

	user := &types.UserV2{
		Metadata: types.Metadata{
			Name: "root",
		},
	}

	role := &types.RoleV6{}
	role.SetNamespaces(types.Allow, []string{"*"})
	role.SetRules(types.Allow, []types.Rule{
		{
			Resources: []string{"*"},
			Verbs:     services.RW(),
		},
	})

	role.SetWindowsLogins(types.Allow, []string{"a", "b"})
	role.SetWindowsLogins(types.Deny, []string{"c"})

	roleSet := []types.Role{role}

	allowed := access{true, true, true, true, true}

	userContext, err := NewUserContext(user, roleSet, proto.Features{Cloud: true}, true)
	require.NoError(t, err)

	require.Equal(t, userContext.Name, "root")
	require.Empty(t, cmp.Diff(userContext.ACL.AuthConnectors, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.TrustedClusters, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.AppServers, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.DBServers, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.KubeServers, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Events, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.RecordedSessions, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Roles, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Users, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Tokens, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Nodes, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.AccessRequests, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.WindowsLogins, []string{"a", "b"}))
	require.Empty(t, cmp.Diff(userContext.AccessStrategy, accessStrategy{
		Type:   types.RequestStrategyOptional,
		Prompt: "",
	}))

	require.Equal(t, userContext.ACL.Clipboard, true)
	require.Equal(t, userContext.ACL.DesktopSessionRecording, true)

	// cloud-specific asserts
	require.Empty(t, cmp.Diff(userContext.ACL.Billing, allowed))
	require.Empty(t, cmp.Diff(userContext.ACL.Desktops, allowed))
}
