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

package common

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/client/identityfile"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/kube/kubeconfig"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

func TestAuthSignKubeconfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	clusterName, err := services.NewClusterNameWithRandomID(types.ClusterNameSpecV2{
		ClusterName: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	remoteCluster, err := types.NewRemoteCluster("leaf.example.com")
	if err != nil {
		t.Fatal(err)
	}

	_, cert, err := tlsca.GenerateSelfSignedCA(pkix.Name{CommonName: "example.com"}, nil, time.Minute)
	require.NoError(t, err)

	ca, err := types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:        types.HostCA,
		ClusterName: "example.com",
		ActiveKeys: types.CAKeySet{
			SSH: []*types.SSHKeyPair{{PublicKey: []byte("SSH CA cert")}},
			TLS: []*types.TLSKeyPair{{Cert: cert}},
		},
	})
	require.NoError(t, err)

	client := &mockClient{
		clusterName:    clusterName,
		remoteClusters: []types.RemoteCluster{remoteCluster},
		userCerts: &proto.Certs{
			SSH: []byte("SSH cert"),
			TLS: cert,
			TLSCACerts: [][]byte{
				cert,
			},
		},
		cas: []types.CertAuthority{ca},
		proxies: []types.Server{
			&types.ServerV2{
				Kind:    types.KindNode,
				Version: types.V2,
				Metadata: types.Metadata{
					Name: "proxy",
				},
				Spec: types.ServerSpecV2{
					PublicAddr: "proxy-from-api.example.com:3080",
				},
			},
		},
	}
	tests := []struct {
		desc        string
		ac          AuthCommand
		wantAddr    string
		wantCluster string
		assertErr   require.ErrorAssertionFunc
	}{
		{
			desc: "valid --proxy URL with valid URL scheme",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				proxyAddr:     "https://proxy-from-flag.example.com",
			},
			wantAddr:  "https://proxy-from-flag.example.com",
			assertErr: require.NoError,
		},
		{
			desc: "valid --proxy URL with invalid URL scheme",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				proxyAddr:     "file://proxy-from-flag.example.com",
			},
			assertErr: func(t require.TestingT, err error, _ ...interface{}) {
				require.Error(t, err)
				require.Equal(t, err.Error(), "expected --proxy URL with http or https scheme")
			},
		},
		{
			desc: "valid --proxy URL without URL scheme",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				proxyAddr:     "proxy-from-flag.example.com",
			},
			wantAddr:  "https://proxy-from-flag.example.com",
			assertErr: require.NoError,
		},
		{
			desc: "invalid --proxy URL",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				proxyAddr:     "1https://proxy-from-flag.example.com",
			},
			assertErr: func(t require.TestingT, err error, _ ...interface{}) {
				require.Error(t, err)
				require.Contains(t, err.Error(), "specified --proxy URL is invalid")
			},
		},
		{
			desc: "k8s proxy running locally with public_addr",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				config: &service.Config{Proxy: service.ProxyConfig{Kube: service.KubeProxyConfig{
					Enabled:     true,
					PublicAddrs: []utils.NetAddr{{Addr: "proxy-from-config.example.com:3026"}},
				}}},
			},
			wantAddr:  "https://proxy-from-config.example.com:3026",
			assertErr: require.NoError,
		},
		{
			desc: "k8s proxy running locally without public_addr",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				config: &service.Config{Proxy: service.ProxyConfig{
					Kube: service.KubeProxyConfig{
						Enabled: true,
					},
					PublicAddrs: []utils.NetAddr{{Addr: "proxy-from-config.example.com:3080"}},
				}},
			},
			wantAddr:  "https://proxy-from-config.example.com:3026",
			assertErr: require.NoError,
		},
		{
			desc: "k8s proxy from cluster info",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				config: &service.Config{Proxy: service.ProxyConfig{
					Kube: service.KubeProxyConfig{
						Enabled: false,
					},
				}},
			},
			wantAddr:  "https://proxy-from-api.example.com:3026",
			assertErr: require.NoError,
		},
		{
			desc: "--kube-cluster specified with valid cluster",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				leafCluster:   remoteCluster.GetMetadata().Name,
				config: &service.Config{Proxy: service.ProxyConfig{
					Kube: service.KubeProxyConfig{
						Enabled: false,
					},
				}},
			},
			wantCluster: remoteCluster.GetMetadata().Name,
			assertErr:   require.NoError,
		},
		{
			desc: "--kube-cluster specified with invalid cluster",
			ac: AuthCommand{
				output:        filepath.Join(tmpDir, "kubeconfig"),
				outputFormat:  identityfile.FormatKubernetes,
				signOverwrite: true,
				leafCluster:   "doesnotexist.example.com",
				config: &service.Config{Proxy: service.ProxyConfig{
					Kube: service.KubeProxyConfig{
						Enabled: false,
					},
				}},
			},
			assertErr: func(t require.TestingT, err error, _ ...interface{}) {
				require.Error(t, err)
				require.Equal(t, err.Error(), "couldn't find leaf cluster named \"doesnotexist.example.com\"")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			// Generate kubeconfig.
			err := tt.ac.generateUserKeys(context.Background(), client)
			tt.assertErr(t, err)

			// Validate kubeconfig contents.
			kc, err := kubeconfig.Load(tt.ac.output)
			if err != nil {
				t.Fatalf("loading generated kubeconfig: %v", err)
			}
			currentCtx, ok := kc.Contexts[kc.CurrentContext]
			if !ok {
				t.Fatalf("currentContext %q not present in kubeconfig", kc.CurrentContext)
			}
			gotCert := kc.AuthInfos[currentCtx.AuthInfo].ClientCertificateData
			if !bytes.Equal(gotCert, client.userCerts.TLS) {
				t.Errorf("got client cert: %q, want %q", gotCert, client.userCerts.TLS)
			}
			gotCA := kc.Clusters[currentCtx.Cluster].CertificateAuthorityData
			wantCA := ca.GetActiveKeys().TLS[0].Cert
			if !bytes.Equal(gotCA, wantCA) {
				t.Errorf("got CA cert: %q, want %q", gotCA, wantCA)
			}
			gotServerAddr := kc.Clusters[currentCtx.Cluster].Server
			if tt.wantAddr != "" && gotServerAddr != tt.wantAddr {
				t.Errorf("got server address: %q, want %q", gotServerAddr, tt.wantAddr)
			}
			if tt.wantCluster != "" && kc.CurrentContext != tt.wantCluster {
				t.Errorf("got cluster: %q, want %q", kc.CurrentContext, tt.wantCluster)
			}
		})
	}
}

