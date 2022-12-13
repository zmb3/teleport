/*
Copyright 2017-2018 Gravitational, Inc.

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

package native

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/auth/test"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

type nativeContext struct {
	suite *test.AuthSuite
}

func setupNativeContext(ctx context.Context, t *testing.T) *nativeContext {
	var tt nativeContext

	clock := clockwork.NewFakeClockAt(time.Date(2016, 9, 8, 7, 6, 5, 0, time.UTC))

	tt.suite = &test.AuthSuite{
		A:      New(context.Background(), SetClock(clock)),
		Keygen: GenerateKeyPair,
		Clock:  clock,
	}

	return &tt
}

// TestPrecomputeMode verifies that package enters precompute mode when
// PrecomputeKeys is called.
func TestPrecomputeMode(t *testing.T) {
	t.Parallel()

	PrecomputeKeys()

	select {
	case <-precomputedKeys:
	case <-time.After(time.Second * 10):
		t.Fatal("Key precompute routine failed to start.")
	}
}

func TestGenerateKeypairEmptyPass(t *testing.T) {
	t.Parallel()

	tt := setupNativeContext(context.Background(), t)
	tt.suite.GenerateKeypairEmptyPass(t)
}

func TestGenerateHostCert(t *testing.T) {
	t.Parallel()

	tt := setupNativeContext(context.Background(), t)
	tt.suite.GenerateHostCert(t)
}

func TestGenerateUserCert(t *testing.T) {
	t.Parallel()

	tt := setupNativeContext(context.Background(), t)
	tt.suite.GenerateUserCert(t)
}

// TestBuildPrincipals makes sure that the list of principals for a host
// certificate is correctly built.
//   - If the node has role admin, then only the host ID should be listed
//     in the principals field.
//   - If only a host ID is provided, don't include a empty node name
//     this is for backward compatibility.
//   - If both host ID and node name are given, then both should be included
//     on the certificate.
//   - If the host ID and node name are the same, only list one.
func TestBuildPrincipals(t *testing.T) {
	t.Parallel()

	tt := setupNativeContext(context.Background(), t)

	caPrivateKey, _, err := GenerateKeyPair()
	require.NoError(t, err)

	caSigner, err := ssh.ParsePrivateKey(caPrivateKey)
	require.NoError(t, err)

	_, hostPublicKey, err := GenerateKeyPair()
	require.NoError(t, err)

	tests := []struct {
		desc               string
		inHostID           string
		inNodeName         string
		inClusterName      string
		inRole             types.SystemRole
		outValidPrincipals []string
	}{
		{
			desc:               "admin role",
			inHostID:           "00000000-0000-0000-0000-000000000000",
			inNodeName:         "auth",
			inClusterName:      "example.com",
			inRole:             types.RoleAdmin,
			outValidPrincipals: []string{"00000000-0000-0000-0000-000000000000"},
		},
		{
			desc:          "backward compatibility",
			inHostID:      "11111111-1111-1111-1111-111111111111",
			inNodeName:    "",
			inClusterName: "example.com",
			inRole:        types.RoleNode,
			outValidPrincipals: []string{
				"11111111-1111-1111-1111-111111111111.example.com",
				"11111111-1111-1111-1111-111111111111",
				string(teleport.PrincipalLocalhost),
				string(teleport.PrincipalLoopbackV4),
				string(teleport.PrincipalLoopbackV6),
			},
		},
		{
			desc:          "dual principals",
			inHostID:      "22222222-2222-2222-2222-222222222222",
			inNodeName:    "proxy",
			inClusterName: "example.com",
			inRole:        types.RoleProxy,
			outValidPrincipals: []string{
				"22222222-2222-2222-2222-222222222222.example.com",
				"22222222-2222-2222-2222-222222222222",
				"proxy.example.com",
				"proxy",
				string(teleport.PrincipalLocalhost),
				string(teleport.PrincipalLoopbackV4),
				string(teleport.PrincipalLoopbackV6),
			},
		},
		{
			desc:          "deduplicate principals",
			inHostID:      "33333333-3333-3333-3333-333333333333",
			inNodeName:    "33333333-3333-3333-3333-333333333333",
			inClusterName: "example.com",
			inRole:        types.RoleProxy,
			outValidPrincipals: []string{
				"33333333-3333-3333-3333-333333333333.example.com",
				"33333333-3333-3333-3333-333333333333",
				string(teleport.PrincipalLocalhost),
				string(teleport.PrincipalLoopbackV4),
				string(teleport.PrincipalLoopbackV6),
			},
		},
	}

	// run tests
	for _, tc := range tests {
		t.Logf("Running test case: %q", tc.desc)
		hostCertificateBytes, err := tt.suite.A.GenerateHostCert(
			services.HostCertParams{
				CASigner:      caSigner,
				PublicHostKey: hostPublicKey,
				HostID:        tc.inHostID,
				NodeName:      tc.inNodeName,
				ClusterName:   tc.inClusterName,
				Role:          tc.inRole,
				TTL:           time.Hour,
			})
		require.NoError(t, err)

		hostCertificate, err := sshutils.ParseCertificate(hostCertificateBytes)
		require.NoError(t, err)

		require.Empty(t, cmp.Diff(hostCertificate.ValidPrincipals, tc.outValidPrincipals))
	}
}

// TestUserCertCompatibility makes sure the compatibility flag can be used to
// add to remove roles from certificate extensions.
func TestUserCertCompatibility(t *testing.T) {
	t.Parallel()

	tt := setupNativeContext(context.Background(), t)

	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	caSigner, err := ssh.ParsePrivateKey(priv)
	require.NoError(t, err)

	tests := []struct {
		inCompatibility string
		outHasRoles     bool
	}{
		// 0 - standard, has roles
		{
			constants.CertificateFormatStandard,
			true,
		},
		// 1 - oldssh, no roles
		{
			teleport.CertificateFormatOldSSH,
			false,
		},
	}

	// run tests
	for i, tc := range tests {
		comment := fmt.Sprintf("Test %v", i)

		userCertificateBytes, err := tt.suite.A.GenerateUserCert(services.UserCertParams{
			CASigner:      caSigner,
			PublicUserKey: pub,
			Username:      "user",
			AllowedLogins: []string{"centos", "root"},
			TTL:           time.Hour,
			Roles:         []string{"foo"},
			CertificateExtensions: []*types.CertExtension{{
				Type:  types.CertExtensionType_SSH,
				Mode:  types.CertExtensionMode_EXTENSION,
				Name:  "login@github.com",
				Value: "hello",
			},
			},
			CertificateFormat:     tc.inCompatibility,
			PermitAgentForwarding: true,
			PermitPortForwarding:  true,
		})
		require.NoError(t, err, comment)

		userCertificate, err := sshutils.ParseCertificate(userCertificateBytes)
		require.NoError(t, err, comment)

		// Check if we added the roles extension.
		_, ok := userCertificate.Extensions[teleport.CertExtensionTeleportRoles]
		require.Equal(t, ok, tc.outHasRoles, comment)

		// Check if users custom extension was added.
		extVal := userCertificate.Extensions["login@github.com"]
		require.Equal(t, extVal, "hello")
	}
}

// TestGenerateRSAPKSC1Keypair tests that GeneratePrivateKey generates
// a valid PKCS1 rsa key.
func TestGeneratePKSC1RSAKey(t *testing.T) {
	t.Parallel()

	priv, err := GeneratePrivateKey()
	require.NoError(t, err)

	block, rest := pem.Decode(priv.PrivateKeyPEM())
	require.NoError(t, err)
	require.Empty(t, rest)

	_, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	require.NoError(t, err)
}
