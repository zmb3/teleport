/*
Copyright 2015-2017 Gravitational, Inc.

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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/constants"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/lib"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/fixtures"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

// TestDatabaseLogin tests "tsh db login" command and verifies "tsh db
// env/config" after login.
func TestDatabaseLogin(t *testing.T) {
	tmpHomePath := t.TempDir()

	connector := mockConnector(t)

	alice, err := types.NewUser("alice@example.com")
	require.NoError(t, err)
	alice.SetRoles([]string{"access"})

	authProcess, proxyProcess := makeTestServers(t, withBootstrap(connector, alice))
	makeTestDatabaseServer(t, authProcess, proxyProcess, service.Database{
		Name:     "postgres",
		Protocol: defaults.ProtocolPostgres,
		URI:      "localhost:5432",
	}, service.Database{
		Name:     "mongo",
		Protocol: defaults.ProtocolMongoDB,
		URI:      "localhost:27017",
	}, service.Database{
		Name:     "mssql",
		Protocol: defaults.ProtocolSQLServer,
		URI:      "localhost:1433",
	})

	authServer := authProcess.GetAuthServer()
	require.NotNil(t, authServer)

	proxyAddr, err := proxyProcess.ProxyWebAddr()
	require.NoError(t, err)

	// Log into Teleport cluster.
	err = Run(context.Background(), []string{
		"login", "--insecure", "--debug", "--auth", connector.GetName(), "--proxy", proxyAddr.String(),
	}, setHomePath(tmpHomePath), cliOption(func(cf *CLIConf) error {
		cf.mockSSOLogin = mockSSOLogin(t, authServer, alice)
		return nil
	}))
	require.NoError(t, err)

	testCases := []struct {
		databaseName          string
		expectCertsLen        int
		expectKeysLen         int
		expectErrForConfigCmd bool
		expectErrForEnvCmd    bool
	}{
		{
			databaseName:   "postgres",
			expectCertsLen: 1,
		},
		{
			databaseName:       "mongo",
			expectCertsLen:     1,
			expectKeysLen:      1,
			expectErrForEnvCmd: true, // "tsh db env" not supported for Mongo.
		},
		{
			databaseName:          "mssql",
			expectCertsLen:        1,
			expectErrForConfigCmd: true, // "tsh db config" not supported for MSSQL.
			expectErrForEnvCmd:    true, // "tsh db env" not supported for MSSQL.
		},
	}

	// Note: keystore currently races when multiple tsh clients work in the
	// same profile dir (e.g. StatusCurrent might fail reading if someone else
	// is writing a key at the same time). Thus running all `tsh db login` in
	// sequence first before running other test cases in parallel.
	for _, test := range testCases {
		t.Run(fmt.Sprintf("%v/%v", "tsh db login", test.databaseName), func(t *testing.T) {
			err := Run(context.Background(), []string{
				"db", "login", "--db-user", "admin", test.databaseName,
			}, setHomePath(tmpHomePath))
			require.NoError(t, err)

			// Fetch the active profile.
			profile, err := client.StatusFor(tmpHomePath, proxyAddr.Host(), alice.GetName())
			require.NoError(t, err)

			// Verify certificates.
			certs, keys, err := decodePEM(profile.DatabaseCertPathForCluster("", test.databaseName))
			require.NoError(t, err)
			require.Len(t, certs, test.expectCertsLen)
			require.Len(t, keys, test.expectKeysLen)
		})
	}

	for _, test := range testCases {
		test := test

		t.Run(fmt.Sprintf("%v/%v", "tsh db config", test.databaseName), func(t *testing.T) {
			t.Parallel()

			err := Run(context.Background(), []string{
				"db", "config", test.databaseName,
			}, setHomePath(tmpHomePath))

			if test.expectErrForConfigCmd {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})

		t.Run(fmt.Sprintf("%v/%v", "tsh db env", test.databaseName), func(t *testing.T) {
			t.Parallel()

			err := Run(context.Background(), []string{
				"db", "env", test.databaseName,
			}, setHomePath(tmpHomePath))

			if test.expectErrForEnvCmd {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestListDatabase(t *testing.T) {
	lib.SetInsecureDevMode(true)
	defer lib.SetInsecureDevMode(false)

	s := newTestSuite(t,
		withRootConfigFunc(func(cfg *service.Config) {
			cfg.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
			cfg.Databases.Enabled = true
			cfg.Databases.Databases = []service.Database{{
				Name:     "root-postgres",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
			}}
		}),
		withLeafCluster(),
		withLeafConfigFunc(func(cfg *service.Config) {
			cfg.Databases.Enabled = true
			cfg.Databases.Databases = []service.Database{{
				Name:     "leaf-postgres",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
			}}
		}),
	)

	mustLoginSetEnv(t, s)

	captureStdout := new(bytes.Buffer)
	err := Run(context.Background(), []string{
		"db",
		"ls",
		"--insecure",
		"--debug",
	}, func(cf *CLIConf) error {
		cf.overrideStdout = io.MultiWriter(os.Stdout, captureStdout)
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, captureStdout.String(), "root-postgres")

	captureStdout.Reset()
	err = Run(context.Background(), []string{
		"db",
		"ls",
		"--cluster",
		"leaf1",
		"--insecure",
		"--debug",
	}, func(cf *CLIConf) error {
		cf.overrideStdout = io.MultiWriter(os.Stdout, captureStdout)
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, captureStdout.String(), "leaf-postgres")
}

func TestFormatDatabaseListCommand(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		require.Equal(t, "tsh db ls", formatDatabaseListCommand(""))
	})

	t.Run("with cluster flag", func(t *testing.T) {
		require.Equal(t, "tsh db ls --cluster=leaf", formatDatabaseListCommand("leaf"))
	})
}

func TestFormatConfigCommand(t *testing.T) {
	db := tlsca.RouteToDatabase{
		ServiceName: "example-db",
	}

	t.Run("default", func(t *testing.T) {
		require.Equal(t, "tsh db config --format=cmd example-db", formatDatabaseConfigCommand("", db))
	})

	t.Run("with cluster flag", func(t *testing.T) {
		require.Equal(t, "tsh db config --cluster=leaf --format=cmd example-db", formatDatabaseConfigCommand("leaf", db))
	})
}

func TestDBInfoHasChanged(t *testing.T) {
	tests := []struct {
		name               string
		databaseUserName   string
		databaseName       string
		db                 tlsca.RouteToDatabase
		wantUserHasChanged bool
	}{
		{
			name:             "empty cli database user flag",
			databaseUserName: "",
			db: tlsca.RouteToDatabase{
				Username: "alice",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: false,
		},
		{
			name:             "different user",
			databaseUserName: "alice",
			db: tlsca.RouteToDatabase{
				Username: "bob",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: true,
		},
		{
			name:             "different user mysql protocol",
			databaseUserName: "alice",
			db: tlsca.RouteToDatabase{
				Username: "bob",
				Protocol: defaults.ProtocolMySQL,
			},
			wantUserHasChanged: true,
		},
		{
			name:             "same user",
			databaseUserName: "bob",
			db: tlsca.RouteToDatabase{
				Username: "bob",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: false,
		},
		{
			name:             "empty cli database user and database name flags",
			databaseUserName: "",
			databaseName:     "",
			db: tlsca.RouteToDatabase{
				Username: "alice",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: false,
		},
		{
			name:             "different database name",
			databaseUserName: "",
			databaseName:     "db1",
			db: tlsca.RouteToDatabase{
				Username: "alice",
				Database: "db2",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: true,
		},
		{
			name:             "same database name",
			databaseUserName: "",
			databaseName:     "db1",
			db: tlsca.RouteToDatabase{
				Username: "alice",
				Database: "db1",
				Protocol: defaults.ProtocolMongoDB,
			},
			wantUserHasChanged: false,
		},
	}

	ca, err := tlsca.FromKeys([]byte(fixtures.TLSCACertPEM), []byte(fixtures.TLSCAKeyPEM))
	require.NoError(t, err)
	privateKey, err := rsa.GenerateKey(rand.Reader, constants.RSAKeySize)
	require.NoError(t, err)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			identity := tlsca.Identity{
				Username:        "user",
				RouteToDatabase: tc.db,
				Groups:          []string{"none"},
			}
			subj, err := identity.Subject()
			require.NoError(t, err)
			certBytes, err := ca.GenerateCertificate(tlsca.CertificateRequest{
				PublicKey: privateKey.Public(),
				Subject:   subj,
				NotAfter:  time.Now().Add(time.Hour),
			})
			require.NoError(t, err)

			certPath := filepath.Join(t.TempDir(), "mongo_db_cert.pem")
			require.NoError(t, os.WriteFile(certPath, certBytes, 0o600))

			cliConf := &CLIConf{DatabaseUser: tc.databaseUserName, DatabaseName: tc.databaseName}
			got, err := dbInfoHasChanged(cliConf, certPath)
			require.NoError(t, err)
			require.Equal(t, tc.wantUserHasChanged, got)
		})
	}
}

func makeTestDatabaseServer(t *testing.T, auth *service.TeleportProcess, proxy *service.TeleportProcess, dbs ...service.Database) (db *service.TeleportProcess) {
	// Proxy uses self-signed certificates in tests.
	lib.SetInsecureDevMode(true)

	cfg := service.MakeDefaultConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	proxyAddr, err := proxy.ProxyWebAddr()
	require.NoError(t, err)

	cfg.SetAuthServerAddress(*proxyAddr)

	token, err := proxy.Config.Token()
	require.NoError(t, err)

	cfg.SetToken(token)
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = false
	cfg.Databases.Enabled = true
	cfg.Databases.Databases = dbs
	cfg.Log = utils.NewLoggerForTests()

	db, err = service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, db.Start())

	t.Cleanup(func() {
		db.Close()
	})

	// Wait for database agent to start.
	_, err = db.WaitForEventTimeout(10*time.Second, service.DatabasesReady)
	require.NoError(t, err, "database server didn't start after 10s")

	// Wait for all databases to register to avoid races.
	waitForDatabases(t, auth, dbs)

	return db
}

func waitForDatabases(t *testing.T, auth *service.TeleportProcess, dbs []service.Database) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		select {
		case <-time.After(500 * time.Millisecond):
			all, err := auth.GetAuthServer().GetDatabaseServers(ctx, apidefaults.Namespace)
			require.NoError(t, err)

			// Count how many input "dbs" are registered.
			var registered int
			for _, db := range dbs {
				for _, a := range all {
					if a.GetName() == db.Name {
						registered++
						break
					}
				}
			}

			if registered == len(dbs) {
				return
			}
		case <-ctx.Done():
			t.Fatal("databases not registered after 10s")
		}
	}
}

// decodePEM sorts out specified PEM file into certificates and private keys.
func decodePEM(pemPath string) (certs []pem.Block, privs []pem.Block, err error) {
	bytes, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	var block *pem.Block
	for {
		block, bytes = pem.Decode(bytes)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certs = append(certs, *block)
		case keys.PKCS1PrivateKeyType:
			privs = append(privs, *block)
		case keys.PKCS8PrivateKeyType:
			privs = append(privs, *block)
		}
	}
	return certs, privs, nil
}

func TestFormatDatabaseConnectArgs(t *testing.T) {
	tests := []struct {
		name      string
		cluster   string
		route     tlsca.RouteToDatabase
		wantFlags []string
	}{
		{
			name:      "match user and db name, cluster set",
			cluster:   "foo",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMongoDB, ServiceName: "svc"},
			wantFlags: []string{"--cluster=foo", "--db-user=<user>", "--db-name=<name>", "svc"},
		},
		{
			name:      "match user and db name",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMongoDB, ServiceName: "svc"},
			wantFlags: []string{"--db-user=<user>", "--db-name=<name>", "svc"},
		},
		{
			name:      "match user and db name, username given",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMongoDB, Username: "bob", ServiceName: "svc"},
			wantFlags: []string{"--db-name=<name>", "svc"},
		},
		{
			name:      "match user and db name, db name given",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMongoDB, Database: "sales", ServiceName: "svc"},
			wantFlags: []string{"--db-user=<user>", "svc"},
		},
		{
			name:      "match user and db name, both given",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMongoDB, Database: "sales", Username: "bob", ServiceName: "svc"},
			wantFlags: []string{"svc"},
		},
		{
			name:      "match user name",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMySQL, ServiceName: "svc"},
			wantFlags: []string{"--db-user=<user>", "svc"},
		},
		{
			name:      "match user name, given",
			cluster:   "",
			route:     tlsca.RouteToDatabase{Protocol: defaults.ProtocolMySQL, Username: "bob", ServiceName: "svc"},
			wantFlags: []string{"svc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := formatDatabaseConnectArgs(tt.cluster, tt.route)
			require.Equal(t, tt.wantFlags, out)
		})
	}
}
