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

package main

import (
	"context"
	"fmt"
	"net"
	"os/user"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/breaker"
	apiclient "github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/config"
	"github.com/zmb3/teleport/lib/service"
)

type suite struct {
	root      *service.TeleportProcess
	leaf      *service.TeleportProcess
	connector types.OIDCConnector
	user      types.User
}

func (s *suite) setupRootCluster(t *testing.T, options testSuiteOptions) {
	sshListenAddr := localListenerAddr()
	_, sshListenPort, err := net.SplitHostPort(sshListenAddr)
	require.NoError(t, err)
	fileConfig := &config.FileConfig{
		Version: "v2",
		Global: config.Global{
			DataDir:  t.TempDir(),
			NodeName: "localnode",
		},
		SSH: config.SSH{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: localListenerAddr(),
			},
		},
		Proxy: config.Proxy{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: sshListenAddr,
			},
			SSHPublicAddr: []string{net.JoinHostPort("localhost", sshListenPort)},
			WebAddr:       localListenerAddr(),
			TunAddr:       localListenerAddr(),
		},
		Auth: config.Auth{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: localListenerAddr(),
			},
			ClusterName: "localhost",
		},
	}

	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	err = config.ApplyFileConfig(fileConfig, cfg)
	require.NoError(t, err)

	cfg.Proxy.DisableWebInterface = true
	cfg.Auth.StaticTokens, err = types.NewStaticTokens(types.StaticTokensSpecV2{
		StaticTokens: []types.ProvisionTokenV1{{
			Roles:   []types.SystemRole{types.RoleProxy, types.RoleDatabase, types.RoleNode, types.RoleTrustedCluster},
			Expires: time.Now().Add(time.Minute),
			Token:   staticToken,
		}},
	})
	require.NoError(t, err)

	user, err := user.Current()
	require.NoError(t, err)

	s.connector = mockConnector(t)
	sshLoginRole, err := types.NewRoleV3("ssh-login", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{user.Username},
		},
		Options: types.RoleOptions{
			ForwardAgent: true,
		},
	})
	require.NoError(t, err)
	kubeLoginRole, err := types.NewRoleV3("kube-login", types.RoleSpecV5{
		Allow: types.RoleConditions{
			KubeGroups: []string{user.Username},
			KubernetesLabels: types.Labels{
				types.Wildcard: []string{types.Wildcard},
			},
		},
	})
	require.NoError(t, err)

	s.user, err = types.NewUser("alice")
	require.NoError(t, err)
	s.user.SetRoles([]string{"access", "ssh-login", "kube-login"})
	cfg.Auth.Resources = []types.Resource{s.connector, s.user, sshLoginRole, kubeLoginRole}

	if options.rootConfigFunc != nil {
		options.rootConfigFunc(cfg)
	}

	s.root = runTeleport(t, cfg, options.newTeleportOptions...)
	t.Cleanup(func() { require.NoError(t, s.root.Close()) })
}

func (s *suite) setupLeafCluster(t *testing.T, options testSuiteOptions) {
	sshListenAddr := localListenerAddr()
	_, sshListenPort, err := net.SplitHostPort(sshListenAddr)
	require.NoError(t, err)
	fileConfig := &config.FileConfig{
		Version: "v2",
		Global: config.Global{
			DataDir:  t.TempDir(),
			NodeName: "localnode",
		},
		SSH: config.SSH{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: localListenerAddr(),
			},
		},
		Proxy: config.Proxy{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: sshListenAddr,
			},
			SSHPublicAddr: []string{net.JoinHostPort("localhost", sshListenPort)},
			WebAddr:       localListenerAddr(),
			TunAddr:       localListenerAddr(),
		},
		Auth: config.Auth{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: localListenerAddr(),
			},
			ClusterName:       "leaf1",
			ProxyListenerMode: types.ProxyListenerMode_Multiplex,
		},
	}

	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	err = config.ApplyFileConfig(fileConfig, cfg)
	require.NoError(t, err)

	user, err := user.Current()
	require.NoError(t, err)

	cfg.Proxy.DisableWebInterface = true
	sshLoginRole, err := types.NewRoleV3("ssh-login", types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{user.Username},
		},
	})
	require.NoError(t, err)

	tc, err := types.NewTrustedCluster("root-cluster", types.TrustedClusterSpecV2{
		Enabled:              true,
		Token:                staticToken,
		ProxyAddress:         s.root.Config.Proxy.WebAddr.String(),
		ReverseTunnelAddress: s.root.Config.Proxy.WebAddr.String(),
		RoleMap: []types.RoleMapping{
			{
				Remote: "access",
				Local:  []string{"access", "ssh-login"},
			},
		},
	})
	require.NoError(t, err)
	cfg.Auth.Resources = []types.Resource{sshLoginRole}
	if options.leafConfigFunc != nil {
		options.leafConfigFunc(cfg)
	}
	s.leaf = runTeleport(t, cfg, options.newTeleportOptions...)

	_, err = s.leaf.GetAuthServer().UpsertTrustedCluster(s.leaf.ExitContext(), tc)
	require.NoError(t, err)
}

