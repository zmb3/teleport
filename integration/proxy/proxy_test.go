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

package proxy

import (
	"bytes"
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gravitational/teleport/api/breaker"
	"github.com/gravitational/teleport/api/client"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/integration/appaccess"
	dbhelpers "github.com/gravitational/teleport/integration/db"
	"github.com/gravitational/teleport/integration/helpers"
	"github.com/gravitational/teleport/integration/kube"
	"github.com/gravitational/teleport/lib"
	"github.com/gravitational/teleport/lib/auth/testauthority"
	libclient "github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/srv/alpnproxy"
	alpncommon "github.com/gravitational/teleport/lib/srv/alpnproxy/common"
	"github.com/gravitational/teleport/lib/srv/db/common"
	"github.com/gravitational/teleport/lib/srv/db/mongodb"
	"github.com/gravitational/teleport/lib/srv/db/mysql"
	"github.com/gravitational/teleport/lib/srv/db/postgres"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"
)

// TestALPNSNIProxyMultiCluster tests SSH connection in multi-cluster setup with.
func TestALPNSNIProxyMultiCluster(t *testing.T) {
	testCase := []struct {
		name                      string
		mainClusterPortSetup      helpers.InstanceListenerSetupFunc
		secondClusterPortSetup    helpers.InstanceListenerSetupFunc
		disableALPNListenerOnRoot bool
		disableALPNListenerOnLeaf bool
	}{
		{
			name:                      "StandardAndOnePortSetupMasterALPNDisabled",
			mainClusterPortSetup:      helpers.StandardListenerSetup,
			secondClusterPortSetup:    helpers.SingleProxyPortSetup,
			disableALPNListenerOnRoot: true,
		},
		{
			name:                   "StandardAndOnePortSetup",
			mainClusterPortSetup:   helpers.StandardListenerSetup,
			secondClusterPortSetup: helpers.SingleProxyPortSetup,
		},
		{
			name:                   "TwoClusterOnePortSetup",
			mainClusterPortSetup:   helpers.SingleProxyPortSetup,
			secondClusterPortSetup: helpers.SingleProxyPortSetup,
		},
		{
			name:                      "OnePortAndStandardListenerSetupLeafALPNDisabled",
			mainClusterPortSetup:      helpers.SingleProxyPortSetup,
			secondClusterPortSetup:    helpers.StandardListenerSetup,
			disableALPNListenerOnLeaf: true,
		},
		{
			name:                   "OnePortAndStandardListenerSetup",
			mainClusterPortSetup:   helpers.SingleProxyPortSetup,
			secondClusterPortSetup: helpers.StandardListenerSetup,
		},
	}

	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			lib.SetInsecureDevMode(true)
			defer lib.SetInsecureDevMode(false)

			username := helpers.MustGetCurrentUser(t).Username

			suite := newSuite(t,
				withRootClusterConfig(rootClusterStandardConfig(t), func(config *service.Config) {
					config.Proxy.DisableALPNSNIListener = tc.disableALPNListenerOnRoot
				}),
				withLeafClusterConfig(leafClusterStandardConfig(t), func(config *service.Config) {
					config.Proxy.DisableALPNSNIListener = tc.disableALPNListenerOnLeaf
				}),
				withRootClusterListeners(tc.mainClusterPortSetup),
				withLeafClusterListeners(tc.secondClusterPortSetup),
				withRootAndLeafClusterRoles(createTestRole(username)),
				withStandardRoleMapping(),
			)
			// Run command in root.
			suite.mustConnectToClusterAndRunSSHCommand(t, helpers.ClientConfig{
				Login:   username,
				Cluster: suite.root.Secrets.SiteName,
				Host:    helpers.Loopback,
				Port:    helpers.Port(t, suite.root.SSH),
			})
			// Run command in leaf.
			suite.mustConnectToClusterAndRunSSHCommand(t, helpers.ClientConfig{
				Login:   username,
				Cluster: suite.leaf.Secrets.SiteName,
				Host:    helpers.Loopback,
				Port:    helpers.Port(t, suite.leaf.SSH),
			})
		})
	}
}