type mockClient struct {
	auth.ClientI

	clusterName    types.ClusterName
	userCerts      *proto.Certs
	userCertsReq   *proto.UserCertsRequest
	dbCertsReq     *proto.DatabaseCertRequest
	dbCerts        *proto.DatabaseCertResponse
	cas            []types.CertAuthority
	proxies        []types.Server
	remoteClusters []types.RemoteCluster
	kubeServices   []types.KubeServer
	appServices    []types.AppServer
	dbServices     []types.DatabaseServer
	appSession     types.WebSession
	networkConfig  types.ClusterNetworkingConfig
}

func (c *mockClient) GetClusterName(...services.MarshalOption) (types.ClusterName, error) {
	return c.clusterName, nil
}

func (c *mockClient) GetClusterNetworkingConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterNetworkingConfig, error) {
	if c.networkConfig == nil {
		return &types.ClusterNetworkingConfigV2{}, nil
	}
	return c.networkConfig, nil
}

func (c *mockClient) GenerateUserCerts(ctx context.Context, userCertsReq proto.UserCertsRequest) (*proto.Certs, error) {
	c.userCertsReq = &userCertsReq
	return c.userCerts, nil
}

func (c *mockClient) GetCertAuthority(ctx context.Context, id types.CertAuthID, loadSigningKeys bool, opts ...services.MarshalOption) (types.CertAuthority, error) {
	for _, v := range c.cas {
		if v.GetType() == id.Type && v.GetClusterName() == id.DomainName {
			return v, nil
		}
	}
	return nil, trace.NotFound("not found")
}

