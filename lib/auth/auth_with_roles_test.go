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

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509/pkix"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/api/types/installers"
	"github.com/zmb3/teleport/api/types/wrappers"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/auth/native"
	"github.com/zmb3/teleport/lib/auth/testauthority"
	libdefaults "github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/fixtures"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/session"
	"github.com/zmb3/teleport/lib/tlsca"
)

// TestLocalUserCanReissueCerts tests that local users can reissue
// certificates for themselves with varying TTLs.
func TestLocalUserCanReissueCerts(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)

	_, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	start := srv.AuthServer.Clock().Now()

	for _, test := range []struct {
		desc      string
		renewable bool
		reqTTL    time.Duration
		expiresIn time.Duration
	}{
		{
			desc:      "not-renewable",
			renewable: false,
			// expiration limited to duration of the user's session (default 1 hour)
			reqTTL:    4 * time.Hour,
			expiresIn: 1 * time.Hour,
		},
		{
			desc:      "renewable",
			renewable: true,
			// expiration is allowed to be pushed out into the future
			reqTTL:    4 * time.Hour,
			expiresIn: 4 * time.Hour,
		},
		{
			desc:      "max-renew",
			renewable: true,
			// expiration is allowed to be pushed out into the future,
			// but no more than the maximum renewable cert TTL
			reqTTL:    2 * libdefaults.MaxRenewableCertTTL,
			expiresIn: libdefaults.MaxRenewableCertTTL,
		},
	} {
		t.Run(test.desc, func(t *testing.T) {
			user, _, err := CreateUserAndRole(srv.Auth(), test.desc, []string{"role"})
			require.NoError(t, err)

			var id TestIdentity
			if test.renewable {
				id = TestRenewableUser(user.GetName(), 0)

				meta := user.GetMetadata()
				meta.Labels = map[string]string{
					types.BotGenerationLabel: "0",
				}
				user.SetMetadata(meta)
				err := srv.Auth().UpsertUser(user)
				require.NoError(t, err)
			} else {
				id = TestUser(user.GetName())
			}

			client, err := srv.NewClient(id)
			require.NoError(t, err)

			certs, err := client.GenerateUserCerts(context.Background(), proto.UserCertsRequest{
				PublicKey: pub,
				Username:  user.GetName(),
				Expires:   start.Add(test.reqTTL),
			})
			require.NoError(t, err)

			x509, err := tlsca.ParseCertificatePEM(certs.TLS)
			require.NoError(t, err)

			require.WithinDuration(t, start.Add(test.expiresIn), x509.NotAfter, 1*time.Second)
		})
	}
}

// TestSSOUserCanReissueCert makes sure that SSO user can reissue certificate
// for themselves.
func TestSSOUserCanReissueCert(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test SSO user.
	user, _, err := CreateUserAndRole(srv.Auth(), "sso-user", []string{"role"})
	require.NoError(t, err)
	user.SetCreatedBy(types.CreatedBy{
		Connector: &types.ConnectorRef{Type: "oidc", ID: "google"},
	})
	err = srv.Auth().UpdateUser(ctx, user)
	require.NoError(t, err)

	client, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)

	_, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	_, err = client.GenerateUserCerts(ctx, proto.UserCertsRequest{
		PublicKey: pub,
		Username:  user.GetName(),
		Expires:   time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
}

func TestSAMLAuthRequest(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	emptyRole, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	_, err = CreateRole(ctx, srv.Auth(), "baz", types.RoleSpecV5{})
	require.NoError(t, err)

	access1Role, err := CreateRole(ctx, srv.Auth(), "test-access-1", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindSAMLRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access2Role, err := CreateRole(ctx, srv.Auth(), "test-access-2", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindSAML},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access3Role, err := CreateRole(ctx, srv.Auth(), "test-access-3", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindSAML, types.KindSAMLRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	readerRole, err := CreateRole(ctx, srv.Auth(), "test-access-4", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindSAMLRequest},
					Verbs:     []string{types.VerbRead},
				},
			},
		},
	})
	require.NoError(t, err)

	conn, err := types.NewSAMLConnector("foo", types.SAMLConnectorSpecV2{
		Issuer:                   "test",
		SSO:                      "test",
		Cert:                     fixtures.TLSCACertPEM,
		AssertionConsumerService: "test",
		AttributesToRoles: []types.AttributeMapping{{
			Name:  "foo",
			Value: "bar",
			Roles: []string{"baz"},
		}},
	})
	require.NoError(t, err)

	err = srv.Auth().UpsertSAMLConnector(ctx, conn)
	require.NoError(t, err)

	reqNormal := types.SAMLAuthRequest{ConnectorID: conn.GetName(), Type: constants.SAML}
	reqTest := types.SAMLAuthRequest{ConnectorID: conn.GetName(), Type: constants.SAML, SSOTestFlow: true, ConnectorSpec: &types.SAMLConnectorSpecV2{
		Issuer:                   "test",
		Audience:                 "test",
		ServiceProviderIssuer:    "test",
		SSO:                      "test",
		Cert:                     fixtures.TLSCACertPEM,
		AssertionConsumerService: "test",
		AttributesToRoles: []types.AttributeMapping{{
			Name:  "foo",
			Value: "bar",
			Roles: []string{"baz"},
		}},
	}}

	tests := []struct {
		desc               string
		roles              []string
		request            types.SAMLAuthRequest
		expectAccessDenied bool
	}{
		{
			desc:               "empty role - no access",
			roles:              []string{emptyRole.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "cannot create sso test request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create normal request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create sso test request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "can create sso test request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqTest,
			expectAccessDenied: false,
		},
	}

	user, err := CreateUser(srv.Auth(), "dummy")
	require.NoError(t, err)

	userReader, err := CreateUser(srv.Auth(), "dummy-reader", readerRole)
	require.NoError(t, err)

	clientReader, err := srv.NewClient(TestUser(userReader.GetName()))
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			user.SetRoles(tt.roles)
			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			client, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			request, err := client.CreateSAMLAuthRequest(ctx, tt.request)
			if tt.expectAccessDenied {
				require.Error(t, err)
				require.True(t, trace.IsAccessDenied(err), "expected access denied, got: %v", err)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, request.ID)
			require.Equal(t, tt.request.ConnectorID, request.ConnectorID)

			requestCopy, err := clientReader.GetSAMLAuthRequest(ctx, request.ID)
			require.NoError(t, err)
			require.Equal(t, request, requestCopy)
		})
	}
}

func TestInstaller(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	_, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	_, err = CreateRole(ctx, srv.Auth(), "test-read", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindInstaller},
					Verbs:     []string{types.VerbRead},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = CreateRole(ctx, srv.Auth(), "test-update", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindInstaller},
					Verbs:     []string{types.VerbUpdate, types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = CreateRole(ctx, srv.Auth(), "test-delete", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindInstaller},
					Verbs:     []string{types.VerbDelete},
				},
			},
		},
	})
	require.NoError(t, err)
	user, err := CreateUser(srv.Auth(), "testuser")
	require.NoError(t, err)

	inst, err := types.NewInstallerV1(installers.InstallerScriptName, "contents")
	require.NoError(t, err)
	err = srv.Auth().SetInstaller(ctx, inst)
	require.NoError(t, err)

	for _, tc := range []struct {
		roles           []string
		assert          require.ErrorAssertionFunc
		installerAction func(*Client) error
	}{{
		roles:  []string{"test-empty"},
		assert: require.Error,
		installerAction: func(c *Client) error {
			_, err := c.GetInstaller(ctx, installers.InstallerScriptName)
			return err
		},
	}, {
		roles:  []string{"test-read"},
		assert: require.NoError,
		installerAction: func(c *Client) error {
			_, err := c.GetInstaller(ctx, installers.InstallerScriptName)
			return err
		},
	}, {
		roles:  []string{"test-update"},
		assert: require.NoError,
		installerAction: func(c *Client) error {
			inst, err := types.NewInstallerV1(installers.InstallerScriptName, "new-contents")
			require.NoError(t, err)
			return c.SetInstaller(ctx, inst)
		},
	}, {
		roles:  []string{"test-delete"},
		assert: require.NoError,
		installerAction: func(c *Client) error {
			err := c.DeleteInstaller(ctx, installers.InstallerScriptName)
			return err
		},
	}} {
		user.SetRoles(tc.roles)
		err = srv.Auth().UpsertUser(user)
		require.NoError(t, err)

		client, err := srv.NewClient(TestUser(user.GetName()))
		require.NoError(t, err)
		tc.assert(t, tc.installerAction(client))
	}
}

func TestOIDCAuthRequest(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	emptyRole, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	access1Role, err := CreateRole(ctx, srv.Auth(), "test-access-1", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindOIDCRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access2Role, err := CreateRole(ctx, srv.Auth(), "test-access-2", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindOIDC},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access3Role, err := CreateRole(ctx, srv.Auth(), "test-access-3", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindOIDC, types.KindOIDCRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	readerRole, err := CreateRole(ctx, srv.Auth(), "test-access-4", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindOIDCRequest},
					Verbs:     []string{types.VerbRead},
				},
			},
		},
	})
	require.NoError(t, err)

	conn, err := types.NewOIDCConnector("example", types.OIDCConnectorSpecV3{
		IssuerURL:    "https://gitlab.com",
		ClientID:     "example-client-id",
		ClientSecret: "example-client-secret",
		RedirectURLs: []string{"https://localhost:3080/v1/webapi/oidc/callback"},
		Display:      "sign in with example.com",
		Scope:        []string{"foo", "bar"},
		ClaimsToRoles: []types.ClaimMapping{
			{
				Claim: "groups",
				Value: "idp-admin",
				Roles: []string{"access"},
			},
		},
	})
	require.NoError(t, err)

	err = srv.Auth().UpsertOIDCConnector(context.Background(), conn)
	require.NoError(t, err)

	reqNormal := types.OIDCAuthRequest{ConnectorID: conn.GetName(), Type: constants.OIDC}
	reqTest := types.OIDCAuthRequest{
		ConnectorID: conn.GetName(),
		Type:        constants.OIDC,
		SSOTestFlow: true,
		ConnectorSpec: &types.OIDCConnectorSpecV3{
			IssuerURL:    "https://gitlab.com",
			ClientID:     "example-client-id",
			ClientSecret: "example-client-secret",
			RedirectURLs: []string{"https://localhost:3080/v1/webapi/oidc/callback"},
			Display:      "sign in with example.com",
			Scope:        []string{"foo", "bar"},
			ClaimsToRoles: []types.ClaimMapping{
				{
					Claim: "groups",
					Value: "idp-admin",
					Roles: []string{"access"},
				},
			},
		},
	}

	tests := []struct {
		desc               string
		roles              []string
		request            types.OIDCAuthRequest
		expectAccessDenied bool
	}{
		{
			desc:               "empty role - no access",
			roles:              []string{emptyRole.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "cannot create sso test request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create normal request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create sso test request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "can create sso test request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqTest,
			expectAccessDenied: false,
		},
	}

	user, err := CreateUser(srv.Auth(), "dummy")
	require.NoError(t, err)

	userReader, err := CreateUser(srv.Auth(), "dummy-reader", readerRole)
	require.NoError(t, err)

	clientReader, err := srv.NewClient(TestUser(userReader.GetName()))
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			user.SetRoles(tt.roles)
			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			client, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			request, err := client.CreateOIDCAuthRequest(ctx, tt.request)
			if tt.expectAccessDenied {
				require.Error(t, err)
				require.True(t, trace.IsAccessDenied(err), "expected access denied, got: %v", err)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, request.StateToken)
			require.Equal(t, tt.request.ConnectorID, request.ConnectorID)

			requestCopy, err := clientReader.GetOIDCAuthRequest(ctx, request.StateToken)
			require.NoError(t, err)
			require.Equal(t, request, requestCopy)
		})
	}
}

func TestGithubAuthRequest(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	emptyRole, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	access1Role, err := CreateRole(ctx, srv.Auth(), "test-access-1", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindGithubRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access2Role, err := CreateRole(ctx, srv.Auth(), "test-access-2", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindGithub},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	access3Role, err := CreateRole(ctx, srv.Auth(), "test-access-3", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindGithub, types.KindGithubRequest},
					Verbs:     []string{types.VerbCreate},
				},
			},
		},
	})
	require.NoError(t, err)

	readerRole, err := CreateRole(ctx, srv.Auth(), "test-access-4", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindGithubRequest},
					Verbs:     []string{types.VerbRead},
				},
			},
		},
	})
	require.NoError(t, err)

	conn, err := types.NewGithubConnector("example", types.GithubConnectorSpecV3{
		ClientID:     "example-client-id",
		ClientSecret: "example-client-secret",
		RedirectURL:  "https://localhost:3080/v1/webapi/github/callback",
		Display:      "sign in with github",
		TeamsToLogins: []types.TeamMapping{
			{
				Organization: "octocats",
				Team:         "idp-admin",
				Logins:       []string{"access"},
			},
		},
	})
	require.NoError(t, err)

	err = srv.Auth().UpsertGithubConnector(context.Background(), conn)
	require.NoError(t, err)

	reqNormal := types.GithubAuthRequest{ConnectorID: conn.GetName(), Type: constants.Github}
	reqTest := types.GithubAuthRequest{ConnectorID: conn.GetName(), Type: constants.Github, SSOTestFlow: true, ConnectorSpec: &types.GithubConnectorSpecV3{
		ClientID:     "example-client-id",
		ClientSecret: "example-client-secret",
		RedirectURL:  "https://localhost:3080/v1/webapi/github/callback",
		Display:      "sign in with github",
		TeamsToLogins: []types.TeamMapping{
			{
				Organization: "octocats",
				Team:         "idp-admin",
				Logins:       []string{"access"},
			},
		},
	}}

	tests := []struct {
		desc               string
		roles              []string
		request            types.GithubAuthRequest
		expectAccessDenied bool
	}{
		{
			desc:               "empty role - no access",
			roles:              []string{emptyRole.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "cannot create sso test request with normal access",
			roles:              []string{access1Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create normal request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: true,
		},
		{
			desc:               "cannot create sso test request with connector access",
			roles:              []string{access2Role.GetName()},
			request:            reqTest,
			expectAccessDenied: true,
		},
		{
			desc:               "can create regular request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqNormal,
			expectAccessDenied: false,
		},
		{
			desc:               "can create sso test request with combined access",
			roles:              []string{access3Role.GetName()},
			request:            reqTest,
			expectAccessDenied: false,
		},
	}

	user, err := CreateUser(srv.Auth(), "dummy")
	require.NoError(t, err)

	userReader, err := CreateUser(srv.Auth(), "dummy-reader", readerRole)
	require.NoError(t, err)

	clientReader, err := srv.NewClient(TestUser(userReader.GetName()))
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			user.SetRoles(tt.roles)
			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			client, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			request, err := client.CreateGithubAuthRequest(ctx, tt.request)
			if tt.expectAccessDenied {
				require.Error(t, err)
				require.True(t, trace.IsAccessDenied(err), "expected access denied, got: %v", err)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, request.StateToken)
			require.Equal(t, tt.request.ConnectorID, request.ConnectorID)

			requestCopy, err := clientReader.GetGithubAuthRequest(ctx, request.StateToken)
			require.NoError(t, err)
			require.Equal(t, request, requestCopy)
		})
	}
}