type testSuiteOptions struct {
	rootConfigFunc     func(cfg *service.Config)
	leafConfigFunc     func(cfg *service.Config)
	leafCluster        bool
	validationFunc     func(*suite) bool
	newTeleportOptions []service.NewTeleportOption
}

type testSuiteOptionFunc func(o *testSuiteOptions)

func withRootConfigFunc(fn func(cfg *service.Config)) testSuiteOptionFunc {
	return func(o *testSuiteOptions) {
		o.rootConfigFunc = fn
	}
}

func withLeafConfigFunc(fn func(cfg *service.Config)) testSuiteOptionFunc {
	return func(o *testSuiteOptions) {
		o.leafConfigFunc = fn
	}
}

func withLeafCluster() testSuiteOptionFunc {
	return func(o *testSuiteOptions) {
		o.leafCluster = true
	}
}

func withValidationFunc(f func(*suite) bool) testSuiteOptionFunc {
	return func(o *testSuiteOptions) {
		o.validationFunc = f
	}
}

func withNewTeleportOption(opt service.NewTeleportOption) testSuiteOptionFunc {
	return func(o *testSuiteOptions) {
		o.newTeleportOptions = append(o.newTeleportOptions, opt)
	}
}

func newTestSuite(t *testing.T, opts ...testSuiteOptionFunc) *suite {
	var options testSuiteOptions
	for _, opt := range opts {
		opt(&options)
	}
	s := &suite{}

	s.setupRootCluster(t, options)

	if options.leafCluster || options.leafConfigFunc != nil {
		s.setupLeafCluster(t, options)
		// Wait for root/leaf to find each other.
		if s.root.Config.Auth.NetworkingConfig.GetProxyListenerMode() == types.ProxyListenerMode_Multiplex {
			require.Eventually(t, func() bool {
				rt, err := s.root.GetAuthServer().GetTunnelConnections(s.leaf.Config.Auth.ClusterName.GetClusterName())
				require.NoError(t, err)
				return len(rt) == 1
			}, time.Second*10, time.Second)
		} else {
			require.Eventually(t, func() bool {
				_, err := s.leaf.GetAuthServer().GetReverseTunnel(s.root.Config.Auth.ClusterName.GetClusterName())
				return err == nil
			}, time.Second*10, time.Second)
		}
	}

	if options.validationFunc != nil {
		require.Eventually(t, func() bool {
			return options.validationFunc(s)
		}, 10*time.Second, 500*time.Millisecond)
	}

	return s
}

func runTeleport(t *testing.T, cfg *service.Config, opts ...service.NewTeleportOption) *service.TeleportProcess {
	process, err := service.NewTeleport(cfg, opts...)
	require.NoError(t, err)
	require.NoError(t, process.Start())
	t.Cleanup(func() {
		require.NoError(t, process.Close())
		require.NoError(t, process.Wait())
	})

	var serviceReadyEvents []string
	if cfg.Proxy.Enabled {
		serviceReadyEvents = append(serviceReadyEvents, service.ProxyWebServerReady)
	}
	if cfg.SSH.Enabled {
		serviceReadyEvents = append(serviceReadyEvents, service.NodeSSHReady)
	}
	if cfg.Databases.Enabled {
		serviceReadyEvents = append(serviceReadyEvents, service.DatabasesReady)
	}
	waitForEvents(t, process, serviceReadyEvents...)

	if cfg.Databases.Enabled {
		waitForDatabases(t, process, cfg.Databases.Databases)
	}
	return process
}

func localListenerAddr() string {
	return fmt.Sprintf("localhost:%d", ports.PopInt())
}

func waitForEvents(t *testing.T, svc service.Supervisor, events ...string) {
	for _, event := range events {
		_, err := svc.WaitForEventTimeout(30*time.Second, event)
		require.NoError(t, err, "service server didn't receved %v event after 30s", event)
	}
}

func mustCreateAuthClientFormUserProfile(t *testing.T, tshHomePath, addr string) {
	ctx := context.Background()
	credentials := apiclient.LoadProfile(tshHomePath, "")
	c, err := apiclient.New(context.Background(), apiclient.Config{
		Addrs:                    []string{addr},
		Credentials:              []apiclient.Credentials{credentials},
		InsecureAddressDiscovery: true,
	})
	require.NoError(t, err)
	_, err = c.Ping(ctx)
	require.NoError(t, err)
}