func (c *mockClient) GetCertAuthorities(context.Context, types.CertAuthType, bool, ...services.MarshalOption) ([]types.CertAuthority, error) {
	return c.cas, nil
}

func (c *mockClient) GetProxies() ([]types.Server, error) {
	return c.proxies, nil
}

func (c *mockClient) GetRemoteClusters(opts ...services.MarshalOption) ([]types.RemoteCluster, error) {
	return c.remoteClusters, nil
}

func (c *mockClient) GetKubernetesServers(context.Context) ([]types.KubeServer, error) {
	return c.kubeServices, nil
}

func (c *mockClient) GenerateDatabaseCert(ctx context.Context, req *proto.DatabaseCertRequest) (*proto.DatabaseCertResponse, error) {
	c.dbCertsReq = req
	return c.dbCerts, nil
}

func (c *mockClient) GetApplicationServers(context.Context, string) ([]types.AppServer, error) {
	return c.appServices, nil
}

func (c *mockClient) CreateAppSession(ctx context.Context, req types.CreateAppSessionRequest) (types.WebSession, error) {
	return c.appSession, nil
}

func (c *mockClient) GetDatabaseServers(context.Context, string, ...services.MarshalOption) ([]types.DatabaseServer, error) {
	return c.dbServices, nil
}

func TestCheckKubeCluster(t *testing.T) {
	const teleportCluster = "local-teleport"
	clusterName, err := services.NewClusterNameWithRandomID(types.ClusterNameSpecV2{
		ClusterName: teleportCluster,
	})
	require.NoError(t, err)
	client := &mockClient{
		clusterName: clusterName,
	}
	tests := []struct {
		desc               string
		kubeCluster        string
		leafCluster        string
		outputFormat       identityfile.Format
		registeredClusters []*types.KubernetesClusterV3
		want               string
		assertErr          require.ErrorAssertionFunc
	}{
		{
			desc:         "non-k8s output format",
			outputFormat: identityfile.FormatFile,
			assertErr:    require.NoError,
		},
		{
			desc:               "local cluster, valid kube cluster",
			kubeCluster:        "foo",
			leafCluster:        teleportCluster,
			registeredClusters: []*types.KubernetesClusterV3{{Metadata: types.Metadata{Name: "foo"}}},
			outputFormat:       identityfile.FormatKubernetes,
			want:               "foo",
			assertErr:          require.NoError,
		},
		{
			desc:               "local cluster, empty kube cluster",
			kubeCluster:        "",
			leafCluster:        teleportCluster,
			registeredClusters: []*types.KubernetesClusterV3{{Metadata: types.Metadata{Name: "foo"}}},
			outputFormat:       identityfile.FormatKubernetes,
			want:               "foo",
			assertErr:          require.NoError,
		},
		{
			desc:               "local cluster, empty kube cluster, no registered kube clusters",
			kubeCluster:        "",
			leafCluster:        teleportCluster,
			registeredClusters: []*types.KubernetesClusterV3{},
			outputFormat:       identityfile.FormatKubernetes,
			want:               "",
			assertErr:          require.NoError,
		},
		{
			desc:               "local cluster, invalid kube cluster",
			kubeCluster:        "bar",
			leafCluster:        teleportCluster,
			registeredClusters: []*types.KubernetesClusterV3{{Metadata: types.Metadata{Name: "foo"}}},
			outputFormat:       identityfile.FormatKubernetes,
			assertErr:          require.Error,
		},
		{
			desc:               "remote cluster, empty kube cluster",
			kubeCluster:        "",
			leafCluster:        "remote-teleport",
			registeredClusters: []*types.KubernetesClusterV3{{Metadata: types.Metadata{Name: "foo"}}},
			outputFormat:       identityfile.FormatKubernetes,
			want:               "",
			assertErr:          require.NoError,
		},
		{
			desc:               "remote cluster, non-empty kube cluster",
			kubeCluster:        "bar",
			leafCluster:        "remote-teleport",
			registeredClusters: []*types.KubernetesClusterV3{{Metadata: types.Metadata{Name: "foo"}}},
			outputFormat:       identityfile.FormatKubernetes,
			want:               "bar",
			assertErr:          require.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			client.kubeServices = []types.KubeServer{}
			for _, kube := range tt.registeredClusters {
				client.kubeServices = append(client.kubeServices, &types.KubernetesServerV3{
					Metadata: types.Metadata{
						Name: kube.GetName(),
					},
					Spec: types.KubernetesServerSpecV3{
						Hostname: "host",
						Cluster:  kube,
					},
				})
			}
			a := &AuthCommand{
				kubeCluster:  tt.kubeCluster,
				leafCluster:  tt.leafCluster,
				outputFormat: tt.outputFormat,
			}
			err := a.checkKubeCluster(context.Background(), client)
			tt.assertErr(t, err)
			require.Equal(t, tt.want, a.kubeCluster)
		})
	}
}