// TestALPNSNIProxyTrustedClusterNode tests ssh connection to a trusted cluster node.
func TestALPNSNIProxyTrustedClusterNode(t *testing.T) {
	testCase := []struct {
		name                       string
		mainClusterListenerSetup   helpers.InstanceListenerSetupFunc
		secondClusterListenerSetup helpers.InstanceListenerSetupFunc
		disableALPNListenerOnRoot  bool
		disableALPNListenerOnLeaf  bool
	}{
		{
			name:                       "StandardAndOnePortSetupMasterALPNDisabled",
			mainClusterListenerSetup:   helpers.StandardListenerSetup,
			secondClusterListenerSetup: helpers.SingleProxyPortSetup,
			disableALPNListenerOnRoot:  true,
		},
		{
			name:                       "StandardAndOnePortSetup",
			mainClusterListenerSetup:   helpers.StandardListenerSetup,
			secondClusterListenerSetup: helpers.SingleProxyPortSetup,
		},
		{
			name:                       "TwoClusterOnePortSetup",
			mainClusterListenerSetup:   helpers.SingleProxyPortSetup,
			secondClusterListenerSetup: helpers.SingleProxyPortSetup,
		},
		{
			name:                       "OnePortAndStandardListenerSetupLeafALPNDisabled",
			mainClusterListenerSetup:   helpers.SingleProxyPortSetup,
			secondClusterListenerSetup: helpers.StandardListenerSetup,
			disableALPNListenerOnLeaf:  true,
		},
		{
			name:                       "OnePortAndStandardListenerSetup",
			mainClusterListenerSetup:   helpers.SingleProxyPortSetup,
			secondClusterListenerSetup: helpers.StandardListenerSetup,
		},
	}
	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			lib.SetInsecureDevMode(true)
			defer lib.SetInsecureDevMode(false)

			username := helpers.MustGetCurrentUser(t).Username

			suite := newSuite(t,
				withRootClusterConfig(rootClusterStandardConfig(t)),
				withLeafClusterConfig(leafClusterStandardConfig(t)),
				withRootClusterListeners(tc.mainClusterListenerSetup),
				withLeafClusterListeners(tc.secondClusterListenerSetup),
				withRootClusterRoles(newRole(t, "maindevs", username)),
				withLeafClusterRoles(newRole(t, "auxdevs", username)),
				withRootAndLeafTrustedClusterReset(),
				withTrustedCluster(),
			)

			nodeHostname := "clusterauxnode"
			suite.addNodeToLeafCluster(t, "clusterauxnode")

			// Try and connect to a node in the Aux cluster from the Root cluster using
			// direct dialing.
			suite.mustConnectToClusterAndRunSSHCommand(t, helpers.ClientConfig{
				Login:   username,
				Cluster: suite.leaf.Secrets.SiteName,
				Host:    helpers.Loopback,
				Port:    helpers.Port(t, suite.leaf.SSH),
			})

			// Try and connect to a node in the Aux cluster from the Root cluster using
			// tunnel dialing.
			suite.mustConnectToClusterAndRunSSHCommand(t, helpers.ClientConfig{
				Login:   username,
				Cluster: suite.leaf.Secrets.SiteName,
				Host:    nodeHostname,
			})
		})
	}
}

// TestALPNSNIProxyMultiCluster tests if the reverse tunnel uses http_proxy
// on a single proxy port setup.
func TestALPNSNIHTTPSProxy(t *testing.T) {
	// start the http proxy
	ph := &helpers.ProxyHandler{}
	ts := httptest.NewServer(ph)
	defer ts.Close()

	// set the http_proxy environment variable
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	t.Setenv("http_proxy", u.Host)

	username := helpers.MustGetCurrentUser(t).Username

	// We need to use the non-loopback address for our Teleport cluster, as the
	// Go HTTP library will recognize requests to the loopback address and
	// refuse to use the HTTP proxy, which will invalidate the test.
	addr, err := helpers.GetLocalIP()
	require.NoError(t, err)

	suite := newSuite(t,
		withRootClusterConfig(rootClusterStandardConfig(t)),
		withLeafClusterConfig(leafClusterStandardConfig(t)),
		withRootClusterNodeName(addr),
		withLeafClusterNodeName(addr),
		withRootClusterListeners(helpers.SingleProxyPortSetupOn(addr)),
		withLeafClusterListeners(helpers.SingleProxyPortSetupOn(addr)),
		withRootAndLeafClusterRoles(createTestRole(username)),
		withStandardRoleMapping(),
	)

	// Wait for both cluster to see each other via reverse tunnels.
	require.Eventually(t, helpers.WaitForClusters(suite.root.Tunnel, 1), 10*time.Second, 1*time.Second,
		"Two clusters do not see each other: tunnels are not working.")
	require.Eventually(t, helpers.WaitForClusters(suite.leaf.Tunnel, 1), 10*time.Second, 1*time.Second,
		"Two clusters do not see each other: tunnels are not working.")

	require.Greater(t, ph.Count(), 0, "proxy did not intercept any connection")
}

// TestMultiPortHTTPSProxy tests if the reverse tunnel uses http_proxy
// on a multiple proxy port setup.
func TestMultiPortHTTPSProxy(t *testing.T) {
	// start the http proxy
	ph := &helpers.ProxyHandler{}
	ts := httptest.NewServer(ph)
	defer ts.Close()

	// set the http_proxy environment variable
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	t.Setenv("http_proxy", u.Host)

	username := helpers.MustGetCurrentUser(t).Username

	// We need to use the non-loopback address for our Teleport cluster, as the
	// Go HTTP library will recognize requests to the loopback address and
	// refuse to use the HTTP proxy, which will invalidate the test.
	addr, err := helpers.GetLocalIP()
	require.NoError(t, err)

	suite := newSuite(t,
		withRootClusterConfig(rootClusterStandardConfig(t)),
		withLeafClusterConfig(leafClusterStandardConfig(t)),
		withRootClusterNodeName(addr),
		withLeafClusterNodeName(addr),
		withRootClusterListeners(helpers.SingleProxyPortSetupOn(addr)),
		withLeafClusterListeners(helpers.SingleProxyPortSetupOn(addr)),
		withRootAndLeafClusterRoles(createTestRole(username)),
		withStandardRoleMapping(),
	)

	// Wait for both cluster to see each other via reverse tunnels.
	require.Eventually(t, helpers.WaitForClusters(suite.root.Tunnel, 1), 10*time.Second, 1*time.Second,
		"Two clusters do not see each other: tunnels are not working.")
	require.Eventually(t, helpers.WaitForClusters(suite.leaf.Tunnel, 1), 10*time.Second, 1*time.Second,
		"Two clusters do not see each other: tunnels are not working.")

	require.Greater(t, ph.Count(), 0, "proxy did not intercept any connection")
}

