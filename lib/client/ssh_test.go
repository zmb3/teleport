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

package client_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gravitational/teleport/api/breaker"
	"github.com/gravitational/teleport/api/client/proto"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	apiutils "github.com/gravitational/teleport/api/utils"
	wanlib "github.com/gravitational/teleport/lib/auth/webauthn"
	wancli "github.com/gravitational/teleport/lib/auth/webauthncli"
	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/utils/prompt"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
)

func TestSSHOnMultipleNodesWithPerSessionMFA(t *testing.T) {
	silenceLogger(t)

	clock := clockwork.NewFakeClockAt(time.Now())
	sa := newStandaloneTeleport(t, clock)

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	node1 := makeTestSSHNode(t, utils.MustParseAddr(sa.AuthAddr), sa.StaticToken, withSSHLabel("env", "stage"))
	node2 := makeTestSSHNode(t, utils.MustParseAddr(sa.AuthAddr), sa.StaticToken, withSSHLabel("env", "stage"))
	node3 := makeTestSSHNode(t, utils.MustParseAddr(sa.AuthAddr), sa.StaticToken, withSSHLabel("env", "dev"))
	hasNodes := func(hostIDs ...string) func() bool {
		return func() bool {
			nodes, err := sa.Auth.GetAuthServer().GetNodes(ctx, apidefaults.Namespace)
			require.NoError(t, err)
			foundCount := 0
			for _, node := range nodes {
				if apiutils.SliceContainsStr(hostIDs, node.GetName()) {
					foundCount++
				}
			}
			return foundCount == len(hostIDs)
		}
	}
	// wait for auth to see nodes
	require.Eventually(t, hasNodes(node1.Config.HostUUID, node2.Config.HostUUID, node3.Config.HostUUID),
		10*time.Second, 100*time.Millisecond, "nodes never showed up")

	// Prepare client config, it won't change throughout the test.
	cfg := client.MakeDefaultConfig()
	cfg.Username = sa.Username
	cfg.HostLogin = sa.Username

	cfg.AddKeysToAgent = client.AddKeysToAgentNo
	// Replace "127.0.0.1" with "localhost". The proxy address becomes the origin
	// for Webauthn requests, and Webauthn doesn't take IP addresses.
	cfg.WebProxyAddr = strings.Replace(sa.ProxyWebAddr, "127.0.0.1", "localhost", 1 /* n */)
	cfg.KeysDir = t.TempDir()
	cfg.InsecureSkipVerify = true
	cfg.Stdin = &bytes.Buffer{}

	tc, err := client.NewClient(cfg)
	require.NoError(t, err)

	oldStdin, oldWebauthn := prompt.Stdin(), *client.PromptWebauthn
	t.Cleanup(func() {
		prompt.SetStdin(oldStdin)
		*client.PromptWebauthn = oldWebauthn
	})

	password := sa.Password
	device := sa.Device

	inputReader := prompt.NewFakeReader().
		AddString(password).
		AddReply(func(ctx context.Context) (string, error) {
			panic("this should not be called")
		})

	solveWebauthn := func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
		car, err := device.SignAssertion(origin, assertion)
		if err != nil {
			return nil, err
		}
		return &proto.MFAAuthenticateResponse{
			Response: &proto.MFAAuthenticateResponse_Webauthn{
				Webauthn: wanlib.CredentialAssertionResponseToProto(car),
			},
		}, nil
	}

	prompt.SetStdin(inputReader)
	*client.PromptWebauthn = func(
		ctx context.Context,
		origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt, _ *wancli.LoginOpts,
	) (*proto.MFAAuthenticateResponse, string, error) {
		resp, err := solveWebauthn(ctx, origin, assertion, prompt)
		return resp, "", err
	}

	key, err := tc.Login(ctx)
	require.NoError(t, err)

	fmt.Println(111, key.Username, key.ClusterName, key.TrustedCA[0].ClusterName, len(key.TrustedCA))

	err = tc.ActivateKey(ctx, key)
	require.NoError(t, err)

	err = tc.SSH(ctx, []string{"ls", "/"}, false)
	require.NoError(t, err)

}

func makeTestSSHNode(t *testing.T, authAddr *utils.NetAddr, token string, opts ...testServerOptFunc) (node *service.TeleportProcess) {
	var options testServersOpts
	for _, opt := range opts {
		opt(&options)
	}

	var err error

	// Set up a test ssh service.
	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.Hostname = "node"
	cfg.DataDir = t.TempDir()

	cfg.AuthServers = []utils.NetAddr{*authAddr}
	cfg.SetToken(token)
	cfg.Auth.Enabled = false
	cfg.Proxy.Enabled = false
	cfg.SSH.Enabled = true
	cfg.SSH.Addr = *utils.MustParseAddr("127.0.0.1:0")
	cfg.SSH.PublicAddrs = []utils.NetAddr{cfg.SSH.Addr}
	cfg.SSH.DisableCreateHostUser = true
	cfg.Log = utils.NewLoggerForTests()

	for _, fn := range options.configFuncs {
		fn(cfg)
	}

	node, err = service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, node.Start())

	t.Cleanup(func() {
		require.NoError(t, node.Close())
		require.NoError(t, node.Wait())
	})

	// Wait for node to become ready.
	node.WaitForEventTimeout(10*time.Second, service.NodeSSHReady)
	require.NoError(t, err, "node didn't start after 10s")

	return node
}

type testServersOpts struct {
	bootstrap   []types.Resource
	configFuncs []func(cfg *service.Config)
}

type testServerOptFunc func(o *testServersOpts)

func withBootstrap(bootstrap ...types.Resource) testServerOptFunc {
	return func(o *testServersOpts) {
		o.bootstrap = bootstrap
	}
}

func withConfig(fn func(cfg *service.Config)) testServerOptFunc {
	return func(o *testServersOpts) {
		o.configFuncs = append(o.configFuncs, fn)
	}
}

func withHostname(hostname string) testServerOptFunc {
	return withConfig(func(cfg *service.Config) {
		cfg.Hostname = hostname
	})
}

func withSSHLabel(key, value string) testServerOptFunc {
	return withConfig(func(cfg *service.Config) {
		if cfg.SSH.Labels == nil {
			cfg.SSH.Labels = make(map[string]string)
		}
		cfg.SSH.Labels[key] = value
	})
}