func TestSSODiagnosticInfo(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// empty role
	emptyRole, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	// privileged role
	privRole, err := CreateRole(ctx, srv.Auth(), "priv-access", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				{
					Resources: []string{types.KindSAMLRequest},
					Verbs:     []string{types.VerbRead},
				},
			},
		},
	})
	require.NoError(t, err)

	user, err := CreateUser(srv.Auth(), "dummy", emptyRole)
	require.NoError(t, err)

	userPriv, err := CreateUser(srv.Auth(), "superDummy", privRole)
	require.NoError(t, err)

	client, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)

	clientPriv, err := srv.NewClient(TestUser(userPriv.GetName()))
	require.NoError(t, err)

	// fresh server, no SSO diag info, request fails
	info, err := client.GetSSODiagnosticInfo(ctx, types.KindSAML, "XXX-INVALID-ID")
	require.Error(t, err)
	require.Nil(t, info)

	infoCreate := types.SSODiagnosticInfo{
		TestFlow: true,
		Error:    "aaa bbb ccc",
	}

	// invalid auth kind returns error, no information stored.
	err = srv.Auth().CreateSSODiagnosticInfo(ctx, "XXX-BAD-KIND", "ABC123", infoCreate)
	require.Error(t, err)
	info, err = client.GetSSODiagnosticInfo(ctx, "XXX-BAD-KIND", "XXX-INVALID-ID")
	require.Error(t, err)
	require.Nil(t, info)

	// proper record can be stored, retrieved, if user has access.
	err = srv.Auth().CreateSSODiagnosticInfo(ctx, types.KindSAML, "ABC123", infoCreate)
	require.NoError(t, err)

	info, err = client.GetSSODiagnosticInfo(ctx, types.KindSAML, "ABC123")
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))
	require.Nil(t, info)

	info, err = clientPriv.GetSSODiagnosticInfo(ctx, types.KindSAML, "ABC123")
	require.NoError(t, err)
	require.Equal(t, &infoCreate, info)
}

func TestGenerateUserCertsWithRoleRequest(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	emptyRole, err := CreateRole(ctx, srv.Auth(), "test-empty", types.RoleSpecV5{})
	require.NoError(t, err)

	accessFooRole, err := CreateRole(ctx, srv.Auth(), "test-access-foo", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{"foo"},
		},
	})
	require.NoError(t, err)

	accessBarRole, err := CreateRole(ctx, srv.Auth(), "test-access-bar", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{"bar"},
		},
	})
	require.NoError(t, err)

	loginsTraitsRole, err := CreateRole(ctx, srv.Auth(), "test-access-traits", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{"{{internal.logins}}"},
		},
	})
	require.NoError(t, err)

	impersonatorRole, err := CreateRole(ctx, srv.Auth(), "test-impersonator", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Impersonate: &types.ImpersonateConditions{
				Roles: []string{
					accessFooRole.GetName(),
					accessBarRole.GetName(),
					loginsTraitsRole.GetName(),
				},
			},
		},
	})
	require.NoError(t, err)

	denyBarRole, err := CreateRole(ctx, srv.Auth(), "test-deny", types.RoleSpecV5{
		Deny: types.RoleConditions{
			Impersonate: &types.ImpersonateConditions{
				Roles: []string{accessBarRole.GetName()},
			},
		},
	})
	require.NoError(t, err)

	dummyUserRole, err := types.NewRoleV3("dummy-user-role", types.RoleSpecV5{})
	require.NoError(t, err)

	dummyUser, err := CreateUser(srv.Auth(), "dummy-user", dummyUserRole)
	require.NoError(t, err)

	dummyUserImpersonatorRole, err := CreateRole(ctx, srv.Auth(), "dummy-user-impersonator", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Impersonate: &types.ImpersonateConditions{
				Users: []string{dummyUser.GetName()},
				Roles: []string{dummyUserRole.GetName()},
			},
		},
	})
	require.NoError(t, err)

	tests := []struct {
		desc             string
		username         string
		userTraits       wrappers.Traits
		roles            []string
		roleRequests     []string
		useRoleRequests  bool
		expectPrincipals []string
		expectRoles      []string
		expectError      func(error) bool
	}{
		{
			desc:             "requesting all allowed roles",
			username:         "alice",
			roles:            []string{emptyRole.GetName(), impersonatorRole.GetName()},
			roleRequests:     []string{accessFooRole.GetName(), accessBarRole.GetName()},
			useRoleRequests:  true,
			expectPrincipals: []string{"foo", "bar"},
		},
		{
			desc:     "requesting a subset of allowed roles",
			username: "bob",
			userTraits: wrappers.Traits{
				// We don't expect this login trait to appear in the principals
				// as "test-access-foo" does not contain {{internal.logins}}
				constants.TraitLogins: []string{"trait-login"},
			},
			roles:            []string{emptyRole.GetName(), impersonatorRole.GetName()},
			roleRequests:     []string{accessFooRole.GetName()},
			useRoleRequests:  true,
			expectPrincipals: []string{"foo"},
		},
		{
			// Users traits should be preserved in role impersonation
			desc:     "requesting a role preserves user traits",
			username: "ash",
			userTraits: wrappers.Traits{
				constants.TraitLogins: []string{"trait-login"},
			},
			roles: []string{
				emptyRole.GetName(),
				impersonatorRole.GetName(),
			},
			roleRequests:     []string{loginsTraitsRole.GetName()},
			useRoleRequests:  true,
			expectPrincipals: []string{"trait-login"},
		},
		{
			// Users not using role requests should keep their own roles
			desc:            "requesting no roles",
			username:        "charlie",
			roles:           []string{emptyRole.GetName()},
			roleRequests:    []string{},
			useRoleRequests: false,
			expectRoles:     []string{emptyRole.GetName()},
		},
		{
			// An empty role request should fail when role requests are
			// expected.
			desc:            "requesting no roles with UseRoleRequests",
			username:        "charlie",
			roles:           []string{emptyRole.GetName()},
			roleRequests:    []string{},
			useRoleRequests: true,
			expectError: func(err error) bool {
				return trace.IsBadParameter(err)
			},
		},
		{
			desc:            "requesting a disallowed role",
			username:        "dave",
			roles:           []string{emptyRole.GetName()},
			roleRequests:    []string{accessFooRole.GetName()},
			useRoleRequests: true,
			expectError: func(err error) bool {
				return err != nil && trace.IsAccessDenied(err)
			},
		},
		{
			desc:            "requesting a nonexistent role",
			username:        "erin",
			roles:           []string{emptyRole.GetName()},
			roleRequests:    []string{"doesnotexist"},
			useRoleRequests: true,
			expectError: func(err error) bool {
				return err != nil && trace.IsNotFound(err)
			},
		},
		{
			desc:             "requesting an allowed role with a separate deny role",
			username:         "frank",
			roles:            []string{emptyRole.GetName(), impersonatorRole.GetName(), denyBarRole.GetName()},
			roleRequests:     []string{accessFooRole.GetName()},
			useRoleRequests:  true,
			expectPrincipals: []string{"foo"},
		},
		{
			desc:            "requesting a denied role",
			username:        "geoff",
			roles:           []string{emptyRole.GetName(), impersonatorRole.GetName(), denyBarRole.GetName()},
			roleRequests:    []string{accessBarRole.GetName()},
			useRoleRequests: true,
			expectError: func(err error) bool {
				return err != nil && trace.IsAccessDenied(err)
			},
		},
		{
			desc:            "misusing a role intended for user impersonation",
			username:        "helen",
			roles:           []string{emptyRole.GetName(), dummyUserImpersonatorRole.GetName()},
			roleRequests:    []string{dummyUserRole.GetName()},
			useRoleRequests: true,
			expectError: func(err error) bool {
				return err != nil && trace.IsAccessDenied(err)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			user, err := CreateUser(srv.Auth(), tt.username)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, srv.Auth().DeleteUser(ctx, tt.username), "failed cleaning up testing user: %s", tt.username)
			})
			for _, role := range tt.roles {
				user.AddRole(role)
			}
			if tt.userTraits != nil {
				user.SetTraits(tt.userTraits)
			}
			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			client, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			_, pub, err := testauthority.New().GenerateKeyPair()
			require.NoError(t, err)

			certs, err := client.GenerateUserCerts(ctx, proto.UserCertsRequest{
				PublicKey:       pub,
				Username:        user.GetName(),
				Expires:         time.Now().Add(time.Hour),
				RoleRequests:    tt.roleRequests,
				UseRoleRequests: tt.useRoleRequests,
			})
			if tt.expectError != nil {
				require.True(t, tt.expectError(err), "error: %+v: %s", err, trace.DebugReport(err))
				return
			}
			require.NoError(t, err)

			// Parse the Identity
			impersonatedTLSCert, err := tlsca.ParseCertificatePEM(certs.TLS)
			require.NoError(t, err)
			impersonatedIdent, err := tlsca.FromSubject(impersonatedTLSCert.Subject, impersonatedTLSCert.NotAfter)
			require.NoError(t, err)

			userCert, err := sshutils.ParseCertificate(certs.SSH)
			require.NoError(t, err)

			roles, ok := userCert.Extensions[teleport.CertExtensionTeleportRoles]
			require.True(t, ok)

			parsedRoles, err := services.UnmarshalCertRoles(roles)
			require.NoError(t, err)

			if len(tt.expectPrincipals) > 0 {
				expectPrincipals := append(tt.expectPrincipals, teleport.SSHSessionJoinPrincipal)
				require.ElementsMatch(t, expectPrincipals, userCert.ValidPrincipals, "principals must match")
			}

			if tt.expectRoles != nil {
				require.ElementsMatch(t, tt.expectRoles, parsedRoles, "granted roles must match expected values")
			} else {
				require.ElementsMatch(t, tt.roleRequests, parsedRoles, "granted roles must match requests")
			}

			_, disallowReissue := userCert.Extensions[teleport.CertExtensionDisallowReissue]
			if len(tt.roleRequests) > 0 {
				impersonator, ok := userCert.Extensions[teleport.CertExtensionImpersonator]
				require.True(t, ok, "impersonator must be set if any role requests exist")
				require.Equal(t, tt.username, impersonator, "certificate must show self-impersonation")

				require.True(t, disallowReissue)
				require.True(t, impersonatedIdent.DisallowReissue)
			} else {
				require.False(t, disallowReissue)
				require.False(t, impersonatedIdent.DisallowReissue)
			}
		})
	}
}

// TestRoleRequestDenyReimpersonation make sure role requests can't be used to
// re-escalate privileges using a (perhaps compromised) set of role
// impersonated certs.
func TestRoleRequestDenyReimpersonation(t *testing.T) {
	ctx := context.Background()
	srv := newTestTLSServer(t)

	accessFooRole, err := CreateRole(ctx, srv.Auth(), "test-access-foo", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{"foo"},
		},
	})
	require.NoError(t, err)

	accessBarRole, err := CreateRole(ctx, srv.Auth(), "test-access-bar", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{"bar"},
		},
	})
	require.NoError(t, err)

	impersonatorRole, err := CreateRole(ctx, srv.Auth(), "test-impersonator", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Impersonate: &types.ImpersonateConditions{
				Roles: []string{accessFooRole.GetName(), accessBarRole.GetName()},
			},
		},
	})
	require.NoError(t, err)

	// Create a testing user.
	user, err := CreateUser(srv.Auth(), "alice")
	require.NoError(t, err)
	user.AddRole(impersonatorRole.GetName())
	err = srv.Auth().UpsertUser(user)
	require.NoError(t, err)

	// Generate cert with a role request.
	client, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)
	priv, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	// Request certs for only the `foo` role.
	certs, err := client.GenerateUserCerts(ctx, proto.UserCertsRequest{
		PublicKey:    pub,
		Username:     user.GetName(),
		Expires:      time.Now().Add(time.Hour),
		RoleRequests: []string{accessFooRole.GetName()},
	})
	require.NoError(t, err)

	// Make an impersonated client.
	impersonatedTLSCert, err := tls.X509KeyPair(certs.TLS, priv)
	require.NoError(t, err)
	impersonatedClient := srv.NewClientWithCert(impersonatedTLSCert)

	// Attempt a request.
	_, err = impersonatedClient.GetClusterName()
	require.NoError(t, err)

	// Attempt to generate new certs for a different (allowed) role.
	_, err = impersonatedClient.GenerateUserCerts(ctx, proto.UserCertsRequest{
		PublicKey:    pub,
		Username:     user.GetName(),
		Expires:      time.Now().Add(time.Hour),
		RoleRequests: []string{accessBarRole.GetName()},
	})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	// Attempt to generate new certs for the same role.
	_, err = impersonatedClient.GenerateUserCerts(ctx, proto.UserCertsRequest{
		PublicKey:    pub,
		Username:     user.GetName(),
		Expires:      time.Now().Add(time.Hour),
		RoleRequests: []string{accessFooRole.GetName()},
	})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	// Attempt to generate new certs with no role requests
	// (If allowed, this might issue certs for the original user without role
	// requests.)
	_, err = impersonatedClient.GenerateUserCerts(ctx, proto.UserCertsRequest{
		PublicKey: pub,
		Username:  user.GetName(),
		Expires:   time.Now().Add(time.Hour),
	})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))
}