// TestAlpnSniProxyKube tests Kubernetes access with custom Kube API mock where traffic is forwarded via
// SNI ALPN proxy service to Kubernetes service based on TLS SNI value.
func TestALPNSNIProxyKube(t *testing.T) {
	const (
		localK8SNI = "kube.teleport.cluster.local"
		k8User     = "alice@example.com"
		k8RoleName = "kubemaster"
	)

	kubeAPIMockSvr := startKubeAPIMock(t)
	kubeConfigPath := mustCreateKubeConfigFile(t, k8ClientConfig(kubeAPIMockSvr.URL, localK8SNI))

	username := helpers.MustGetCurrentUser(t).Username
	kubeRoleSpec := types.RoleSpecV6{
		Allow: types.RoleConditions{
			Logins:     []string{username},
			KubeGroups: []string{kube.TestImpersonationGroup},
			KubeUsers:  []string{k8User},
		},
	}
	kubeRole, err := types.NewRoleV3(k8RoleName, kubeRoleSpec)
	require.NoError(t, err)

	suite := newSuite(t,
		withRootClusterConfig(rootClusterStandardConfig(t), func(config *service.Config) {
			config.Proxy.Kube.Enabled = true
			config.Proxy.Kube.KubeconfigPath = kubeConfigPath
			config.Proxy.Kube.LegacyKubeProxy = true
		}),
		withLeafClusterConfig(leafClusterStandardConfig(t)),
		withRootAndLeafClusterRoles(kubeRole),
		withStandardRoleMapping(),
	)

	k8Client, _, err := kube.ProxyClient(kube.ProxyConfig{
		T:                   suite.root,
		Username:            kubeRoleSpec.Allow.Logins[0],
		KubeUsers:           kubeRoleSpec.Allow.KubeGroups,
		KubeGroups:          kubeRoleSpec.Allow.KubeUsers,
		CustomTLSServerName: localK8SNI,
		TargetAddress:       suite.root.Config.Proxy.WebAddr,
	})
	require.NoError(t, err)

	resp, err := k8Client.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp.Items), "pods item length mismatch")
}

// TestALPNSNIProxyKubeV2Leaf tests remove cluster kubernetes configuration where root and leaf proxies
// are using V2 configuration with Multiplex proxy listener.
func TestALPNSNIProxyKubeV2Leaf(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	const (
		localK8SNI = "kube.teleport.cluster.local"
		k8User     = "alice@example.com"
		k8RoleName = "kubemaster"
	)

	kubeAPIMockSvr := startKubeAPIMock(t)
	kubeConfigPath := mustCreateKubeConfigFile(t, k8ClientConfig(kubeAPIMockSvr.URL, localK8SNI))

	username := helpers.MustGetCurrentUser(t).Username
	kubeRoleSpec := types.RoleSpecV6{
		Allow: types.RoleConditions{
			Logins:     []string{username},
			KubeGroups: []string{kube.TestImpersonationGroup},
			KubeUsers:  []string{k8User},
		},
	}
	kubeRole, err := types.NewRoleV3(k8RoleName, kubeRoleSpec)
	require.NoError(t, err)

	suite := newSuite(t,
		withRootClusterConfig(rootClusterStandardConfig(t), func(config *service.Config) {
			config.Proxy.Kube.Enabled = true
			config.Version = defaults.TeleportConfigVersionV2
		}),
		withLeafClusterConfig(leafClusterStandardConfig(t), func(config *service.Config) {
			config.Version = defaults.TeleportConfigVersionV2
			config.Proxy.Kube.Enabled = true

			config.Kube.Enabled = true
			config.Kube.KubeconfigPath = kubeConfigPath
			config.Kube.ListenAddr = utils.MustParseAddr(
				helpers.NewListener(t, service.ListenerKube, &config.FileDescriptors))
		}),
		withRootClusterRoles(kubeRole),
		withLeafClusterRoles(kubeRole),
		withRootAndLeafTrustedClusterReset(),
		withTrustedCluster(),
	)

	k8Client, _, err := kube.ProxyClient(kube.ProxyConfig{
		T:                   suite.root,
		Username:            kubeRoleSpec.Allow.Logins[0],
		KubeUsers:           kubeRoleSpec.Allow.KubeGroups,
		KubeGroups:          kubeRoleSpec.Allow.KubeUsers,
		CustomTLSServerName: localK8SNI,
		TargetAddress:       suite.root.Config.Proxy.WebAddr,
		RouteToCluster:      suite.leaf.Secrets.SiteName,
	})
	require.NoError(t, err)

	resp, err := k8Client.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(resp.Items), "pods item length mismatch")
}