// TestGenerateDatabaseKeys verifies cert/key pair generation for databases.
func TestGenerateDatabaseKeys(t *testing.T) {
	clusterName, err := services.NewClusterNameWithRandomID(
		types.ClusterNameSpecV2{
			ClusterName: "example.com",
		})
	require.NoError(t, err)

	certBytes := []byte("TLS cert")
	caBytes := []byte("CA cert")

	authClient := &mockClient{
		clusterName: clusterName,
		dbCerts: &proto.DatabaseCertResponse{
			Cert:    certBytes,
			CACerts: [][]byte{caBytes},
		},
	}

	key, err := client.GenerateRSAKey()
	require.NoError(t, err)

	tests := []struct {
		name           string
		inFormat       identityfile.Format
		inHost         string
		inOutDir       string
		inOutFile      string
		outSubject     pkix.Name
		outServerNames []string
		outKeyFile     string
		outCertFile    string
		outCAFile      string
		outKey         []byte
		outCert        []byte
		outCA          []byte
		genKeyErrMsg   string
	}{
		{
			name:           "database certificate",
			inFormat:       identityfile.FormatDatabase,
			inHost:         "postgres.example.com",
			inOutDir:       t.TempDir(),
			inOutFile:      "db",
			outSubject:     pkix.Name{CommonName: "postgres.example.com"},
			outServerNames: []string{"postgres.example.com"},
			outKeyFile:     "db.key",
			outCertFile:    "db.crt",
			outCAFile:      "db.cas",
			outKey:         key.PrivateKeyPEM(),
			outCert:        certBytes,
			outCA:          caBytes,
		},
		{
			name:           "database certificate multiple SANs",
			inFormat:       identityfile.FormatDatabase,
			inHost:         "mysql.external.net,mysql.internal.net,192.168.1.1",
			inOutDir:       t.TempDir(),
			inOutFile:      "db",
			outSubject:     pkix.Name{CommonName: "mysql.external.net"},
			outServerNames: []string{"mysql.external.net", "mysql.internal.net", "192.168.1.1"},
			outKeyFile:     "db.key",
			outCertFile:    "db.crt",
			outCAFile:      "db.cas",
			outKey:         key.PrivateKeyPEM(),
			outCert:        certBytes,
			outCA:          caBytes,
		},
		{
			name:           "mongodb certificate",
			inFormat:       identityfile.FormatMongo,
			inHost:         "mongo.example.com",
			inOutDir:       t.TempDir(),
			inOutFile:      "mongo",
			outSubject:     pkix.Name{CommonName: "mongo.example.com", Organization: []string{"example.com"}},
			outServerNames: []string{"mongo.example.com"},
			outCertFile:    "mongo.crt",
			outCAFile:      "mongo.cas",
			outCert:        append(certBytes, key.PrivateKeyPEM()...),
			outCA:          caBytes,
		},
		{
			name:           "cockroachdb certificate",
			inFormat:       identityfile.FormatCockroach,
			inHost:         "localhost,roach1",
			inOutDir:       t.TempDir(),
			outSubject:     pkix.Name{CommonName: "node"},
			outServerNames: []string{"node", "localhost", "roach1"}, // "node" principal should always be added
			outKeyFile:     "node.key",
			outCertFile:    "node.crt",
			outCAFile:      "ca.crt",
			outKey:         key.PrivateKeyPEM(),
			outCert:        certBytes,
			outCA:          caBytes,
		},
		{
			name:           "redis certificate",
			inFormat:       identityfile.FormatRedis,
			inHost:         "localhost,redis1,172.0.0.1",
			inOutDir:       t.TempDir(),
			inOutFile:      "db",
			outSubject:     pkix.Name{CommonName: "localhost"},
			outServerNames: []string{"localhost", "redis1", "172.0.0.1"},
			outKeyFile:     "db.key",
			outCertFile:    "db.crt",
			outCAFile:      "db.cas",
			outKey:         key.PrivateKeyPEM(),
			outCert:        certBytes,
			outCA:          caBytes,
		},
		{
			name:         "missing host",
			inFormat:     identityfile.FormatRedis,
			inOutDir:     t.TempDir(),
			inHost:       "", // missing host
			inOutFile:    "db",
			genKeyErrMsg: "at least one hostname must be specified",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ac := AuthCommand{
				output:        filepath.Join(test.inOutDir, test.inOutFile),
				outputFormat:  test.inFormat,
				signOverwrite: true,
				genHost:       test.inHost,
				genTTL:        time.Hour,
			}

			err = ac.generateDatabaseKeysForKey(context.Background(), authClient, key)
			if test.genKeyErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.genKeyErrMsg)
				return
			}

			require.NotNil(t, authClient.dbCertsReq)
			csr, err := tlsca.ParseCertificateRequestPEM(authClient.dbCertsReq.CSR)
			require.NoError(t, err)
			require.Equal(t, test.outSubject.String(), csr.Subject.String())
			require.Equal(t, test.outServerNames, authClient.dbCertsReq.ServerNames)
			require.Equal(t, test.outServerNames[0], authClient.dbCertsReq.ServerName)

			if len(test.outKey) > 0 {
				keyBytes, err := os.ReadFile(filepath.Join(test.inOutDir, test.outKeyFile))
				require.NoError(t, err)
				require.Equal(t, test.outKey, keyBytes, "keys match")
			}

			if len(test.outCert) > 0 {
				certBytes, err := os.ReadFile(filepath.Join(test.inOutDir, test.outCertFile))
				require.NoError(t, err)
				require.Equal(t, test.outCert, certBytes, "certificates match")
			}

			if len(test.outCA) > 0 {
				caBytes, err := os.ReadFile(filepath.Join(test.inOutDir, test.outCAFile))
				require.NoError(t, err)
				require.Equal(t, test.outCA, caBytes, "CA certificates match")
			}
		})
	}
}

