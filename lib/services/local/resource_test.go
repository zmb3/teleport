/*
Copyright 2019 Gravitational, Inc.

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
	"encoding/base32"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/services/suite"
)

func TestUserResource(t *testing.T) {
	t.Parallel()

	tt := setupServicesContext(context.Background(), t)
	runUserResourceTest(t, tt, false)
}

func TestUserResourceWithSecrets(t *testing.T) {
	t.Parallel()

	tt := setupServicesContext(context.Background(), t)
	runUserResourceTest(t, tt, true)
}

func runUserResourceTest(t *testing.T, tt *servicesContext, withSecrets bool) {
	expiry := tt.bk.Clock().Now().Add(time.Minute)

	alice := newUserTestCase(t, "alice", nil, withSecrets, expiry)
	bob := newUserTestCase(t, "bob", nil, withSecrets, expiry)

	// Check basic dynamic item creation
	runCreationChecks(t, tt, alice, bob)

	// Check that dynamically created item is compatible with service
	s := NewIdentityService(tt.bk)
	b, err := s.GetUser("bob", withSecrets)
	require.NoError(t, err)
	require.Equal(t, services.UsersEquals(bob, b), true, "dynamically inserted user does not match")
	allUsers, err := s.GetUsers(withSecrets)
	require.NoError(t, err)
	require.Equal(t, len(allUsers), 2, "expected exactly two users")
	for _, user := range allUsers {
		switch user.GetName() {
		case "alice":
			require.Equal(t, services.UsersEquals(alice, user), true, "alice does not match")
		case "bob":
			require.Equal(t, services.UsersEquals(bob, user), true, "bob does not match")
		default:
			t.Errorf("Unexpected user %q", user.GetName())
		}
	}

	// Advance the clock to let the users to expire.
	tt.bk.Clock().(clockwork.FakeClock).Advance(2 * time.Minute)
	allUsers, err = s.GetUsers(withSecrets)
	require.NoError(t, err)
	require.Equal(t, len(allUsers), 0, "expected all users to expire")
}

func TestCertAuthorityResource(t *testing.T) {
	t.Parallel()

	tt := setupServicesContext(context.Background(), t)

	userCA := suite.NewTestCA(types.UserCA, "example.com")
	hostCA := suite.NewTestCA(types.HostCA, "example.com")

	// Check basic dynamic item creation
	runCreationChecks(t, tt, userCA, hostCA)

	// Check that dynamically created item is compatible with service
	s := NewCAService(tt.bk)
	err := s.CompareAndSwapCertAuthority(userCA, userCA)
	require.NoError(t, err)
}

func TestTrustedClusterResource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tt := setupServicesContext(ctx, t)

	foo, err := types.NewTrustedCluster("foo", types.TrustedClusterSpecV2{
		Enabled:              true,
		Roles:                []string{"bar", "baz"},
		Token:                "qux",
		ProxyAddress:         "quux",
		ReverseTunnelAddress: "quuz",
	})
	require.NoError(t, err)

	bar, err := types.NewTrustedCluster("bar", types.TrustedClusterSpecV2{
		Enabled:              false,
		Roles:                []string{"baz", "aux"},
		Token:                "quux",
		ProxyAddress:         "quuz",
		ReverseTunnelAddress: "corge",
	})
	require.NoError(t, err)

	// Check basic dynamic item creation
	runCreationChecks(t, tt, foo, bar)

	s := NewPresenceService(tt.bk)
	_, err = s.GetTrustedCluster(ctx, "foo")
	require.NoError(t, err)
	_, err = s.GetTrustedCluster(ctx, "bar")
	require.NoError(t, err)
}

func TestGithubConnectorResource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tt := setupServicesContext(ctx, t)

	connector := &types.GithubConnectorV3{
		Kind:    types.KindGithubConnector,
		Version: types.V3,
		Metadata: types.Metadata{
			Name:      "github",
			Namespace: apidefaults.Namespace,
		},
		Spec: types.GithubConnectorSpecV3{
			ClientID:     "aaa",
			ClientSecret: "bbb",
			RedirectURL:  "https://localhost:3080/v1/webapi/github/callback",
			Display:      "Github",
			TeamsToLogins: []types.TeamMapping{
				{
					Organization: "gravitational",
					Team:         "admins",
					Logins:       []string{"admin"},
					KubeGroups:   []string{"system:masters"},
				},
			},
		},
	}

	// Check basic dynamic item creation
	runCreationChecks(t, tt, connector)

	s := NewIdentityService(tt.bk)
	_, err := s.GetGithubConnector(ctx, "github", true)
	require.NoError(t, err)
}

func localAuthSecretsTestCase(t *testing.T) types.LocalAuthSecrets {
	var auth types.LocalAuthSecrets
	var err error
	auth.PasswordHash, err = bcrypt.GenerateFromPassword([]byte("insecure"), bcrypt.MinCost)
	require.NoError(t, err)

	dev, err := services.NewTOTPDevice("otp", base32.StdEncoding.EncodeToString([]byte("abc123")), time.Now())
	require.NoError(t, err)
	auth.MFA = append(auth.MFA, dev)

	return auth
}

func newUserTestCase(t *testing.T, name string, roles []string, withSecrets bool, expires time.Time) types.User {
	user := types.UserV2{
		Kind:    types.KindUser,
		Version: types.V2,
		Metadata: types.Metadata{
			Name:      name,
			Namespace: apidefaults.Namespace,
			Expires:   &expires,
		},
		Spec: types.UserSpecV2{
			Roles: roles,
		},
	}
	if withSecrets {
		auth := localAuthSecretsTestCase(t)
		user.SetLocalAuth(&auth)
	}
	return &user
}

func dumpResources(t *testing.T, tt *servicesContext) []types.Resource {
	startKey := []byte("/")
	endKey := backend.RangeEnd(startKey)
	result, err := tt.bk.GetRange(context.TODO(), startKey, endKey, 0)
	require.NoError(t, err)
	resources, err := ItemsToResources(result.Items...)
	require.NoError(t, err)
	return resources
}

func runCreationChecks(t *testing.T, tt *servicesContext, resources ...types.Resource) {
	for _, rsc := range resources {
		switch r := rsc.(type) {
		case types.User:
			t.Logf("Creating User: %+v", r)
		default:
		}
	}
	err := CreateResources(context.TODO(), tt.bk, resources...)
	require.NoError(t, err)
	dump := dumpResources(t, tt)
Outer:
	for _, exp := range resources {
		for _, got := range dump {
			if got.GetKind() == exp.GetKind() && got.GetName() == exp.GetName() && got.Expiry() == exp.Expiry() {
				continue Outer
			}
		}
		t.Errorf("Missing expected resource kind=%s,name=%s,expiry=%v", exp.GetKind(), exp.GetName(), exp.Expiry().String())
	}
}