// TestALPNSNIProxyDatabaseAccess test DB connection forwarded through local SNI ALPN proxy where
// DB protocol is wrapped into TLS and forwarded to proxy ALPN SNI service and routed to appropriate db service.
func TestALPNSNIProxyDatabaseAccess(t *testing.T) {
	pack := dbhelpers.SetupDatabaseTest(t,
		dbhelpers.WithListenerSetupDatabaseTest(helpers.SingleProxyPortSetup),
		dbhelpers.WithLeafConfig(func(config *service.Config) {
			config.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
		}),
		dbhelpers.WithRootConfig(func(config *service.Config) {
			config.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
		}),
	)
	pack.WaitForLeaf(t)

	t.Run("mysql", func(t *testing.T) {
		lp := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolMySQL)
		t.Run("connect to main cluster via proxy", func(t *testing.T) {
			client, err := mysql.MakeTestClient(common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MysqlService.Name,
					Protocol:    pack.Root.MysqlService.Protocol,
					Username:    "root",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			result, err := client.Execute("select 1")
			require.NoError(t, err)
			require.Equal(t, mysql.TestQueryResponse, result)

			// Disconnect.
			err = client.Close()
			require.NoError(t, err)
		})

		t.Run("connect to leaf cluster via proxy", func(t *testing.T) {
			client, err := mysql.MakeTestClient(common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Leaf.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Leaf.MysqlService.Name,
					Protocol:    pack.Leaf.MysqlService.Protocol,
					Username:    "root",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			result, err := client.Execute("select 1")
			require.NoError(t, err)
			require.Equal(t, mysql.TestQueryResponse, result)

			// Disconnect.
			err = client.Close()
			require.NoError(t, err)
		})
		t.Run("connect to main cluster via proxy using ping protocol", func(t *testing.T) {
			pingProxy := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolWithPing(alpncommon.ProtocolMySQL))
			client, err := mysql.MakeTestClient(common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    pingProxy.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MysqlService.Name,
					Protocol:    pack.Root.MysqlService.Protocol,
					Username:    "root",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			result, err := client.Execute("select 1")
			require.NoError(t, err)
			require.Equal(t, mysql.TestQueryResponse, result)

			// Disconnect.
			err = client.Close()
			require.NoError(t, err)
		})
	})

	t.Run("postgres", func(t *testing.T) {
		lp := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolPostgres)
		t.Run("connect to main cluster via proxy", func(t *testing.T) {
			client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.PostgresService.Name,
					Protocol:    pack.Root.PostgresService.Protocol,
					Username:    "postgres",
					Database:    "test",
				},
			})
			require.NoError(t, err)
			mustRunPostgresQuery(t, client)
			mustClosePostgresClient(t, client)
		})
		t.Run("connect to leaf cluster via proxy", func(t *testing.T) {
			client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Leaf.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Leaf.PostgresService.Name,
					Protocol:    pack.Leaf.PostgresService.Protocol,
					Username:    "postgres",
					Database:    "test",
				},
			})
			require.NoError(t, err)
			mustRunPostgresQuery(t, client)
			mustClosePostgresClient(t, client)
		})
		t.Run("connect to main cluster via proxy with ping protocol", func(t *testing.T) {
			pingProxy := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolWithPing(alpncommon.ProtocolPostgres))
			client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    pingProxy.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.PostgresService.Name,
					Protocol:    pack.Root.PostgresService.Protocol,
					Username:    "postgres",
					Database:    "test",
				},
			})
			require.NoError(t, err)
			mustRunPostgresQuery(t, client)
			mustClosePostgresClient(t, client)
		})
	})

	t.Run("mongo", func(t *testing.T) {
		lp := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolMongoDB)
		t.Run("connect to main cluster via proxy", func(t *testing.T) {
			client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MongoService.Name,
					Protocol:    pack.Root.MongoService.Protocol,
					Username:    "admin",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			_, err = client.Database("test").Collection("test").Find(context.Background(), bson.M{})
			require.NoError(t, err)

			// Disconnect.
			err = client.Disconnect(context.Background())
			require.NoError(t, err)
		})
		t.Run("connect to leaf cluster via proxy", func(t *testing.T) {
			client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Leaf.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Leaf.MongoService.Name,
					Protocol:    pack.Leaf.MongoService.Protocol,
					Username:    "admin",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			_, err = client.Database("test").Collection("test").Find(context.Background(), bson.M{})
			require.NoError(t, err)

			// Disconnect.
			err = client.Disconnect(context.Background())
			require.NoError(t, err)
		})
		t.Run("connect to main cluster via proxy with ping protocol", func(t *testing.T) {
			pingProxy := mustStartALPNLocalProxy(t, pack.Root.Cluster.SSHProxy, alpncommon.ProtocolWithPing(alpncommon.ProtocolMongoDB))
			client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    pingProxy.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MongoService.Name,
					Protocol:    pack.Root.MongoService.Protocol,
					Username:    "admin",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			_, err = client.Database("test").Collection("test").Find(context.Background(), bson.M{})
			require.NoError(t, err)

			// Disconnect.
			err = client.Disconnect(context.Background())
			require.NoError(t, err)
		})
	})

	// Simulate situations where an AWS ALB is between client and the Teleport
	// Proxy service, which drops ALPN along the way. The ALPN local proxy will
	// need to make a connection upgrade first through a web API provided by
	// the Proxy server and then tunnel the original ALPN/TLS routing traffic
	// inside this tunnel.
	t.Run("ALPN connection upgrade", func(t *testing.T) {
		// Make a mock ALB which points to the Teleport Proxy Service. Then
		// ALPN local proxies will point to this ALB instead.
		albProxy := mustStartMockALBProxy(t, pack.Root.Cluster.Web)

		// Test a protocol in the alpncommon.IsDBTLSProtocol list where
		// the database client will perform a native TLS handshake.
		//
		// Packet layers:
		// - HTTPS served by Teleport web server for connection upgrade
		// - TLS routing with alpncommon.ProtocolMongoDB (no client cert)
		// - TLS with client cert (provided by the database client)
		// - MongoDB
		t.Run("database client native TLS", func(t *testing.T) {
			lp := mustStartALPNLocalProxyWithConfig(t, alpnproxy.LocalProxyConfig{
				RemoteProxyAddr:         albProxy.Addr().String(),
				Protocols:               []alpncommon.Protocol{alpncommon.ProtocolMongoDB},
				ALPNConnUpgradeRequired: true,
				InsecureSkipVerify:      true,
			})
			client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MongoService.Name,
					Protocol:    pack.Root.MongoService.Protocol,
					Username:    "admin",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			_, err = client.Database("test").Collection("test").Find(context.Background(), bson.M{})
			require.NoError(t, err)

			// Disconnect.
			require.NoError(t, client.Disconnect(context.Background()))
		})

		// Test the case where the database client cert is terminated within
		// the database protocol.
		//
		// Packet layers:
		// - HTTPS served by Teleport web server for connection upgrade
		// - TLS routing with alpncommon.ProtocolMySQL (no client cert)
		// - MySQL handshake then upgrade to TLS with Teleport issued client cert
		// - MySQL protocol
		t.Run("MySQL custom TLS", func(t *testing.T) {
			lp := mustStartALPNLocalProxyWithConfig(t, alpnproxy.LocalProxyConfig{
				RemoteProxyAddr:         albProxy.Addr().String(),
				Protocols:               []alpncommon.Protocol{alpncommon.ProtocolMySQL},
				ALPNConnUpgradeRequired: true,
				InsecureSkipVerify:      true,
			})
			client, err := mysql.MakeTestClient(common.TestClientConfig{
				AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
				Address:    lp.GetAddr(),
				Cluster:    pack.Root.Cluster.Secrets.SiteName,
				Username:   pack.Root.User.GetName(),
				RouteToDatabase: tlsca.RouteToDatabase{
					ServiceName: pack.Root.MysqlService.Name,
					Protocol:    pack.Root.MysqlService.Protocol,
					Username:    "root",
				},
			})
			require.NoError(t, err)

			// Execute a query.
			result, err := client.Execute("select 1")
			require.NoError(t, err)
			require.Equal(t, mysql.TestQueryResponse, result)

			// Disconnect.
			require.NoError(t, client.Close())
		})

		// Test the case where the client cert is terminated by Teleport and
		// the database client sends data in plain database protocol.
		//
		// Packet layers:
		// - HTTPS served by Teleport web server for connection upgrade
		// - TLS routing with alpncommon.ProtocolMySQL (client cert provided by ALPN local proxy)
		// - MySQL protocol
		t.Run("authenticated tunnel", func(t *testing.T) {
			routeToDatabase := tlsca.RouteToDatabase{
				ServiceName: pack.Root.MysqlService.Name,
				Protocol:    pack.Root.MysqlService.Protocol,
				Username:    "root",
			}
			clientTLSConfig, err := common.MakeTestClientTLSConfig(common.TestClientConfig{
				AuthClient:      pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
				AuthServer:      pack.Root.Cluster.Process.GetAuthServer(),
				Cluster:         pack.Root.Cluster.Secrets.SiteName,
				Username:        pack.Root.User.GetName(),
				RouteToDatabase: routeToDatabase,
			})
			require.NoError(t, err)

			lp := mustStartALPNLocalProxyWithConfig(t, alpnproxy.LocalProxyConfig{
				RemoteProxyAddr:         albProxy.Addr().String(),
				Protocols:               []alpncommon.Protocol{alpncommon.ProtocolMySQL},
				ALPNConnUpgradeRequired: true,
				InsecureSkipVerify:      true,
				Certs:                   clientTLSConfig.Certificates,
			})

			client, err := mysql.MakeTestClientWithoutTLS(lp.GetAddr(), routeToDatabase)
			require.NoError(t, err)

			// Execute a query.
			result, err := client.Execute("select 1")
			require.NoError(t, err)
			require.Equal(t, mysql.TestQueryResponse, result)

			// Disconnect.
			require.NoError(t, client.Close())
		})
	})

	t.Run("authenticated tunnel with cert renewal", func(t *testing.T) {
		// get a teleport client
		tc, err := pack.Root.Cluster.NewClient(helpers.ClientConfig{
			Login:   pack.Root.User.GetName(),
			Cluster: pack.Root.Cluster.Secrets.SiteName,
		})
		require.NoError(t, err)
		routeToDatabase := tlsca.RouteToDatabase{
			ServiceName: pack.Root.MysqlService.Name,
			Protocol:    pack.Root.MysqlService.Protocol,
			Username:    "root",
		}
		// inject a fake clock into the middleware so we can control when it thinks certs have expired
		fakeClock := clockwork.NewFakeClockAt(time.Now())

		// configure local proxy without certs but with cert checking/reissuing middleware
		// local proxy middleware should fetch a DB cert when the local proxy starts
		lp := mustStartALPNLocalProxyWithConfig(t, alpnproxy.LocalProxyConfig{
			RemoteProxyAddr:    pack.Root.Cluster.SSHProxy,
			Protocols:          []alpncommon.Protocol{alpncommon.ProtocolMySQL},
			InsecureSkipVerify: true,
			Middleware:         libclient.NewDBCertChecker(tc, routeToDatabase, fakeClock),
			Clock:              fakeClock,
		})

		client, err := mysql.MakeTestClientWithoutTLS(lp.GetAddr(), routeToDatabase)
		require.NoError(t, err)

		// Execute a query.
		result, err := client.Execute("select 1")
		require.NoError(t, err)
		require.Equal(t, mysql.TestQueryResponse, result)

		// Disconnect.
		require.NoError(t, client.Close())

		// advance the fake clock and verify that the local proxy thinks its cert expired.
		fakeClock.Advance(time.Hour * 48)
		err = lp.CheckDBCerts(routeToDatabase)
		require.Error(t, err)
		var x509Err x509.CertificateInvalidError
		require.ErrorAs(t, err, &x509Err)
		require.Equal(t, x509Err.Reason, x509.Expired)
		require.Contains(t, x509Err.Detail, "is after")

		// Open a new connection
		client, err = mysql.MakeTestClientWithoutTLS(lp.GetAddr(), routeToDatabase)
		require.NoError(t, err)

		// Execute a query.
		result, err = client.Execute("select 1")
		require.NoError(t, err)
		require.Equal(t, mysql.TestQueryResponse, result)

		// Disconnect.
		require.NoError(t, client.Close())
	})

	t.Run("teleterm gateways cert renewal", func(t *testing.T) {
		testTeletermGatewaysCertRenewal(t, pack)
	})
}