// TestGenerateDatabaseCert makes sure users and services with appropriate
// permissions can generate certificates for self-hosted databases.
func TestGenerateDatabaseCert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// This user can't impersonate anyone and can't generate database certs.
	userWithoutAccess, _, err := CreateUserAndRole(srv.Auth(), "user", []string{"role1"})
	require.NoError(t, err)

	// This user can impersonate system role Db.
	userImpersonateDb, roleDb, err := CreateUserAndRole(srv.Auth(), "user-impersonate-db", []string{"role2"})
	require.NoError(t, err)
	roleDb.SetImpersonateConditions(types.Allow, types.ImpersonateConditions{
		Users: []string{string(types.RoleDatabase)},
		Roles: []string{string(types.RoleDatabase)},
	})
	require.NoError(t, srv.Auth().UpsertRole(ctx, roleDb))

	tests := []struct {
		desc     string
		identity TestIdentity
		err      string
	}{
		{
			desc:     "user can't sign database certs",
			identity: TestUser(userWithoutAccess.GetName()),
			err:      "access denied",
		},
		{
			desc:     "user can impersonate Db and sign database certs",
			identity: TestUser(userImpersonateDb.GetName()),
		},
		{
			desc:     "built-in admin can sign database certs",
			identity: TestAdmin(),
		},
		{
			desc:     "database service can sign database certs",
			identity: TestBuiltin(types.RoleDatabase),
		},
	}

	// Generate CSR once for speed sake.
	priv, err := native.GeneratePrivateKey()
	require.NoError(t, err)

	csr, err := tlsca.GenerateCertificateRequestPEM(pkix.Name{CommonName: "test"}, priv)
	require.NoError(t, err)

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			client, err := srv.NewClient(test.identity)
			require.NoError(t, err)

			_, err = client.GenerateDatabaseCert(ctx, &proto.DatabaseCertRequest{CSR: csr})
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type testDynamicallyConfigurableRBACParams struct {
	kind                          string
	storeDefault, storeConfigFile func(*Server)
	get, set, reset               func(*ServerWithRoles) error
	alwaysReadable                bool
}

// TestDynamicConfigurationRBACVerbs tests the dynamic configuration RBAC verbs described
// in rfd/0016-dynamic-configuration.md § Implementation.
func testDynamicallyConfigurableRBAC(t *testing.T, p testDynamicallyConfigurableRBACParams) {
	testAuth, err := NewTestAuthServer(TestAuthServerConfig{Dir: t.TempDir()})
	require.NoError(t, err)

	testOperation := func(op func(*ServerWithRoles) error, allowRules []types.Rule, expectErr, withConfigFile bool) func(*testing.T) {
		return func(t *testing.T) {
			if withConfigFile {
				p.storeConfigFile(testAuth.AuthServer)
			} else {
				p.storeDefault(testAuth.AuthServer)
			}
			server := serverWithAllowRules(t, testAuth, allowRules)
			opErr := op(server)
			if expectErr {
				require.Error(t, opErr)
			} else {
				require.NoError(t, opErr)
			}
		}
	}

	// runTestCases generates all non-empty RBAC verb combinations and checks the expected
	// error for each operation.
	runTestCases := func(withConfigFile bool) {
		for _, canCreate := range []bool{false, true} {
			for _, canUpdate := range []bool{false, true} {
				for _, canRead := range []bool{false, true} {
					if !canRead && !canUpdate && !canCreate {
						continue
					}
					verbs := []string{}
					expectGetErr, expectSetErr, expectResetErr := true, true, true
					if canRead || p.alwaysReadable {
						verbs = append(verbs, types.VerbRead)
						expectGetErr = false
					}
					if canUpdate {
						verbs = append(verbs, types.VerbUpdate)
						if !withConfigFile {
							expectSetErr, expectResetErr = false, false
						}
					}
					if canCreate {
						verbs = append(verbs, types.VerbCreate)
						if canUpdate {
							expectSetErr = false
						}
					}
					allowRules := []types.Rule{
						{
							Resources: []string{p.kind},
							Verbs:     verbs,
						},
					}
					t.Run(fmt.Sprintf("get %v %v", verbs, withConfigFile), testOperation(p.get, allowRules, expectGetErr, withConfigFile))
					t.Run(fmt.Sprintf("set %v %v", verbs, withConfigFile), testOperation(p.set, allowRules, expectSetErr, withConfigFile))
					t.Run(fmt.Sprintf("reset %v %v", verbs, withConfigFile), testOperation(p.reset, allowRules, expectResetErr, withConfigFile))
				}
			}
		}
	}

	runTestCases(false)
	runTestCases(true)
}

func TestAuthPreferenceRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testDynamicallyConfigurableRBAC(t, testDynamicallyConfigurableRBACParams{
		kind: types.KindClusterAuthPreference,
		storeDefault: func(s *Server) {
			s.SetAuthPreference(ctx, types.DefaultAuthPreference())
		},
		storeConfigFile: func(s *Server) {
			authPref := types.DefaultAuthPreference()
			authPref.SetOrigin(types.OriginConfigFile)
			s.SetAuthPreference(ctx, authPref)
		},
		get: func(s *ServerWithRoles) error {
			_, err := s.GetAuthPreference(ctx)
			return err
		},
		set: func(s *ServerWithRoles) error {
			return s.SetAuthPreference(ctx, types.DefaultAuthPreference())
		},
		reset: func(s *ServerWithRoles) error {
			return s.ResetAuthPreference(ctx)
		},
		alwaysReadable: true,
	})
}

func TestClusterNetworkingConfigRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testDynamicallyConfigurableRBAC(t, testDynamicallyConfigurableRBACParams{
		kind: types.KindClusterNetworkingConfig,
		storeDefault: func(s *Server) {
			s.SetClusterNetworkingConfig(ctx, types.DefaultClusterNetworkingConfig())
		},
		storeConfigFile: func(s *Server) {
			netConfig := types.DefaultClusterNetworkingConfig()
			netConfig.SetOrigin(types.OriginConfigFile)
			s.SetClusterNetworkingConfig(ctx, netConfig)
		},
		get: func(s *ServerWithRoles) error {
			_, err := s.GetClusterNetworkingConfig(ctx)
			return err
		},
		set: func(s *ServerWithRoles) error {
			return s.SetClusterNetworkingConfig(ctx, types.DefaultClusterNetworkingConfig())
		},
		reset: func(s *ServerWithRoles) error {
			return s.ResetClusterNetworkingConfig(ctx)
		},
	})
}

func TestSessionRecordingConfigRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testDynamicallyConfigurableRBAC(t, testDynamicallyConfigurableRBACParams{
		kind: types.KindSessionRecordingConfig,
		storeDefault: func(s *Server) {
			s.SetSessionRecordingConfig(ctx, types.DefaultSessionRecordingConfig())
		},
		storeConfigFile: func(s *Server) {
			recConfig := types.DefaultSessionRecordingConfig()
			recConfig.SetOrigin(types.OriginConfigFile)
			s.SetSessionRecordingConfig(ctx, recConfig)
		},
		get: func(s *ServerWithRoles) error {
			_, err := s.GetSessionRecordingConfig(ctx)
			return err
		},
		set: func(s *ServerWithRoles) error {
			return s.SetSessionRecordingConfig(ctx, types.DefaultSessionRecordingConfig())
		},
		reset: func(s *ServerWithRoles) error {
			return s.ResetSessionRecordingConfig(ctx)
		},
	})
}