// TestGenerateAppCertificates verifies cert/key pair generation for applications.
func TestGenerateAppCertificates(t *testing.T) {
	const appName = "app-1"
	const clusterNameStr = "example.com"
	const publicAddr = "https://app-1.example.com"
	const sessionID = "foobar"

	clusterName, err := services.NewClusterNameWithRandomID(
		types.ClusterNameSpecV2{
			ClusterName: clusterNameStr,
		})
	require.NoError(t, err)

	authClient := &mockClient{
		clusterName: clusterName,
		userCerts: &proto.Certs{
			SSH: []byte("SSH cert"),
			TLS: []byte("TLS cert"),
		},
		appServices: []types.AppServer{
			&types.AppServerV3{
				Metadata: types.Metadata{
					Name: appName,
				},
				Spec: types.AppServerSpecV3{
					App: &types.AppV3{
						Spec: types.AppSpecV3{
							PublicAddr: publicAddr,
						},
					},
				},
			},
		},
		appSession: &types.WebSessionV2{
			Metadata: types.Metadata{
				Name: sessionID,
			},
		},
	}

	tests := []struct {
		name        string
		outDir      string
		outFileBase string
		appName     string
		assertErr   require.ErrorAssertionFunc
	}{
		{
			name:        "app happy path",
			outDir:      t.TempDir(),
			outFileBase: "app-1",
			appName:     "app-1",
			assertErr:   require.NoError,
		},
		{
			name:        "app non-existent",
			outDir:      t.TempDir(),
			outFileBase: "app-2",
			appName:     "app-2",
			assertErr: func(t require.TestingT, err error, _ ...interface{}) {
				require.Error(t, err)
				require.True(t, trace.IsNotFound(err))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(tc.outDir, tc.outFileBase)
			ac := AuthCommand{
				output:        output,
				outputFormat:  identityfile.FormatTLS,
				signOverwrite: true,
				genTTL:        time.Hour,
				appName:       tc.appName,
			}
			err = ac.generateUserKeys(context.Background(), authClient)
			tc.assertErr(t, err)
			if err != nil {
				return
			}

			expectedRouteToApp := proto.RouteToApp{
				Name:        tc.appName,
				SessionID:   sessionID,
				PublicAddr:  publicAddr,
				ClusterName: clusterNameStr,
			}
			require.Equal(t, proto.UserCertsRequest_App, authClient.userCertsReq.Usage)
			require.Equal(t, expectedRouteToApp, authClient.userCertsReq.RouteToApp)

			certBytes, err := os.ReadFile(filepath.Join(tc.outDir, tc.outFileBase+".crt"))
			require.NoError(t, err)
			require.Equal(t, authClient.userCerts.TLS, certBytes, "certificates match")
		})
	}
}