// TestALPNSNIProxyAppAccess tests application access via ALPN SNI proxy service.
func TestALPNSNIProxyAppAccess(t *testing.T) {
	pack := appaccess.SetupWithOptions(t, appaccess.AppTestOptions{
		RootClusterListeners: helpers.SingleProxyPortSetup,
		LeafClusterListeners: helpers.SingleProxyPortSetup,
		RootConfig: func(config *service.Config) {
			config.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
		},
		LeafConfig: func(config *service.Config) {
			config.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
		},
	})

	sess := pack.CreateAppSession(t, pack.RootAppPublicAddr(), pack.RootAppClusterName())
	status, _, err := pack.MakeRequest(sess, http.MethodGet, "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	sess = pack.CreateAppSession(t, pack.LeafAppPublicAddr(), pack.LeafAppClusterName())
	status, _, err = pack.MakeRequest(sess, http.MethodGet, "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

// TestALPNProxyRootLeafAuthDial tests dialing local/remote auth service based on ALPN
// teleport-auth protocol and ServerName as encoded cluster name.
func TestALPNProxyRootLeafAuthDial(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	username := helpers.MustGetCurrentUser(t).Username

	suite := newSuite(t,
		withRootClusterConfig(rootClusterStandardConfig(t)),
		withLeafClusterConfig(leafClusterStandardConfig(t)),
		withRootClusterListeners(helpers.SingleProxyPortSetup),
		withLeafClusterListeners(helpers.SingleProxyPortSetup),
		withRootClusterRoles(newRole(t, "rootdevs", username)),
		withLeafClusterRoles(newRole(t, "leafdevs", username)),
		withRootAndLeafTrustedClusterReset(),
		withTrustedCluster(),
	)

	client, err := suite.root.NewClient(helpers.ClientConfig{
		Login:   username,
		Cluster: suite.root.Hostname,
	})
	require.NoError(t, err)

	ctx := context.Background()
	proxyClient, err := client.ConnectToProxy(context.Background())
	require.NoError(t, err)

	// Dial root auth service.
	rootAuthClient, err := proxyClient.ConnectToAuthServiceThroughALPNSNIProxy(ctx, "root.example.com", "")
	require.NoError(t, err)
	pr, err := rootAuthClient.Ping(ctx)
	require.NoError(t, err)
	require.Equal(t, "root.example.com", pr.ClusterName)
	err = rootAuthClient.Close()
	require.NoError(t, err)

	// Dial leaf auth service.
	leafAuthClient, err := proxyClient.ConnectToAuthServiceThroughALPNSNIProxy(ctx, "leaf.example.com", "")
	require.NoError(t, err)
	pr, err = leafAuthClient.Ping(ctx)
	require.NoError(t, err)
	require.Equal(t, "leaf.example.com", pr.ClusterName)
	err = leafAuthClient.Close()
	require.NoError(t, err)
}

// TestALPNProxyAuthClientConnectWithUserIdentity creates and connects to the Auth service
// using user identity file when teleport is configured with Multiple proxy listener mode.
func TestALPNProxyAuthClientConnectWithUserIdentity(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	cfg := helpers.InstanceConfig{
		ClusterName: "root.example.com",
		HostID:      uuid.New().String(),
		NodeName:    helpers.Loopback,
		Log:         utils.NewLoggerForTests(),
	}
	cfg.Listeners = helpers.SingleProxyPortSetup(t, &cfg.Fds)
	rc := helpers.NewInstance(t, cfg)

	rcConf := service.MakeDefaultConfig()
	rcConf.DataDir = t.TempDir()
	rcConf.Auth.Enabled = true
	rcConf.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
	rcConf.Auth.Preference.SetSecondFactor("off")
	rcConf.Proxy.Enabled = true
	rcConf.Proxy.DisableWebInterface = true
	rcConf.SSH.Enabled = false
	rcConf.Version = "v2"
	rcConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	username := helpers.MustGetCurrentUser(t).Username
	rc.AddUser(username, []string{username})

	err := rc.CreateEx(t, nil, rcConf)
	require.NoError(t, err)
	err = rc.Start()
	require.NoError(t, err)
	defer rc.StopAll()

	identityFilePath := helpers.MustCreateUserIdentityFile(t, rc, username, time.Hour)

	identity := client.LoadIdentityFile(identityFilePath)
	require.NoError(t, err)

	tc, err := client.New(context.Background(), client.Config{
		Addrs:                    []string{rc.Web},
		Credentials:              []client.Credentials{identity},
		InsecureAddressDiscovery: true,
	})
	require.NoError(t, err)

	resp, err := tc.Ping(context.Background())
	require.NoError(t, err)
	require.Equal(t, rc.Secrets.SiteName, resp.ClusterName)
}

// TestALPNProxyDialProxySSHWithoutInsecureMode tests dialing to the localhost with teleport-proxy-ssh
// protocol without using insecure mode in order to check if establishing connection to localhost works properly.
func TestALPNProxyDialProxySSHWithoutInsecureMode(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	privateKey, publicKey, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	rootCfg := helpers.InstanceConfig{
		ClusterName: "root.example.com",
		HostID:      uuid.New().String(),
		NodeName:    helpers.Loopback,
		Priv:        privateKey,
		Pub:         publicKey,
		Log:         utils.NewLoggerForTests(),
	}
	rootCfg.Listeners = helpers.StandardListenerSetup(t, &rootCfg.Fds)
	rc := helpers.NewInstance(t, rootCfg)
	username := helpers.MustGetCurrentUser(t).Username
	rc.AddUser(username, []string{username})

	// Make root cluster config.
	rcConf := service.MakeDefaultConfig()
	rcConf.DataDir = t.TempDir()
	rcConf.Auth.Enabled = true
	rcConf.Auth.Preference.SetSecondFactor("off")
	rcConf.Proxy.Enabled = true
	rcConf.Proxy.DisableWebInterface = true
	rcConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	err = rc.CreateEx(t, nil, rcConf)
	require.NoError(t, err)

	err = rc.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		rc.StopAll()
	})

	// Disable insecure mode to make sure that dialing to localhost works.
	lib.SetInsecureDevMode(false)
	cfg := helpers.ClientConfig{
		Login:   username,
		Cluster: rc.Secrets.SiteName,
		Host:    "localhost",
	}

	ctx := context.Background()
	output := &bytes.Buffer{}
	cmd := []string{"echo", "hello world"}
	tc, err := rc.NewClient(cfg)
	require.NoError(t, err)
	tc.Stdout = output

	// Try to connect to the separate proxy SSH listener.
	tc.TLSRoutingEnabled = false
	err = tc.SSH(ctx, cmd, false)
	require.NoError(t, err)
	require.Equal(t, "hello world\n", output.String())
	output.Reset()

	// Try to connect to the ALPN SNI Listener.
	tc.TLSRoutingEnabled = true
	err = tc.SSH(ctx, cmd, false)
	require.NoError(t, err)
	require.Equal(t, "hello world\n", output.String())
}

// TestALPNProxyHTTPProxyNoProxyDial tests if a node joining to root cluster
// takes into account http_proxy and no_proxy env variables.
func TestALPNProxyHTTPProxyNoProxyDial(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	// We need to use the non-loopback address for our Teleport cluster, as the
	// Go HTTP library will recognize requests to the loopback address and
	// refuse to use the HTTP proxy, which will invalidate the test.
	addr, err := helpers.GetLocalIP()
	require.NoError(t, err)

	instanceCfg := helpers.InstanceConfig{
		ClusterName: "root.example.com",
		HostID:      uuid.New().String(),
		NodeName:    addr,
		Log:         utils.NewLoggerForTests(),
	}
	instanceCfg.Listeners = helpers.SingleProxyPortSetupOn(addr)(t, &instanceCfg.Fds)
	rc := helpers.NewInstance(t, instanceCfg)
	username := helpers.MustGetCurrentUser(t).Username
	rc.AddUser(username, []string{username})

	rcConf := service.MakeDefaultConfig()
	rcConf.DataDir = t.TempDir()
	rcConf.Auth.Enabled = true
	rcConf.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
	rcConf.Auth.Preference.SetSecondFactor("off")
	rcConf.Proxy.Enabled = true
	rcConf.Proxy.DisableWebInterface = true
	rcConf.SSH.Enabled = false
	rcConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	err = rc.CreateEx(t, nil, rcConf)
	require.NoError(t, err)

	err = rc.Start()
	require.NoError(t, err)
	defer rc.StopAll()

	// Create and start http_proxy server.
	ph := &helpers.ProxyHandler{}
	ts := httptest.NewServer(ph)
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)

	t.Setenv("http_proxy", u.Host)
	t.Setenv("no_proxy", addr)

	rcProxyAddr := rc.Web

	// Start the node, due to no_proxy=127.0.0.1 env variable the connection established
	// to the proxy should not go through the http_proxy server.
	_, err = rc.StartNode(makeNodeConfig("first-root-node", rcProxyAddr))
	require.NoError(t, err)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
	defer cancel()

	err = helpers.WaitForNodeCount(ctx, rc, "root.example.com", 1)
	require.NoError(t, err)

	require.Zero(t, ph.Count())

	// Unset the no_proxy=127.0.0.1 env variable. After that a new node
	// should take into account the http_proxy address and connection should go through the http_proxy.
	require.NoError(t, os.Unsetenv("no_proxy"))
	_, err = rc.StartNode(makeNodeConfig("second-root-node", rcProxyAddr))
	require.NoError(t, err)
	err = helpers.WaitForNodeCount(ctx, rc, "root.example.com", 2)
	require.NoError(t, err)

	require.NotZero(t, ph.Count())
}