// TestGetAndList_Nodes users can retrieve nodes with various filters
// and with the appropriate permissions.
func TestGetAndList_Nodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test nodes.
	for i := 0; i < 10; i++ {
		name := uuid.New().String()
		node, err := types.NewServerWithLabels(
			name,
			types.KindNode,
			types.ServerSpecV2{},
			map[string]string{"name": name},
		)
		require.NoError(t, err)

		_, err = srv.Auth().UpsertNode(ctx, node)
		require.NoError(t, err)
	}

	testNodes, err := srv.Auth().GetNodes(ctx, defaults.Namespace)
	require.NoError(t, err)

	// create user, role, and client
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// permit user to list all nodes
	role.SetNodeLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	// Convert nodes retrieved earlier as types.ResourcesWithLabels
	testResources := make([]types.ResourceWithLabels, len(testNodes))
	for i, node := range testNodes {
		testResources[i] = node
	}

	// listing nodes 0-4 should list first 5 nodes
	resp, err := clt.ListResources(ctx, proto.ListResourcesRequest{
		ResourceType: types.KindNode,
		Namespace:    defaults.Namespace,
		Limit:        5,
	})
	require.NoError(t, err)
	require.Len(t, resp.Resources, 5)
	expectedNodes := testResources[:5]
	require.Empty(t, cmp.Diff(expectedNodes, resp.Resources))

	// remove permission for third node
	role.SetNodeLabels(types.Deny, types.Labels{"name": {testResources[3].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	// listing nodes 0-4 should skip the third node and add the fifth to the end.
	resp, err = clt.ListResources(ctx, proto.ListResourcesRequest{
		ResourceType: types.KindNode,
		Namespace:    defaults.Namespace,
		Limit:        5,
	})
	require.NoError(t, err)
	require.Len(t, resp.Resources, 5)
	expectedNodes = append(testResources[:3], testResources[4:6]...)
	require.Empty(t, cmp.Diff(expectedNodes, resp.Resources))

	// Test various filtering.
	baseRequest := proto.ListResourcesRequest{
		ResourceType: types.KindNode,
		Namespace:    defaults.Namespace,
		Limit:        int32(len(testResources) + 1),
	}

	// Test label match.
	withLabels := baseRequest
	withLabels.Labels = map[string]string{"name": testResources[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test search keywords match.
	withSearchKeywords := baseRequest
	withSearchKeywords.SearchKeywords = []string{"name", testResources[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test expression match.
	withExpression := baseRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testResources[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))
}

// TestStreamSessionEvents_User ensures that when a user streams a session's events, it emits an audit event.
func TestStreamSessionEvents_User(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newTestTLSServer(t)

	username := "user"
	user, _, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)

	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// ignore the response as we don't want the events or the error (the session will not exist)
	_, _ = clt.StreamSessionEvents(ctx, "44c6cea8-362f-11ea-83aa-125400432324", 0)

	// we need to wait for a short period to ensure the event is returned
	time.Sleep(500 * time.Millisecond)

	searchEvents, _, err := srv.AuthServer.AuditLog.SearchEvents(
		srv.Clock().Now().Add(-time.Hour),
		srv.Clock().Now().Add(time.Hour),
		defaults.Namespace,
		[]string{events.SessionRecordingAccessEvent},
		1,
		types.EventOrderDescending,
		"",
	)
	require.NoError(t, err)

	event := searchEvents[0].(*apievents.SessionRecordingAccess)
	require.Equal(t, username, event.User)
}

// TestStreamSessionEvents_Builtin ensures that when a builtin role streams a session's events, it does not emit
// an audit event.
func TestStreamSessionEvents_Builtin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newTestTLSServer(t)

	identity := TestBuiltin(types.RoleProxy)
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// ignore the response as we don't want the events or the error (the session will not exist)
	_, _ = clt.StreamSessionEvents(ctx, "44c6cea8-362f-11ea-83aa-125400432324", 0)

	// we need to wait for a short period to ensure the event is returned
	time.Sleep(500 * time.Millisecond)

	searchEvents, _, err := srv.AuthServer.AuditLog.SearchEvents(
		srv.Clock().Now().Add(-time.Hour),
		srv.Clock().Now().Add(time.Hour),
		defaults.Namespace,
		[]string{events.SessionRecordingAccessEvent},
		1,
		types.EventOrderDescending,
		"",
	)
	require.NoError(t, err)

	require.Equal(t, 0, len(searchEvents))
}

// TestGetSessionEvents ensures that when a user streams a session's events, it emits an audit event.
func TestGetSessionEvents(t *testing.T) {
	t.Parallel()

	srv := newTestTLSServer(t)

	username := "user"
	user, _, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)

	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// ignore the response as we don't want the events or the error (the session will not exist)
	_, _ = clt.GetSessionEvents(defaults.Namespace, "44c6cea8-362f-11ea-83aa-125400432324", 0, false)

	// we need to wait for a short period to ensure the event is returned
	time.Sleep(500 * time.Millisecond)

	searchEvents, _, err := srv.AuthServer.AuditLog.SearchEvents(
		srv.Clock().Now().Add(-time.Hour),
		srv.Clock().Now().Add(time.Hour),
		defaults.Namespace,
		[]string{events.SessionRecordingAccessEvent},
		1,
		types.EventOrderDescending,
		"",
	)
	require.NoError(t, err)

	event := searchEvents[0].(*apievents.SessionRecordingAccess)
	require.Equal(t, username, event.User)
}

// TestAPILockedOut tests Auth API when there are locks involved.
func TestAPILockedOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create user, role and client.
	user, role, err := CreateUserAndRole(srv.Auth(), "test-user", nil)
	require.NoError(t, err)
	clt, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)

	// Prepare an operation requiring authorization.
	testOp := func() error {
		_, err := clt.GetUser(user.GetName(), false)
		return err
	}

	// With no locks, the operation should pass with no error.
	require.NoError(t, testOp())

	// With a lock targeting the user, the operation should be denied.
	lock, err := types.NewLock("user-lock", types.LockSpecV2{
		Target: types.LockTarget{User: user.GetName()},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Auth().UpsertLock(ctx, lock))
	require.Eventually(t, func() bool { return trace.IsAccessDenied(testOp()) }, time.Second, time.Second/10)

	// Delete the lock.
	require.NoError(t, srv.Auth().DeleteLock(ctx, lock.GetName()))
	require.Eventually(t, func() bool { return testOp() == nil }, time.Second, time.Second/10)

	// Create a new lock targeting the user's role.
	roleLock, err := types.NewLock("role-lock", types.LockSpecV2{
		Target: types.LockTarget{Role: role.GetName()},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Auth().UpsertLock(ctx, roleLock))
	require.Eventually(t, func() bool { return trace.IsAccessDenied(testOp()) }, time.Second, time.Second/10)
}

func serverWithAllowRules(t *testing.T, srv *TestAuthServer, allowRules []types.Rule) *ServerWithRoles {
	username := "test-user"
	_, role, err := CreateUserAndRoleWithoutRoles(srv.AuthServer, username, nil)
	require.NoError(t, err)
	role.SetRules(types.Allow, allowRules)
	err = srv.AuthServer.UpsertRole(context.TODO(), role)
	require.NoError(t, err)

	localUser := LocalUser{Username: username, Identity: tlsca.Identity{Username: username}}
	authContext, err := contextForLocalUser(localUser, srv.AuthServer, srv.ClusterName)
	require.NoError(t, err)

	return &ServerWithRoles{
		authServer: srv.AuthServer,
		sessions:   srv.SessionServer,
		alog:       srv.AuditLog,
		context:    *authContext,
	}
}

// TestDatabasesCRUDRBAC verifies RBAC is applied to database CRUD methods.
func TestDatabasesCRUDRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Setup a couple of users:
	// - "dev" only has access to databases with labels env=dev
	// - "admin" has access to all databases
	dev, devRole, err := CreateUserAndRole(srv.Auth(), "dev", nil)
	require.NoError(t, err)
	devRole.SetDatabaseLabels(types.Allow, types.Labels{"env": {"dev"}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, devRole))
	devClt, err := srv.NewClient(TestUser(dev.GetName()))
	require.NoError(t, err)

	admin, adminRole, err := CreateUserAndRole(srv.Auth(), "admin", nil)
	require.NoError(t, err)
	adminRole.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, adminRole))
	adminClt, err := srv.NewClient(TestUser(admin.GetName()))
	require.NoError(t, err)

	// Prepare a couple of database resources.
	devDatabase, err := types.NewDatabaseV3(types.Metadata{
		Name:   "dev",
		Labels: map[string]string{"env": "dev", types.OriginLabel: types.OriginDynamic},
	}, types.DatabaseSpecV3{
		Protocol: libdefaults.ProtocolPostgres,
		URI:      "localhost:5432",
	})
	require.NoError(t, err)
	adminDatabase, err := types.NewDatabaseV3(types.Metadata{
		Name:   "admin",
		Labels: map[string]string{"env": "prod", types.OriginLabel: types.OriginDynamic},
	}, types.DatabaseSpecV3{
		Protocol: libdefaults.ProtocolMySQL,
		URI:      "localhost:3306",
	})
	require.NoError(t, err)

	// Dev shouldn't be able to create prod database...
	err = devClt.CreateDatabase(ctx, adminDatabase)
	require.True(t, trace.IsAccessDenied(err))

	// ... but can create dev database.
	err = devClt.CreateDatabase(ctx, devDatabase)
	require.NoError(t, err)

	// Admin can create prod database.
	err = adminClt.CreateDatabase(ctx, adminDatabase)
	require.NoError(t, err)

	// Dev shouldn't be able to update prod database...
	err = devClt.UpdateDatabase(ctx, adminDatabase)
	require.True(t, trace.IsAccessDenied(err))

	// ... but can update dev database.
	err = devClt.UpdateDatabase(ctx, devDatabase)
	require.NoError(t, err)

	// Dev shouldn't be able to update labels on the prod database.
	adminDatabase.SetStaticLabels(map[string]string{"env": "dev", types.OriginLabel: types.OriginDynamic})
	err = devClt.UpdateDatabase(ctx, adminDatabase)
	require.True(t, trace.IsAccessDenied(err))
	adminDatabase.SetStaticLabels(map[string]string{"env": "prod", types.OriginLabel: types.OriginDynamic}) // Reset.

	// Dev shouldn't be able to get prod database...
	_, err = devClt.GetDatabase(ctx, adminDatabase.GetName())
	require.True(t, trace.IsAccessDenied(err))

	// ... but can get dev database.
	db, err := devClt.GetDatabase(ctx, devDatabase.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(devDatabase, db,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin can get both databases.
	db, err = adminClt.GetDatabase(ctx, adminDatabase.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(adminDatabase, db,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))
	db, err = adminClt.GetDatabase(ctx, devDatabase.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(devDatabase, db,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// When listing databases, dev should only see one.
	dbs, err := devClt.GetDatabases(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Database{devDatabase}, dbs,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin should see both.
	dbs, err = adminClt.GetDatabases(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Database{adminDatabase, devDatabase}, dbs,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Dev shouldn't be able to delete dev database...
	err = devClt.DeleteDatabase(ctx, adminDatabase.GetName())
	require.True(t, trace.IsAccessDenied(err))

	// ... but can delete dev database.
	err = devClt.DeleteDatabase(ctx, devDatabase.GetName())
	require.NoError(t, err)

	// Admin should be able to delete admin database.
	err = adminClt.DeleteDatabase(ctx, adminDatabase.GetName())
	require.NoError(t, err)

	// Create both databases again to test "delete all" functionality.
	require.NoError(t, devClt.CreateDatabase(ctx, devDatabase))
	require.NoError(t, adminClt.CreateDatabase(ctx, adminDatabase))

	// Dev should only be able to delete dev database.
	err = devClt.DeleteAllDatabases(ctx)
	require.NoError(t, err)
	dbs, err = adminClt.GetDatabases(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Database{adminDatabase}, dbs,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin should be able to delete all.
	err = adminClt.DeleteAllDatabases(ctx)
	require.NoError(t, err)
	dbs, err = adminClt.GetDatabases(ctx)
	require.NoError(t, err)
	require.Len(t, dbs, 0)
}

func TestGetAndList_DatabaseServers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test databases.
	for i := 0; i < 5; i++ {
		name := uuid.New().String()
		db, err := types.NewDatabaseServerV3(types.Metadata{
			Name:   name,
			Labels: map[string]string{"name": name},
		}, types.DatabaseServerSpecV3{
			Protocol: "postgres",
			URI:      "example.com",
			Hostname: "host",
			HostID:   "hostid",
		})
		require.NoError(t, err)

		_, err = srv.Auth().UpsertDatabaseServer(ctx, db)
		require.NoError(t, err)
	}

	testServers, err := srv.Auth().GetDatabaseServers(ctx, defaults.Namespace)
	require.NoError(t, err)

	testResources := make([]types.ResourceWithLabels, len(testServers))
	for i, server := range testServers {
		testResources[i] = server
	}

	// create user, role, and client
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	listRequest := proto.ListResourcesRequest{
		Namespace: defaults.Namespace,
		// Guarantee that the list will all the servers.
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindDatabaseServer,
	}

	// permit user to get the first database
	role.SetDatabaseLabels(types.Allow, types.Labels{"name": {testServers[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err := clt.GetDatabaseServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, 1, len(servers))
	require.Empty(t, cmp.Diff(testServers[0:1], servers))
	resp, err := clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// permit user to get all databases
	role.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetDatabaseServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, len(testServers), len(servers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))

	// Test various filtering.
	baseRequest := proto.ListResourcesRequest{
		Namespace:    defaults.Namespace,
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindDatabaseServer,
	}

	// list only database with label
	withLabels := baseRequest
	withLabels.Labels = map[string]string{"name": testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test search keywords match.
	withSearchKeywords := baseRequest
	withSearchKeywords.SearchKeywords = []string{"name", testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test expression match.
	withExpression := baseRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testServers[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// deny user to get the first database
	role.SetDatabaseLabels(types.Deny, types.Labels{"name": {testServers[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetDatabaseServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, len(testServers[1:]), len(servers))
	require.Empty(t, cmp.Diff(testServers[1:], servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources[1:]))
	require.Empty(t, cmp.Diff(testResources[1:], resp.Resources))

	// deny user to get all databases
	role.SetDatabaseLabels(types.Deny, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetDatabaseServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, 0, len(servers))
	require.Empty(t, cmp.Diff([]types.DatabaseServer{}, servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 0)
	require.Empty(t, cmp.Diff([]types.ResourceWithLabels{}, resp.Resources))
}

// TestGetAndList_ApplicationServers verifies RBAC and filtering is applied when fetching app servers.
func TestGetAndList_ApplicationServers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test app servers.
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("app-%v", i)
		app, err := types.NewAppV3(types.Metadata{
			Name:   name,
			Labels: map[string]string{"name": name},
		},
			types.AppSpecV3{URI: "localhost"})
		require.NoError(t, err)
		server, err := types.NewAppServerV3FromApp(app, "host", "hostid")
		require.NoError(t, err)

		_, err = srv.Auth().UpsertApplicationServer(ctx, server)
		require.NoError(t, err)
	}

	testServers, err := srv.Auth().GetApplicationServers(ctx, defaults.Namespace)
	require.NoError(t, err)

	testResources := make([]types.ResourceWithLabels, len(testServers))
	for i, server := range testServers {
		testResources[i] = server
	}

	listRequest := proto.ListResourcesRequest{
		Namespace: defaults.Namespace,
		// Guarantee that the list will all the servers.
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindAppServer,
	}

	// create user, role, and client
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// permit user to get the first app
	role.SetAppLabels(types.Allow, types.Labels{"name": {testServers[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err := clt.GetApplicationServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, 1, len(servers))
	require.Empty(t, cmp.Diff(testServers[0:1], servers))
	resp, err := clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// permit user to get all apps
	role.SetAppLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetApplicationServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, len(testServers), len(servers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))

	// Test various filtering.
	baseRequest := proto.ListResourcesRequest{
		Namespace:    defaults.Namespace,
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindAppServer,
	}

	// list only application with label
	withLabels := baseRequest
	withLabels.Labels = map[string]string{"name": testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test search keywords match.
	withSearchKeywords := baseRequest
	withSearchKeywords.SearchKeywords = []string{"name", testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test expression match.
	withExpression := baseRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testServers[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// deny user to get the first app
	role.SetAppLabels(types.Deny, types.Labels{"name": {testServers[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetApplicationServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, len(testServers[1:]), len(servers))
	require.Empty(t, cmp.Diff(testServers[1:], servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources[1:]))
	require.Empty(t, cmp.Diff(testResources[1:], resp.Resources))

	// deny user to get all apps
	role.SetAppLabels(types.Deny, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetApplicationServers(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.EqualValues(t, 0, len(servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 0)
	require.Empty(t, cmp.Diff([]types.ResourceWithLabels{}, resp.Resources))
}

// TestApps verifies RBAC is applied to app resources.
func TestApps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Setup a couple of users:
	// - "dev" only has access to apps with labels env=dev
	// - "admin" has access to all apps
	dev, devRole, err := CreateUserAndRole(srv.Auth(), "dev", nil)
	require.NoError(t, err)
	devRole.SetAppLabels(types.Allow, types.Labels{"env": {"dev"}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, devRole))
	devClt, err := srv.NewClient(TestUser(dev.GetName()))
	require.NoError(t, err)

	admin, adminRole, err := CreateUserAndRole(srv.Auth(), "admin", nil)
	require.NoError(t, err)
	adminRole.SetAppLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, adminRole))
	adminClt, err := srv.NewClient(TestUser(admin.GetName()))
	require.NoError(t, err)

	// Prepare a couple of app resources.
	devApp, err := types.NewAppV3(types.Metadata{
		Name:   "dev",
		Labels: map[string]string{"env": "dev", types.OriginLabel: types.OriginDynamic},
	}, types.AppSpecV3{
		URI: "localhost1",
	})
	require.NoError(t, err)
	adminApp, err := types.NewAppV3(types.Metadata{
		Name:   "admin",
		Labels: map[string]string{"env": "prod", types.OriginLabel: types.OriginDynamic},
	}, types.AppSpecV3{
		URI: "localhost2",
	})
	require.NoError(t, err)

	// Dev shouldn't be able to create prod app...
	err = devClt.CreateApp(ctx, adminApp)
	require.True(t, trace.IsAccessDenied(err))

	// ... but can create dev app.
	err = devClt.CreateApp(ctx, devApp)
	require.NoError(t, err)

	// Admin can create prod app.
	err = adminClt.CreateApp(ctx, adminApp)
	require.NoError(t, err)

	// Dev shouldn't be able to update prod app...
	err = devClt.UpdateApp(ctx, adminApp)
	require.True(t, trace.IsAccessDenied(err))

	// ... but can update dev app.
	err = devClt.UpdateApp(ctx, devApp)
	require.NoError(t, err)

	// Dev shouldn't be able to update labels on the prod app.
	adminApp.SetStaticLabels(map[string]string{"env": "dev", types.OriginLabel: types.OriginDynamic})
	err = devClt.UpdateApp(ctx, adminApp)
	require.True(t, trace.IsAccessDenied(err))
	adminApp.SetStaticLabels(map[string]string{"env": "prod", types.OriginLabel: types.OriginDynamic}) // Reset.

	// Dev shouldn't be able to get prod app...
	_, err = devClt.GetApp(ctx, adminApp.GetName())
	require.True(t, trace.IsAccessDenied(err))

	// ... but can get dev app.
	app, err := devClt.GetApp(ctx, devApp.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(devApp, app,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin can get both apps.
	app, err = adminClt.GetApp(ctx, adminApp.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(adminApp, app,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))
	app, err = adminClt.GetApp(ctx, devApp.GetName())
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(devApp, app,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// When listing apps, dev should only see one.
	apps, err := devClt.GetApps(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Application{devApp}, apps,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin should see both.
	apps, err = adminClt.GetApps(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Application{adminApp, devApp}, apps,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Dev shouldn't be able to delete dev app...
	err = devClt.DeleteApp(ctx, adminApp.GetName())
	require.True(t, trace.IsAccessDenied(err))

	// ... but can delete dev app.
	err = devClt.DeleteApp(ctx, devApp.GetName())
	require.NoError(t, err)

	// Admin should be able to delete admin app.
	err = adminClt.DeleteApp(ctx, adminApp.GetName())
	require.NoError(t, err)

	// Create both apps again to test "delete all" functionality.
	require.NoError(t, devClt.CreateApp(ctx, devApp))
	require.NoError(t, adminClt.CreateApp(ctx, adminApp))

	// Dev should only be able to delete dev app.
	err = devClt.DeleteAllApps(ctx)
	require.NoError(t, err)
	apps, err = adminClt.GetApps(ctx)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]types.Application{adminApp}, apps,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
	))

	// Admin should be able to delete all.
	err = adminClt.DeleteAllApps(ctx)
	require.NoError(t, err)
	apps, err = adminClt.GetApps(ctx)
	require.NoError(t, err)
	require.Len(t, apps, 0)
}

// TestReplaceRemoteLocksRBAC verifies that only a remote proxy may replace the
// remote locks associated with its cluster.
func TestReplaceRemoteLocksRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, err := NewTestAuthServer(TestAuthServerConfig{Dir: t.TempDir()})
	require.NoError(t, err)

	user, _, err := CreateUserAndRole(srv.AuthServer, "test-user", []string{})
	require.NoError(t, err)

	targetCluster := "cluster"
	tests := []struct {
		desc     string
		identity TestIdentity
		checkErr func(error) bool
	}{
		{
			desc:     "users may not replace remote locks",
			identity: TestUser(user.GetName()),
			checkErr: trace.IsAccessDenied,
		},
		{
			desc:     "local proxy may not replace remote locks",
			identity: TestBuiltin(types.RoleProxy),
			checkErr: trace.IsAccessDenied,
		},
		{
			desc:     "remote proxy of a non-target cluster may not replace the target's remote locks",
			identity: TestRemoteBuiltin(types.RoleProxy, "non-"+targetCluster),
			checkErr: trace.IsAccessDenied,
		},
		{
			desc:     "remote proxy of the target cluster may replace its remote locks",
			identity: TestRemoteBuiltin(types.RoleProxy, targetCluster),
			checkErr: func(err error) bool { return err == nil },
		},
	}

	lock, err := types.NewLock("test-lock", types.LockSpecV2{Target: types.LockTarget{User: "test-user"}})
	require.NoError(t, err)

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			authContext, err := srv.Authorizer.Authorize(context.WithValue(ctx, ContextUser, test.identity.I))
			require.NoError(t, err)

			s := &ServerWithRoles{
				authServer: srv.AuthServer,
				sessions:   srv.SessionServer,
				alog:       srv.AuditLog,
				context:    *authContext,
			}

			err = s.ReplaceRemoteLocks(ctx, targetCluster, []types.Lock{lock})
			require.True(t, test.checkErr(err), trace.DebugReport(err))
		})
	}
}

// TestIsMFARequiredMFADB tests isMFARequest logic per database protocol where different role matchers are used.
func TestIsMFARequiredMFADB(t *testing.T) {
	const (
		databaseName = "test-database"
		userName     = "test-username"
	)

	type modifyRoleFunc func(role types.Role)
	tests := []struct {
		name               string
		userRoleRequireMFA bool
		checkMFA           require.BoolAssertionFunc
		modifyRoleFunc     modifyRoleFunc
		dbProtocol         string
		req                *proto.IsMFARequiredRequest
	}{
		{
			name:       "RequireSessionMFA on MySQL protocol doesn't match database name",
			dbProtocol: libdefaults.ProtocolMySQL,
			req: &proto.IsMFARequiredRequest{
				Target: &proto.IsMFARequiredRequest_Database{
					Database: &proto.RouteToDatabase{
						ServiceName: databaseName,
						Protocol:    libdefaults.ProtocolMySQL,
						Username:    userName,
						Database:    "example",
					},
				},
			},
			modifyRoleFunc: func(role types.Role) {
				roleOpt := role.GetOptions()
				roleOpt.RequireMFAType = types.RequireMFAType_SESSION
				role.SetOptions(roleOpt)

				role.SetDatabaseUsers(types.Allow, []string{types.Wildcard})
				role.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
				role.SetDatabaseNames(types.Allow, nil)
			},
			checkMFA: require.True,
		},
		{
			name:       "RequireSessionMFA off",
			dbProtocol: libdefaults.ProtocolMySQL,
			req: &proto.IsMFARequiredRequest{
				Target: &proto.IsMFARequiredRequest_Database{
					Database: &proto.RouteToDatabase{
						ServiceName: databaseName,
						Protocol:    libdefaults.ProtocolMySQL,
						Username:    userName,
						Database:    "example",
					},
				},
			},
			modifyRoleFunc: func(role types.Role) {
				roleOpt := role.GetOptions()
				roleOpt.RequireMFAType = types.RequireMFAType_OFF
				role.SetOptions(roleOpt)

				role.SetDatabaseUsers(types.Allow, []string{types.Wildcard})
				role.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
				role.SetDatabaseNames(types.Allow, nil)
			},
			checkMFA: require.False,
		},
		{
			name:       "RequireSessionMFA on Postgres protocol database name doesn't match",
			dbProtocol: libdefaults.ProtocolPostgres,
			req: &proto.IsMFARequiredRequest{
				Target: &proto.IsMFARequiredRequest_Database{
					Database: &proto.RouteToDatabase{
						ServiceName: databaseName,
						Protocol:    libdefaults.ProtocolPostgres,
						Username:    userName,
						Database:    "example",
					},
				},
			},
			modifyRoleFunc: func(role types.Role) {
				roleOpt := role.GetOptions()
				roleOpt.RequireMFAType = types.RequireMFAType_SESSION
				role.SetOptions(roleOpt)

				role.SetDatabaseUsers(types.Allow, []string{types.Wildcard})
				role.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
				role.SetDatabaseNames(types.Allow, nil)
			},
			checkMFA: require.False,
		},
		{
			name:       "RequireSessionMFA on Postgres protocol database name matches",
			dbProtocol: libdefaults.ProtocolPostgres,
			req: &proto.IsMFARequiredRequest{
				Target: &proto.IsMFARequiredRequest_Database{
					Database: &proto.RouteToDatabase{
						ServiceName: databaseName,
						Protocol:    libdefaults.ProtocolPostgres,
						Username:    userName,
						Database:    "example",
					},
				},
			},
			modifyRoleFunc: func(role types.Role) {
				roleOpt := role.GetOptions()
				roleOpt.RequireMFAType = types.RequireMFAType_SESSION
				role.SetOptions(roleOpt)

				role.SetDatabaseUsers(types.Allow, []string{types.Wildcard})
				role.SetDatabaseLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
				role.SetDatabaseNames(types.Allow, []string{"example"})
			},
			checkMFA: require.True,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			srv := newTestTLSServer(t)

			// Enable MFA support.
			authPref, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
				Type:         constants.Local,
				SecondFactor: constants.SecondFactorOptional,
				Webauthn: &types.Webauthn{
					RPID: "teleport",
				},
			})
			require.NoError(t, err)
			err = srv.Auth().SetAuthPreference(ctx, authPref)
			require.NoError(t, err)

			database, err := types.NewDatabaseServerV3(
				types.Metadata{
					Name: databaseName,
					Labels: map[string]string{
						"env": "dev",
					},
				},
				types.DatabaseServerSpecV3{
					Protocol: tc.dbProtocol,
					URI:      "example.com",
					Hostname: "host",
					HostID:   "hostID",
				},
			)
			require.NoError(t, err)

			_, err = srv.Auth().UpsertDatabaseServer(ctx, database)
			require.NoError(t, err)

			user, role, err := CreateUserAndRole(srv.Auth(), userName, []string{"test-role"})
			require.NoError(t, err)

			if tc.modifyRoleFunc != nil {
				tc.modifyRoleFunc(role)
			}
			err = srv.Auth().UpsertRole(ctx, role)
			require.NoError(t, err)

			cl, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			resp, err := cl.IsMFARequired(ctx, tc.req)
			require.NoError(t, err)
			tc.checkMFA(t, resp.GetRequired())
		})
	}
}

// TestKindClusterConfig verifies that types.KindClusterConfig can be used
// as an alternative privilege to provide access to cluster configuration
// resources.
func TestKindClusterConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, err := NewTestAuthServer(TestAuthServerConfig{Dir: t.TempDir()})
	require.NoError(t, err)

	getClusterConfigResources := func(ctx context.Context, user types.User) []error {
		authContext, err := srv.Authorizer.Authorize(context.WithValue(ctx, ContextUser, TestUser(user.GetName()).I))
		require.NoError(t, err, trace.DebugReport(err))
		s := &ServerWithRoles{
			authServer: srv.AuthServer,
			sessions:   srv.SessionServer,
			alog:       srv.AuditLog,
			context:    *authContext,
		}
		_, err1 := s.GetClusterAuditConfig(ctx)
		_, err2 := s.GetClusterNetworkingConfig(ctx)
		_, err3 := s.GetSessionRecordingConfig(ctx)
		return []error{err1, err2, err3}
	}

	t.Run("without KindClusterConfig privilege", func(t *testing.T) {
		user, err := CreateUser(srv.AuthServer, "test-user")
		require.NoError(t, err)
		for _, err := range getClusterConfigResources(ctx, user) {
			require.Error(t, err)
			require.True(t, trace.IsAccessDenied(err))
		}
	})

	t.Run("with KindClusterConfig privilege", func(t *testing.T) {
		role, err := types.NewRoleV3("test-role", types.RoleSpecV5{
			Allow: types.RoleConditions{
				Rules: []types.Rule{
					types.NewRule(types.KindClusterConfig, []string{types.VerbRead}),
				},
			},
		})
		require.NoError(t, err)
		user, err := CreateUser(srv.AuthServer, "test-user", role)
		require.NoError(t, err)
		for _, err := range getClusterConfigResources(ctx, user) {
			require.NoError(t, err)
		}
	})
}

func TestGetAndList_KubeServices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test kube services.
	for i := 0; i < 5; i++ {
		name := uuid.NewString()
		s, err := types.NewServerWithLabels(name, types.KindKubeService, types.ServerSpecV2{
			KubernetesClusters: []*types.KubernetesCluster{
				{Name: name, StaticLabels: map[string]string{"name": name}},
			},
		}, map[string]string{"name": name})
		require.NoError(t, err)

		_, err = srv.Auth().UpsertKubeServiceV2(ctx, s)
		require.NoError(t, err)
	}

	testServers, err := srv.Auth().GetKubeServices(ctx)
	require.NoError(t, err)

	testResources := make([]types.ResourceWithLabels, len(testServers))
	for i, server := range testServers {
		testResources[i] = server
	}

	// create user, role, and client
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	listRequest := proto.ListResourcesRequest{
		Namespace: defaults.Namespace,
		// Guarantee that the list will all the servers.
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindKubeService,
	}

	// permit user to get all kubernetes service
	role.SetKubernetesLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err := clt.GetKubeServices(ctx)
	require.NoError(t, err)
	require.Len(t, testServers, len(testServers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err := clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))

	// Test various filtering.
	baseRequest := proto.ListResourcesRequest{
		Namespace:    defaults.Namespace,
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindKubeService,
	}

	// Test label match.
	withLabels := baseRequest
	withLabels.Labels = map[string]string{"name": testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test search keywords match.
	withSearchKeywords := baseRequest
	withSearchKeywords.SearchKeywords = []string{"name", testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test expression match.
	withExpression := baseRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testServers[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// deny user to get the first kubernetes service
	role.SetKubernetesLabels(types.Deny, types.Labels{"name": {testServers[0].GetName()}})
	testServers[0].SetKubernetesClusters(nil)
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetKubeServices(ctx)
	require.NoError(t, err)
	require.Len(t, testServers, len(testServers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))

	// deny user to get all databases
	role.SetKubernetesLabels(types.Deny, types.Labels{types.Wildcard: {types.Wildcard}})
	for _, testServer := range testServers {
		testServer.SetKubernetesClusters(nil)
	}
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetKubeServices(ctx)
	require.NoError(t, err)
	require.Len(t, testServers, len(testServers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))
}

func TestGetAndList_KubernetesServers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test kube servers.
	for i := 0; i < 5; i++ {
		// insert legacy kube servers
		name := uuid.NewString()
		cluster, err := types.NewKubernetesClusterV3(
			types.Metadata{
				Name: name, Labels: map[string]string{"name": name},
			},
			types.KubernetesClusterSpecV3{},
		)
		require.NoError(t, err)

		kubeServer, err := types.NewKubernetesServerV3(
			types.Metadata{
				Name: name, Labels: map[string]string{"name": name},
			},

			types.KubernetesServerSpecV3{
				HostID:   name,
				Hostname: "test",
				Cluster:  cluster,
			},
		)
		require.NoError(t, err)

		_, err = srv.Auth().UpsertKubernetesServer(ctx, kubeServer)
		require.NoError(t, err)
	}

	testServers, err := srv.Auth().GetKubernetesServers(ctx)
	require.NoError(t, err)
	require.Len(t, testServers, 5)

	testResources := make([]types.ResourceWithLabels, len(testServers))
	for i, server := range testServers {
		testResources[i] = server
	}

	// create user, role, and client
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	listRequest := proto.ListResourcesRequest{
		Namespace: defaults.Namespace,
		// Guarantee that the list will all the servers.
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindKubeServer,
	}

	// permit user to get all kubernetes service
	role.SetKubernetesLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err := clt.GetKubernetesServers(ctx)
	require.NoError(t, err)
	require.Len(t, testServers, len(testServers))
	require.Empty(t, cmp.Diff(testServers, servers))
	resp, err := clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))

	// Test various filtering.
	baseRequest := proto.ListResourcesRequest{
		Namespace:    defaults.Namespace,
		Limit:        int32(len(testServers) + 1),
		ResourceType: types.KindKubeServer,
	}

	// Test label match.
	withLabels := baseRequest
	withLabels.Labels = map[string]string{"name": testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test search keywords match.
	withSearchKeywords := baseRequest
	withSearchKeywords.SearchKeywords = []string{"name", testServers[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test expression match.
	withExpression := baseRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testServers[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// deny user to get the first kubernetes service
	role.SetKubernetesLabels(types.Deny, types.Labels{"name": {testServers[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetKubernetesServers(ctx)
	require.NoError(t, err)
	require.Len(t, servers, len(testServers)-1)
	require.Empty(t, cmp.Diff(testServers[1:], servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources)-1)
	require.Empty(t, cmp.Diff(testResources[1:], resp.Resources))

	// deny user to get all databases
	role.SetKubernetesLabels(types.Deny, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	servers, err = clt.GetKubernetesServers(ctx)
	require.NoError(t, err)
	require.Len(t, servers, 0)
	require.Empty(t, cmp.Diff(testServers[:0], servers))
	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 0)
	require.Empty(t, cmp.Diff(testResources[:0], resp.Resources))
}

func TestListResources_NeedTotalCountFlag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test nodes.
	for i := 0; i < 3; i++ {
		name := uuid.New().String()
		node, err := types.NewServerWithLabels(
			name,
			types.KindNode,
			types.ServerSpecV2{},
			map[string]string{"name": name},
		)
		require.NoError(t, err)

		_, err = srv.Auth().UpsertNode(ctx, node)
		require.NoError(t, err)
	}

	testNodes, err := srv.Auth().GetNodes(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.Len(t, testNodes, 3)

	// create user and client
	user, _, err := CreateUserAndRole(srv.Auth(), "user", nil)
	require.NoError(t, err)
	clt, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)

	// Total returned.
	resp, err := clt.ListResources(ctx, proto.ListResourcesRequest{
		ResourceType:   types.KindNode,
		Limit:          2,
		NeedTotalCount: true,
	})
	require.NoError(t, err)
	require.Len(t, resp.Resources, 2)
	require.NotEmpty(t, resp.NextKey)
	require.Equal(t, len(testNodes), resp.TotalCount)

	// No total.
	resp, err = clt.ListResources(ctx, proto.ListResourcesRequest{
		ResourceType: types.KindNode,
		Limit:        2,
	})
	require.NoError(t, err)
	require.Len(t, resp.Resources, 2)
	require.NotEmpty(t, resp.NextKey)
	require.Empty(t, resp.TotalCount)
}

func TestListResources_SearchAsRoles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test nodes.
	const numTestNodes = 3
	for i := 0; i < numTestNodes; i++ {
		name := fmt.Sprintf("node%d", i)
		node, err := types.NewServerWithLabels(
			name,
			types.KindNode,
			types.ServerSpecV2{},
			map[string]string{"name": name},
		)
		require.NoError(t, err)

		_, err = srv.Auth().UpsertNode(ctx, node)
		require.NoError(t, err)
	}

	testNodes, err := srv.Auth().GetNodes(ctx, defaults.Namespace)
	require.NoError(t, err)
	require.Len(t, testNodes, numTestNodes)

	// create user and client
	user, role, err := CreateUserAndRole(srv.Auth(), "user", []string{"user"})
	require.NoError(t, err)

	// only allow user to see first node
	role.SetNodeLabels(types.Allow, types.Labels{"name": {testNodes[0].GetName()}})

	// create a new role which can see second node
	searchAsRole := services.RoleForUser(user)
	searchAsRole.SetName("test_search_role")
	searchAsRole.SetNodeLabels(types.Allow, types.Labels{"name": {testNodes[1].GetName()}})
	searchAsRole.SetLogins(types.Allow, []string{"user"})
	require.NoError(t, srv.Auth().UpsertRole(ctx, searchAsRole))

	// create a third role which can see the third node
	previewAsRole := services.RoleForUser(user)
	previewAsRole.SetName("test_preview_role")
	previewAsRole.SetNodeLabels(types.Allow, types.Labels{"name": {testNodes[2].GetName()}})
	previewAsRole.SetLogins(types.Allow, []string{"user"})
	require.NoError(t, srv.Auth().UpsertRole(ctx, previewAsRole))

	role.SetSearchAsRoles(types.Allow, []string{searchAsRole.GetName()})
	role.SetPreviewAsRoles(types.Allow, []string{previewAsRole.GetName()})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	clt, err := srv.NewClient(TestUser(user.GetName()))
	require.NoError(t, err)

	for _, tc := range []struct {
		desc                   string
		requestOpt             func(*proto.ListResourcesRequest)
		expectNodes            []string
		expectSearchEventRoles []string
	}{
		{
			desc:        "basic",
			expectNodes: []string{testNodes[0].GetName()},
		},
		{
			desc: "search as roles",
			requestOpt: func(req *proto.ListResourcesRequest) {
				req.UseSearchAsRoles = true
			},
			expectNodes:            []string{testNodes[0].GetName(), testNodes[1].GetName()},
			expectSearchEventRoles: []string{role.GetName(), searchAsRole.GetName()},
		},
		{
			desc: "preview as roles",
			requestOpt: func(req *proto.ListResourcesRequest) {
				req.UsePreviewAsRoles = true
			},
			expectNodes:            []string{testNodes[0].GetName(), testNodes[2].GetName()},
			expectSearchEventRoles: []string{role.GetName(), previewAsRole.GetName()},
		},
		{
			desc: "both",
			requestOpt: func(req *proto.ListResourcesRequest) {
				req.UseSearchAsRoles = true
				req.UsePreviewAsRoles = true
			},
			expectNodes:            []string{testNodes[0].GetName(), testNodes[1].GetName(), testNodes[2].GetName()},
			expectSearchEventRoles: []string{role.GetName(), searchAsRole.GetName(), previewAsRole.GetName()},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			req := proto.ListResourcesRequest{
				ResourceType: types.KindNode,
				Limit:        int32(len(testNodes)),
			}
			if tc.requestOpt != nil {
				tc.requestOpt(&req)
			}
			resp, err := clt.ListResources(ctx, req)
			require.NoError(t, err)
			require.Len(t, resp.Resources, len(tc.expectNodes))
			var gotNodes []string
			for _, node := range resp.Resources {
				gotNodes = append(gotNodes, node.GetName())
			}
			require.ElementsMatch(t, tc.expectNodes, gotNodes)

			if len(tc.expectSearchEventRoles) > 0 {
				require.Eventually(t, func() bool {
					// make sure an audit event is logged for the search
					auditEvents, _, err := srv.AuthServer.AuditLog.SearchEvents(time.Time{}, time.Now(), "", []string{events.AccessRequestResourceSearch}, 10, 0, "")
					require.NoError(t, err)
					if len(auditEvents) == 0 {
						t.Log("no search audit events found")
						return false
					}
					lastEvent := auditEvents[len(auditEvents)-1].(*apievents.AccessRequestResourceSearch)
					diff := cmp.Diff(tc.expectSearchEventRoles, lastEvent.SearchAsRoles)
					if diff == "" {
						// Found the event we're looking for.
						return true
					}
					t.Logf("most recent search event does not have the expected roles, diff: %s", diff)
					return false
				}, 10*time.Second, 250*time.Millisecond, "did not find expected search event")
			}
		})
	}

}

func TestGetAndList_WindowsDesktops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create test desktops.
	for i := 0; i < 5; i++ {
		name := uuid.New().String()
		desktop, err := types.NewWindowsDesktopV3(name, map[string]string{"name": name},
			types.WindowsDesktopSpecV3{Addr: "_", HostID: "_"})
		require.NoError(t, err)
		require.NoError(t, srv.Auth().UpsertWindowsDesktop(ctx, desktop))
	}

	// Test all has been upserted.
	testDesktops, err := srv.Auth().GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
	require.NoError(t, err)
	require.Len(t, testDesktops, 5)

	testResources := types.WindowsDesktops(testDesktops).AsResources()

	// Create user, role, and client.
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// Base request.
	listRequest := proto.ListResourcesRequest{
		ResourceType: types.KindWindowsDesktop,
		Limit:        int32(len(testDesktops) + 1),
	}

	// Permit user to get the first desktop.
	role.SetWindowsDesktopLabels(types.Allow, types.Labels{"name": {testDesktops[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	desktops, err := clt.GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, len(desktops))
	require.Empty(t, cmp.Diff(testDesktops[0:1], desktops))

	resp, err := clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))
	require.Empty(t, resp.NextKey)
	require.Empty(t, resp.TotalCount)

	// Permit user to get all desktops.
	role.SetWindowsDesktopLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))
	desktops, err = clt.GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
	require.NoError(t, err)
	require.EqualValues(t, len(testDesktops), len(desktops))
	require.Empty(t, cmp.Diff(testDesktops, desktops))

	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	require.Empty(t, cmp.Diff(testResources, resp.Resources))
	require.Empty(t, resp.NextKey)
	require.Empty(t, resp.TotalCount)

	// Test sorting is supported.
	withSort := listRequest
	withSort.SortBy = types.SortBy{IsDesc: true, Field: types.ResourceMetadataName}
	resp, err = clt.ListResources(ctx, withSort)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources))
	desktops, err = types.ResourcesWithLabels(resp.Resources).AsWindowsDesktops()
	require.NoError(t, err)
	names, err := types.WindowsDesktops(desktops).GetFieldVals(types.ResourceMetadataName)
	require.NoError(t, err)
	require.IsDecreasing(t, names)

	// Filter by labels.
	withLabels := listRequest
	withLabels.Labels = map[string]string{"name": testDesktops[0].GetName()}
	resp, err = clt.ListResources(ctx, withLabels)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))
	require.Empty(t, resp.NextKey)
	require.Empty(t, resp.TotalCount)

	// Test search keywords match.
	withSearchKeywords := listRequest
	withSearchKeywords.SearchKeywords = []string{"name", testDesktops[0].GetName()}
	resp, err = clt.ListResources(ctx, withSearchKeywords)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Test predicate match.
	withExpression := listRequest
	withExpression.PredicateExpression = fmt.Sprintf(`labels.name == "%s"`, testDesktops[0].GetName())
	resp, err = clt.ListResources(ctx, withExpression)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 1)
	require.Empty(t, cmp.Diff(testResources[0:1], resp.Resources))

	// Deny user to get the first desktop.
	role.SetWindowsDesktopLabels(types.Deny, types.Labels{"name": {testDesktops[0].GetName()}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	desktops, err = clt.GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
	require.NoError(t, err)
	require.EqualValues(t, len(testDesktops[1:]), len(desktops))
	require.Empty(t, cmp.Diff(testDesktops[1:], desktops))

	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, len(testResources[1:]))
	require.Empty(t, cmp.Diff(testResources[1:], resp.Resources))

	// Deny user all desktops.
	role.SetWindowsDesktopLabels(types.Deny, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	desktops, err = clt.GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 0, len(desktops))
	require.Empty(t, cmp.Diff([]types.WindowsDesktop{}, desktops))

	resp, err = clt.ListResources(ctx, listRequest)
	require.NoError(t, err)
	require.Len(t, resp.Resources, 0)
	require.Empty(t, cmp.Diff([]types.ResourceWithLabels{}, resp.Resources))
}

func TestListResources_KindKubernetesCluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, err := NewTestAuthServer(TestAuthServerConfig{Dir: t.TempDir()})
	require.NoError(t, err)

	authContext, err := srv.Authorizer.Authorize(context.WithValue(ctx, ContextUser, TestBuiltin(types.RoleProxy).I))
	require.NoError(t, err)

	s := &ServerWithRoles{
		authServer: srv.AuthServer,
		sessions:   srv.SessionServer,
		alog:       srv.AuditLog,
		context:    *authContext,
	}

	testNames := []string{"a", "b", "c", "d"}

	// Add a kube service with 3 clusters.
	kubeService, err := types.NewServer("bar", types.KindKubeService, types.ServerSpecV2{
		KubernetesClusters: []*types.KubernetesCluster{{Name: "d"}, {Name: "b"}, {Name: "a"}},
	})
	require.NoError(t, err)
	_, err = s.UpsertKubeServiceV2(ctx, kubeService)
	require.NoError(t, err)

	// Add a kube service with 2 clusters.
	// Includes a duplicate cluster name to test deduplicate.
	kubeService, err = types.NewServer("foo", types.KindKubeService, types.ServerSpecV2{
		KubernetesClusters: []*types.KubernetesCluster{{Name: "a"}, {Name: "c"}},
	})
	require.NoError(t, err)
	_, err = s.UpsertKubeServiceV2(ctx, kubeService)
	require.NoError(t, err)

	// Test upsert.
	kubeservices, err := s.GetKubeServices(ctx)
	require.NoError(t, err)
	require.Len(t, kubeservices, 2)

	t.Run("fetch all", func(t *testing.T) {
		t.Parallel()

		res, err := s.ListResources(ctx, proto.ListResourcesRequest{
			ResourceType: types.KindKubernetesCluster,
			Limit:        10,
		})
		require.NoError(t, err)
		require.Len(t, res.Resources, len(testNames))
		require.Empty(t, res.NextKey)
		// There is 2 kube services, but 4 unique clusters.
		require.Equal(t, 4, res.TotalCount)

		clusters, err := types.ResourcesWithLabels(res.Resources).AsKubeClusters()
		require.NoError(t, err)
		names, err := types.KubeClusters(clusters).GetFieldVals(types.ResourceMetadataName)
		require.NoError(t, err)
		require.ElementsMatch(t, names, testNames)
	})

	t.Run("start keys", func(t *testing.T) {
		t.Parallel()

		// First fetch.
		res, err := s.ListResources(ctx, proto.ListResourcesRequest{
			ResourceType: types.KindKubernetesCluster,
			Limit:        1,
		})
		require.NoError(t, err)
		require.Len(t, res.Resources, 1)
		require.Equal(t, kubeservices[0].GetKubernetesClusters()[1].Name, res.NextKey)

		// Second fetch.
		res, err = s.ListResources(ctx, proto.ListResourcesRequest{
			ResourceType: types.KindKubernetesCluster,
			Limit:        1,
			StartKey:     res.NextKey,
		})
		require.NoError(t, err)
		require.Len(t, res.Resources, 1)
		require.Equal(t, kubeservices[0].GetKubernetesClusters()[2].Name, res.NextKey)
	})

	t.Run("fetch with sort and total count", func(t *testing.T) {
		t.Parallel()
		res, err := s.ListResources(ctx, proto.ListResourcesRequest{
			ResourceType: types.KindKubernetesCluster,
			Limit:        10,
			SortBy: types.SortBy{
				IsDesc: true,
				Field:  types.ResourceMetadataName,
			},
			NeedTotalCount: true,
		})
		require.NoError(t, err)
		require.Empty(t, res.NextKey)
		require.Len(t, res.Resources, len(testNames))
		require.Equal(t, res.TotalCount, len(testNames))

		clusters, err := types.ResourcesWithLabels(res.Resources).AsKubeClusters()
		require.NoError(t, err)
		names, err := types.KubeClusters(clusters).GetFieldVals(types.ResourceMetadataName)
		require.NoError(t, err)
		require.IsDecreasing(t, names)
	})
}

func TestDeleteUserAppSessions(t *testing.T) {
	ctx := context.Background()

	srv := newTestTLSServer(t)
	t.Cleanup(func() { srv.Close() })

	// Generates a new user client.
	userClient := func(username string) *Client {
		user, _, err := CreateUserAndRole(srv.Auth(), username, nil)
		require.NoError(t, err)
		identity := TestUser(user.GetName())
		clt, err := srv.NewClient(identity)
		require.NoError(t, err)
		return clt
	}

	// Register users.
	aliceClt := userClient("alice")
	bobClt := userClient("bob")

	// Register multiple applications.
	applications := []struct {
		name       string
		publicAddr string
	}{
		{name: "panel", publicAddr: "panel.example.com"},
		{name: "admin", publicAddr: "admin.example.com"},
		{name: "metrics", publicAddr: "metrics.example.com"},
	}

	// Register and create a session for each application.
	for _, application := range applications {
		// Register an application.
		app, err := types.NewAppV3(types.Metadata{
			Name: application.name,
		}, types.AppSpecV3{
			URI:        "localhost",
			PublicAddr: application.publicAddr,
		})
		require.NoError(t, err)
		server, err := types.NewAppServerV3FromApp(app, "host", uuid.New().String())
		require.NoError(t, err)
		_, err = srv.Auth().UpsertApplicationServer(ctx, server)
		require.NoError(t, err)

		// Create a session for alice.
		_, err = aliceClt.CreateAppSession(ctx, types.CreateAppSessionRequest{
			Username:    "alice",
			PublicAddr:  application.publicAddr,
			ClusterName: "localhost",
		})
		require.NoError(t, err)

		// Create a session for bob.
		_, err = bobClt.CreateAppSession(ctx, types.CreateAppSessionRequest{
			Username:    "bob",
			PublicAddr:  application.publicAddr,
			ClusterName: "localhost",
		})
		require.NoError(t, err)
	}

	// Ensure the correct number of sessions.
	sessions, err := srv.Auth().GetAppSessions(ctx)
	require.NoError(t, err)
	require.Len(t, sessions, 6)

	// Try to delete other user app sessions.
	err = aliceClt.DeleteUserAppSessions(ctx, &proto.DeleteUserAppSessionsRequest{Username: "bob"})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	err = bobClt.DeleteUserAppSessions(ctx, &proto.DeleteUserAppSessionsRequest{Username: "alice"})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	// Delete alice sessions.
	err = aliceClt.DeleteUserAppSessions(ctx, &proto.DeleteUserAppSessionsRequest{Username: "alice"})
	require.NoError(t, err)

	// Check if only bob's sessions are left.
	sessions, err = srv.Auth().GetAppSessions(ctx)
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	for _, session := range sessions {
		require.Equal(t, "bob", session.GetUser())
	}

	// Delete bob sessions.
	err = bobClt.DeleteUserAppSessions(ctx, &proto.DeleteUserAppSessionsRequest{Username: "bob"})
	require.NoError(t, err)

	// No sessions left.
	sessions, err = srv.Auth().GetAppSessions(ctx)
	require.NoError(t, err)
	require.Len(t, sessions, 0)
}

func TestListResources_SortAndDeduplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	// Create user, role, and client.
	username := "user"
	user, role, err := CreateUserAndRole(srv.Auth(), username, nil)
	require.NoError(t, err)
	identity := TestUser(user.GetName())
	clt, err := srv.NewClient(identity)
	require.NoError(t, err)

	// Permit user to get all resources.
	role.SetWindowsDesktopLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	require.NoError(t, srv.Auth().UpsertRole(ctx, role))

	// Define some resource names for testing.
	names := []string{"d", "b", "d", "a", "a", "b"}
	uniqueNames := []string{"a", "b", "d"}

	tests := []struct {
		name            string
		kind            string
		insertResources func()
		wantNames       []string
	}{
		{
			name: "KindDatabaseServer",
			kind: types.KindDatabaseServer,
			insertResources: func() {
				for i := 0; i < len(names); i++ {
					db, err := types.NewDatabaseServerV3(types.Metadata{
						Name: fmt.Sprintf("name-%v", i),
					}, types.DatabaseServerSpecV3{
						HostID:   "_",
						Hostname: "_",
						Database: &types.DatabaseV3{
							Metadata: types.Metadata{
								Name: names[i],
							},
							Spec: types.DatabaseSpecV3{
								Protocol: "_",
								URI:      "_",
							},
						},
					})
					require.NoError(t, err)
					_, err = srv.Auth().UpsertDatabaseServer(ctx, db)
					require.NoError(t, err)
				}
			},
		},
		{
			name: "KindAppServer",
			kind: types.KindAppServer,
			insertResources: func() {
				for i := 0; i < len(names); i++ {
					server, err := types.NewAppServerV3(types.Metadata{
						Name: fmt.Sprintf("name-%v", i),
					}, types.AppServerSpecV3{
						HostID: "_",
						App:    &types.AppV3{Metadata: types.Metadata{Name: names[i]}, Spec: types.AppSpecV3{URI: "_"}},
					})
					require.NoError(t, err)
					_, err = srv.Auth().UpsertApplicationServer(ctx, server)
					require.NoError(t, err)
				}
			},
		},
		{
			name: "KindWindowsDesktop",
			kind: types.KindWindowsDesktop,
			insertResources: func() {
				for i := 0; i < len(names); i++ {
					desktop, err := types.NewWindowsDesktopV3(names[i], nil, types.WindowsDesktopSpecV3{
						Addr:   "_",
						HostID: fmt.Sprintf("name-%v", i),
					})
					require.NoError(t, err)
					require.NoError(t, srv.Auth().UpsertWindowsDesktop(ctx, desktop))
				}
			},
		},
		{
			name: "KindKubernetesCluster",
			kind: types.KindKubernetesCluster,
			insertResources: func() {
				for i := 0; i < len(names); i++ {
					server, err := types.NewServer(fmt.Sprintf("name-%v", i), types.KindKubeService, types.ServerSpecV2{
						KubernetesClusters: []*types.KubernetesCluster{
							// Test dedup inside this list as well as from each service.
							{Name: names[i]},
							{Name: names[i]},
						},
					})
					require.NoError(t, err)
					_, err = srv.Auth().UpsertKubeServiceV2(ctx, server)
					require.NoError(t, err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.insertResources()

			// Fetch all resources
			fetchedResources := make([]types.ResourceWithLabels, 0, len(uniqueNames))
			resp, err := clt.ListResources(ctx, proto.ListResourcesRequest{
				ResourceType:   tc.kind,
				NeedTotalCount: true,
				Limit:          2,
				SortBy:         types.SortBy{Field: types.ResourceMetadataName, IsDesc: true},
			})
			require.NoError(t, err)
			require.Len(t, resp.Resources, 2)
			require.Equal(t, len(uniqueNames), resp.TotalCount)
			fetchedResources = append(fetchedResources, resp.Resources...)

			resp, err = clt.ListResources(ctx, proto.ListResourcesRequest{
				ResourceType:   tc.kind,
				NeedTotalCount: true,
				StartKey:       resp.NextKey,
				Limit:          2,
				SortBy:         types.SortBy{Field: types.ResourceMetadataName, IsDesc: true},
			})
			require.NoError(t, err)
			require.Len(t, resp.Resources, 1)
			require.Equal(t, len(uniqueNames), resp.TotalCount)
			fetchedResources = append(fetchedResources, resp.Resources...)

			r := types.ResourcesWithLabels(fetchedResources)
			var extractedErr error
			var extractedNames []string

			switch tc.kind {
			case types.KindDatabaseServer:
				s, err := r.AsDatabaseServers()
				require.NoError(t, err)
				extractedNames, extractedErr = types.DatabaseServers(s).GetFieldVals(types.ResourceMetadataName)

			case types.KindAppServer:
				s, err := r.AsAppServers()
				require.NoError(t, err)
				extractedNames, extractedErr = types.AppServers(s).GetFieldVals(types.ResourceMetadataName)

			case types.KindWindowsDesktop:
				s, err := r.AsWindowsDesktops()
				require.NoError(t, err)
				extractedNames, extractedErr = types.WindowsDesktops(s).GetFieldVals(types.ResourceMetadataName)

			default:
				s, err := r.AsKubeClusters()
				require.NoError(t, err)
				require.Len(t, s, 3)
				extractedNames, extractedErr = types.KubeClusters(s).GetFieldVals(types.ResourceMetadataName)
			}

			require.NoError(t, extractedErr)
			require.ElementsMatch(t, uniqueNames, extractedNames)
			require.IsDecreasing(t, extractedNames)
		})
	}
}

func TestListResources_WithRoles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)
	const nodePerPool = 3

	// inserts a pool nodes with different labels
	insertNodes := func(ctx context.Context, t *testing.T, srv *Server, nodeCount int, labels map[string]string) {
		for i := 0; i < nodeCount; i++ {
			name := uuid.NewString()
			addr := fmt.Sprintf("node-%s.example.com", name)

			node := &types.ServerV2{
				Kind:    types.KindNode,
				Version: types.V2,
				Metadata: types.Metadata{
					Name:      name,
					Namespace: defaults.Namespace,
					Labels:    labels,
				},
				Spec: types.ServerSpecV2{
					Addr:       addr,
					PublicAddr: addr,
				},
			}

			_, err := srv.UpsertNode(ctx, node)
			require.NoError(t, err)
		}
	}

	// creates roles that deny the given labels
	createRole := func(ctx context.Context, t *testing.T, srv *Server, name string, labels map[string]apiutils.Strings) {
		role, err := types.NewRoleV3(name, types.RoleSpecV5{
			Allow: types.RoleConditions{
				NodeLabels: types.Labels{
					"*": []string{types.Wildcard},
				},
			},
			Deny: types.RoleConditions{
				NodeLabels: labels,
			},
		})
		require.NoError(t, err)
		err = srv.UpsertRole(ctx, role)
		require.NoError(t, err)
	}

	// the pool from which nodes and roles are created from
	pool := map[string]struct {
		denied map[string]apiutils.Strings
		labels map[string]string
	}{
		"other": {
			denied: nil,
			labels: map[string]string{
				"other": "123",
			},
		},
		"a": {
			denied: map[string]apiutils.Strings{
				"pool": {"a"},
			},
			labels: map[string]string{
				"pool": "a",
			},
		},
		"b": {
			denied: map[string]apiutils.Strings{
				"pool": {"b"},
			},
			labels: map[string]string{
				"pool": "b",
			},
		},
		"c": {
			denied: map[string]apiutils.Strings{
				"pool": {"c"},
			},
			labels: map[string]string{
				"pool": "c",
			},
		},
		"d": {
			denied: map[string]apiutils.Strings{
				"pool": {"d"},
			},
			labels: map[string]string{
				"pool": "d",
			},
		},
		"e": {
			denied: map[string]apiutils.Strings{
				"pool": {"e"},
			},
			labels: map[string]string{
				"pool": "e",
			},
		},
	}

	// create the nodes and role
	for name, data := range pool {
		insertNodes(ctx, t, srv.Auth(), nodePerPool, data.labels)
		createRole(ctx, t, srv.Auth(), name, data.denied)
	}

	nodeCount := len(pool) * nodePerPool

	cases := []struct {
		name     string
		roles    []string
		expected int
	}{
		{
			name:     "all allowed",
			roles:    []string{"other"},
			expected: nodeCount,
		},
		{
			name:     "role a",
			roles:    []string{"a"},
			expected: nodeCount - nodePerPool,
		},
		{
			name:     "role a,b",
			roles:    []string{"a", "b"},
			expected: nodeCount - (2 * nodePerPool),
		},
		{
			name:     "role a,b,c",
			roles:    []string{"a", "b", "c"},
			expected: nodeCount - (3 * nodePerPool),
		},
		{
			name:     "role a,b,c,d",
			roles:    []string{"a", "b", "c", "d"},
			expected: nodeCount - (4 * nodePerPool),
		},
		{
			name:     "role a,b,c,d,e",
			roles:    []string{"a", "b", "c", "d", "e"},
			expected: nodeCount - (5 * nodePerPool),
		},
	}

	// ensure that a user can see the correct number of resources for their role(s)
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			user, err := types.NewUser(uuid.NewString())
			require.NoError(t, err)

			for _, role := range tt.roles {
				user.AddRole(role)
			}

			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			for _, needTotal := range []bool{true, false} {
				total := needTotal
				t.Run(fmt.Sprintf("needTotal=%t", total), func(t *testing.T) {
					t.Parallel()

					clt, err := srv.NewClient(TestUser(user.GetName()))
					require.NoError(t, err)

					var resp *types.ListResourcesResponse
					var nodes []types.ResourceWithLabels
					for {
						key := ""
						if resp != nil {
							key = resp.NextKey
						}

						resp, err = clt.ListResources(ctx, proto.ListResourcesRequest{
							ResourceType:   types.KindNode,
							StartKey:       key,
							Limit:          nodePerPool,
							NeedTotalCount: total,
						})
						require.NoError(t, err)

						nodes = append(nodes, resp.Resources...)

						if resp.NextKey == "" {
							break
						}
					}

					require.Len(t, nodes, tt.expected)
				})
			}
		})
	}
}

// TestGenerateHostCert attempts to generate host certificates using various
// RBAC rules
func TestGenerateHostCert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := newTestTLSServer(t)

	clusterName := srv.ClusterName()

	_, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	noError := func(err error) bool {
		return err == nil
	}

	for _, test := range []struct {
		desc       string
		principals []string
		skipRule   bool
		where      string
		deny       bool
		denyWhere  string
		expect     func(error) bool
	}{
		{
			desc:       "disallowed",
			skipRule:   true,
			principals: []string{"foo.example.com"},
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "denied",
			deny:       true,
			principals: []string{"foo.example.com"},
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "allowed",
			principals: []string{"foo.example.com"},
			expect:     noError,
		},
		{
			desc:       "allowed-subset",
			principals: []string{"foo.example.com"},
			where:      `is_subset(host_cert.principals, "foo.example.com", "bar.example.com")`,
			expect:     noError,
		},
		{
			desc:       "disallowed-subset",
			principals: []string{"baz.example.com"},
			where:      `is_subset(host_cert.principals, "foo.example.com", "bar.example.com")`,
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "allowed-all-equal",
			principals: []string{"foo.example.com"},
			where:      `all_equal(host_cert.principals, "foo.example.com")`,
			expect:     noError,
		},
		{
			desc:       "disallowed-all-equal",
			principals: []string{"bar.example.com"},
			where:      `all_equal(host_cert.principals, "foo.example.com")`,
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "allowed-all-end-with",
			principals: []string{"node.foo.example.com"},
			where:      `all_end_with(host_cert.principals, ".foo.example.com")`,
			expect:     noError,
		},
		{
			desc:       "disallowed-all-end-with",
			principals: []string{"node.bar.example.com"},
			where:      `all_end_with(host_cert.principals, ".foo.example.com")`,
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "allowed-complex",
			principals: []string{"foo.example.com"},
			where:      `all_end_with(host_cert.principals, ".example.com")`,
			denyWhere:  `is_subset(host_cert.principals, "bar.example.com", "baz.example.com")`,
			expect:     noError,
		},
		{
			desc:       "disallowed-complex",
			principals: []string{"bar.example.com"},
			where:      `all_end_with(host_cert.principals, ".example.com")`,
			denyWhere:  `is_subset(host_cert.principals, "bar.example.com", "baz.example.com")`,
			expect:     trace.IsAccessDenied,
		},
		{
			desc:       "allowed-multiple",
			principals: []string{"bar.example.com", "foo.example.com"},
			where:      `is_subset(host_cert.principals, "foo.example.com", "bar.example.com")`,
			expect:     noError,
		},
		{
			desc:       "disallowed-multiple",
			principals: []string{"foo.example.com", "bar.example.com", "baz.example.com"},
			where:      `is_subset(host_cert.principals, "foo.example.com", "bar.example.com")`,
			expect:     trace.IsAccessDenied,
		},
	} {
		t.Run(test.desc, func(t *testing.T) {
			rules := []types.Rule{}
			if !test.skipRule {
				rules = append(rules, types.Rule{
					Resources: []string{types.KindHostCert},
					Verbs:     []string{types.VerbCreate},
					Where:     test.where,
				})
			}

			denyRules := []types.Rule{}
			if test.deny || test.denyWhere != "" {
				denyRules = append(denyRules, types.Rule{
					Resources: []string{types.KindHostCert},
					Verbs:     []string{types.VerbCreate},
					Where:     test.denyWhere,
				})
			}

			role, err := CreateRole(ctx, srv.Auth(), test.desc, types.RoleSpecV5{
				Allow: types.RoleConditions{Rules: rules},
				Deny:  types.RoleConditions{Rules: denyRules},
			})
			require.NoError(t, err)

			user, err := CreateUser(srv.Auth(), test.desc, role)
			require.NoError(t, err)

			client, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			_, err = client.GenerateHostCert(ctx, pub, "", "", test.principals, clusterName, types.RoleNode, 0)
			require.True(t, test.expect(err))
		})
	}
}

// TestLocalServiceRolesHavePermissionsForUploaderService verifies that all of Teleport's
// builtin roles have permissions to execute the calls required by the uploader service.
// This is because only one uploader service runs per Teleport process, and it will use
// the first available identity.
func TestLocalServiceRolesHavePermissionsForUploaderService(t *testing.T) {
	srv, err := NewTestAuthServer(TestAuthServerConfig{Dir: t.TempDir()})
	require.NoError(t, err)

	for _, role := range types.LocalServiceMappings() {
		if role == types.RoleAuth {
			continue
		}
		t.Run(role.String(), func(t *testing.T) {
			ctx := context.Background()

			identity := TestBuiltin(role)
			authContext, err := srv.Authorizer.Authorize(context.WithValue(ctx, ContextUser, identity.I))
			require.NoError(t, err)

			s := &ServerWithRoles{
				authServer: srv.AuthServer,
				alog:       srv.AuditLog,
				context:    *authContext,
			}

			t.Run("GetSessionTracker", func(t *testing.T) {
				sid := session.ID("foo/" + role.String())
				tracker, err := s.CreateSessionTracker(ctx, &types.SessionTrackerV1{
					ResourceHeader: types.ResourceHeader{
						Metadata: types.Metadata{
							Name: sid.String(),
						},
					},
					Spec: types.SessionTrackerSpecV1{
						SessionID: sid.String(),
					},
				})
				require.NoError(t, err)

				_, err = s.GetSessionTracker(ctx, tracker.GetSessionID())
				require.NoError(t, err)
			})

			t.Run("EmitAuditEvent", func(t *testing.T) {
				err := s.EmitAuditEvent(ctx, &apievents.UserLogin{
					Metadata: apievents.Metadata{
						Type: events.UserLoginEvent,
						Code: events.UserLocalLoginFailureCode,
					},
					Method: events.LoginMethodClientCert,
					Status: apievents.Status{Success: true},
				})
				require.NoError(t, err)
			})

			t.Run("StreamSessionEvents", func(t *testing.T) {
				// swap out the audit log with a discard log because we don't care if
				// the streaming actually succeeds, we just want to make sure RBAC checks
				// pass and allow us to enter the audit log code
				originalLog := s.alog
				t.Cleanup(func() { s.alog = originalLog })
				s.alog = events.NewDiscardAuditLog()

				eventC, errC := s.StreamSessionEvents(ctx, "foo", 0)
				select {
				case err := <-errC:
					require.NoError(t, err)
				default:
					// drain eventC to prevent goroutine leak
					for range eventC {
					}
				}
			})

			t.Run("CreateAuditStream", func(t *testing.T) {
				stream, err := s.CreateAuditStream(ctx, session.ID("streamer"))
				require.NoError(t, err)
				require.NoError(t, stream.Close(ctx))
			})

			t.Run("ResumeAuditStream", func(t *testing.T) {
				stream, err := s.ResumeAuditStream(ctx, session.ID("streamer"), "upload")
				require.NoError(t, err)
				require.NoError(t, stream.Close(ctx))
			})
		})
	}
}

type getActiveSessionsTestCase struct {
	name      string
	tracker   types.SessionTracker
	role      types.Role
	hasAccess bool
}

func TestGetActiveSessionTrackers(t *testing.T) {
	t.Parallel()

	testCases := []getActiveSessionsTestCase{func() getActiveSessionsTestCase {
		tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
			SessionID: "1",
			Kind:      string(types.SSHSessionKind),
		})
		require.NoError(t, err)

		role, err := types.NewRole("foo", types.RoleSpecV5{
			Allow: types.RoleConditions{
				Rules: []types.Rule{{
					Resources: []string{types.KindSessionTracker},
					Verbs:     []string{types.VerbList, types.VerbRead},
				}},
			},
		})
		require.NoError(t, err)

		return getActiveSessionsTestCase{"with access simple", tracker, role, true}
	}(), func() getActiveSessionsTestCase {
		tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
			SessionID: "1",
			Kind:      string(types.SSHSessionKind),
		})
		require.NoError(t, err)

		role, err := types.NewRole("foo", types.RoleSpecV5{})
		require.NoError(t, err)

		return getActiveSessionsTestCase{"with no access rule", tracker, role, false}
	}(), func() getActiveSessionsTestCase {
		tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
			SessionID: "1",
			Kind:      string(types.KubernetesSessionKind),
		})
		require.NoError(t, err)

		role, err := types.NewRole("foo", types.RoleSpecV5{
			Allow: types.RoleConditions{
				Rules: []types.Rule{{
					Resources: []string{types.KindSessionTracker},
					Verbs:     []string{types.VerbList, types.VerbRead},
					Where:     "equals(session_tracker.session_id, \"1\")",
				}},
			},
		})
		require.NoError(t, err)

		return getActiveSessionsTestCase{"access with match expression", tracker, role, true}
	}(), func() getActiveSessionsTestCase {
		tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
			SessionID: "2",
			Kind:      string(types.KubernetesSessionKind),
		})
		require.NoError(t, err)

		role, err := types.NewRole("foo", types.RoleSpecV5{
			Allow: types.RoleConditions{
				Rules: []types.Rule{{
					Resources: []string{types.KindSessionTracker},
					Verbs:     []string{types.VerbList, types.VerbRead},
					Where:     "equals(session_tracker.session_id, \"1\")",
				}},
			},
		})
		require.NoError(t, err)

		return getActiveSessionsTestCase{"no access with match expression", tracker, role, false}
	}()}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			srv := newTestTLSServer(t)
			err := srv.Auth().CreateRole(ctx, testCase.role)
			require.NoError(t, err)

			_, err = srv.Auth().CreateSessionTracker(ctx, testCase.tracker)
			require.NoError(t, err)

			user, err := types.NewUser(uuid.NewString())
			require.NoError(t, err)

			user.AddRole(testCase.role.GetName())
			err = srv.Auth().UpsertUser(user)
			require.NoError(t, err)

			clt, err := srv.NewClient(TestUser(user.GetName()))
			require.NoError(t, err)

			found, err := clt.GetActiveSessionTrackers(ctx)
			require.NoError(t, err)
			require.Equal(t, testCase.hasAccess, len(found) != 0)
		})
	}
}