func TestGenerateDatabaseUserCertificates(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		clusterName        string
		dbService          string
		dbName             string
		dbUser             string
		expectedDbProtocol string
		dbServices         []types.DatabaseServer
		expectedErr        error
	}{
		"DatabaseExists": {
			clusterName:        "example.com",
			dbService:          "db-1",
			expectedDbProtocol: defaults.ProtocolPostgres,
			dbServices: []types.DatabaseServer{
				&types.DatabaseServerV3{
					Metadata: types.Metadata{
						Name: "db-1",
					},
					Spec: types.DatabaseServerSpecV3{
						Hostname: "example.com",
						Database: &types.DatabaseV3{
							Spec: types.DatabaseSpecV3{
								Protocol: defaults.ProtocolPostgres,
							},
						},
					},
				},
			},
		},
		"DatabaseWithUserExists": {
			clusterName:        "example.com",
			dbService:          "db-user-1",
			dbUser:             "mongo-user",
			expectedDbProtocol: defaults.ProtocolMongoDB,
			dbServices: []types.DatabaseServer{
				&types.DatabaseServerV3{
					Metadata: types.Metadata{
						Name: "db-user-1",
					},
					Spec: types.DatabaseServerSpecV3{
						Hostname: "example.com",
						Database: &types.DatabaseV3{
							Spec: types.DatabaseSpecV3{
								Protocol: defaults.ProtocolMongoDB,
							},
						},
					},
				},
			},
		},
		"DatabaseWithDatabaseNameExists": {
			clusterName:        "example.com",
			dbService:          "db-user-1",
			dbName:             "root-database",
			expectedDbProtocol: defaults.ProtocolMongoDB,
			dbServices: []types.DatabaseServer{
				&types.DatabaseServerV3{
					Metadata: types.Metadata{
						Name: "db-user-1",
					},
					Spec: types.DatabaseServerSpecV3{
						Hostname: "example.com",
						Database: &types.DatabaseV3{
							Spec: types.DatabaseSpecV3{
								Protocol: defaults.ProtocolMongoDB,
							},
						},
					},
				},
			},
		},
		"DatabaseNotFound": {
			clusterName: "example.com",
			dbService:   "db-2",
			dbServices:  []types.DatabaseServer{},
			expectedErr: trace.NotFound(""),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			clusterName, err := services.NewClusterNameWithRandomID(
				types.ClusterNameSpecV2{
					ClusterName: test.clusterName,
				})
			require.NoError(t, err)

			authClient := &mockClient{
				clusterName: clusterName,
				userCerts: &proto.Certs{
					SSH: []byte("SSH cert"),
					TLS: []byte("TLS cert"),
				},
				dbServices: test.dbServices,
			}

			certsDir := t.TempDir()
			output := filepath.Join(certsDir, test.dbService)
			ac := AuthCommand{
				output:        output,
				outputFormat:  identityfile.FormatTLS,
				signOverwrite: true,
				genTTL:        time.Hour,
				dbService:     test.dbService,
				dbName:        test.dbName,
				dbUser:        test.dbUser,
			}

			err = ac.generateUserKeys(ctx, authClient)
			if test.expectedErr != nil {
				require.Error(t, err)
				require.IsType(t, test.expectedErr, err)
				return
			}

			require.NoError(t, err)

			expectedRouteToDatabase := proto.RouteToDatabase{
				ServiceName: test.dbService,
				Protocol:    test.expectedDbProtocol,
				Database:    test.dbName,
				Username:    test.dbUser,
			}
			require.Equal(t, proto.UserCertsRequest_Database, authClient.userCertsReq.Usage)
			require.Equal(t, expectedRouteToDatabase, authClient.userCertsReq.RouteToDatabase)

			certBytes, err := os.ReadFile(filepath.Join(certsDir, test.dbService+".crt"))
			require.NoError(t, err)
			require.Equal(t, authClient.userCerts.TLS, certBytes, "certificates match")
		})
	}
}