// TestALPNProxyHTTPProxyBasicAuthDial tests if a node joining to root cluster
// takes into account http_proxy with basic auth credentials in the address
func TestALPNProxyHTTPProxyBasicAuthDial(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	log := utils.NewLoggerForTests()

	// We need to use the non-loopback address for our Teleport cluster, as the
	// Go HTTP library will recognize requests to the loopback address and
	// refuse to use the HTTP proxy, which will invalidate the test.
	rcAddr, err := helpers.GetLocalIP()
	require.NoError(t, err)

	log.Info("Creating Teleport instance...")
	cfg := helpers.InstanceConfig{
		ClusterName: "root.example.com",
		HostID:      uuid.New().String(),
		NodeName:    rcAddr,
		Log:         log,
	}
	cfg.Listeners = helpers.SingleProxyPortSetupOn(rcAddr)(t, &cfg.Fds)
	rc := helpers.NewInstance(t, cfg)
	defer rc.StopAll()
	log.Info("Teleport root cluster instance created")

	username := helpers.MustGetCurrentUser(t).Username
	rc.AddUser(username, []string{username})

	rcConf := service.MakeDefaultConfig()
	rcConf.DataDir = t.TempDir()
	rcConf.Auth.Enabled = true
	rcConf.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
	rcConf.Auth.Preference.SetSecondFactor("off")
	rcConf.Proxy.Enabled = true
	rcConf.Proxy.DisableWebInterface = true
	rcConf.SSH.Enabled = false
	rcConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	rcConf.Log = log

	log.Infof("Root cluster config: %#v", rcConf)

	log.Info("Creating Root cluster...")
	err = rc.CreateEx(t, nil, rcConf)
	require.NoError(t, err)

	log.Info("Starting Root Cluster...")
	err = rc.Start()
	require.NoError(t, err)

	// Create and start http_proxy server.
	log.Info("Creating HTTP Proxy server...")
	ph := &helpers.ProxyHandler{}
	authorizer := helpers.NewProxyAuthorizer(ph, "alice", "rosebud")
	ts := httptest.NewServer(authorizer)
	defer ts.Close()

	proxyURL, err := url.Parse(ts.URL)
	require.NoError(t, err)
	log.Infof("HTTP Proxy server running on %s", proxyURL)

	// set http_proxy to user:password@host
	// these credentials will be rejected by the auth proxy (initially).
	user := "aladdin"
	pass := "open sesame"
	t.Setenv("http_proxy", helpers.MakeProxyAddr(user, pass, proxyURL.Host))

	rcProxyAddr := net.JoinHostPort(rcAddr, helpers.PortStr(t, rc.Web))
	nodeCfg := makeNodeConfig("node1", rcProxyAddr)
	nodeCfg.Log = log

	timeout := time.Second * 60
	startErrC := make(chan error)
	// start the node but don't block waiting for it while it attempts to connect to the auth server.
	go func() {
		_, err := rc.StartNode(nodeCfg)
		startErrC <- err
	}()
	require.ErrorIs(t, authorizer.WaitForRequest(timeout), trace.AccessDenied("bad credentials"))
	require.Zero(t, ph.Count())

	// set the auth credentials to match our environment
	authorizer.SetCredentials(user, pass)

	// with env set correctly and authorized, the node should register.
	require.NoError(t, <-startErrC)
	require.NoError(t, helpers.WaitForNodeCount(context.Background(), rc, rc.Secrets.SiteName, 1))
	require.Greater(t, ph.Count(), 0)
}