func TestGenerateAndSignKeys(t *testing.T) {
	clusterName, err := services.NewClusterNameWithRandomID(
		types.ClusterNameSpecV2{
			ClusterName: "example.com",
		})
	require.NoError(t, err)

	_, cert, err := tlsca.GenerateSelfSignedCA(pkix.Name{CommonName: "example.com"}, nil, time.Minute)
	require.NoError(t, err)
	firstCA, err := types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:        types.DatabaseCA,
		ClusterName: "example.com",
		ActiveKeys: types.CAKeySet{
			SSH: []*types.SSHKeyPair{{PublicKey: []byte("SSH CA cert")}},
			TLS: []*types.TLSKeyPair{{Cert: cert}},
		},
	})
	require.NoError(t, err)

	secondCA, err := types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:        types.DatabaseCA,
		ClusterName: "leaf.example.com",
		ActiveKeys: types.CAKeySet{
			SSH: []*types.SSHKeyPair{{PublicKey: []byte("SSH CA cert")}},
			TLS: []*types.TLSKeyPair{{Cert: cert}},
		},
	})
	require.NoError(t, err)
	certBytes := []byte("TLS cert")
	caBytes := []byte("CA cert")

	authClient := &mockClient{
		clusterName: clusterName,
		dbCerts: &proto.DatabaseCertResponse{
			Cert:    certBytes,
			CACerts: [][]byte{caBytes},
		},
		cas: []types.CertAuthority{firstCA, secondCA},
	}

	tests := []struct {
		name      string
		inFormat  identityfile.Format
		inHost    string
		inOutDir  string
		inOutFile string
	}{
		{
			name:      "snowflake format",
			inFormat:  identityfile.FormatSnowflake,
			inOutDir:  t.TempDir(),
			inOutFile: "server",
		},
		{
			name:      "db format",
			inFormat:  identityfile.FormatDatabase,
			inOutDir:  t.TempDir(),
			inOutFile: "server",
			inHost:    "localhost",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ac := AuthCommand{
				output:        filepath.Join(test.inOutDir, test.inOutFile),
				outputFormat:  test.inFormat,
				signOverwrite: true,
				genHost:       test.inHost,
				genTTL:        time.Hour,
			}

			err = ac.GenerateAndSignKeys(context.Background(), authClient)
			require.NoError(t, err)
		})
	}
}
