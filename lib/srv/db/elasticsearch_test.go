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

package db

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/defaults"
	libevents "github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/srv/db/common"
	"github.com/zmb3/teleport/lib/srv/db/elasticsearch"
)

func init() {
	// Override Elasticsearch engine that is used normally with the test one
	// with custom HTTP client.
	common.RegisterEngine(newTestElasticsearchEngine, defaults.ProtocolElasticsearch)
}

func newTestElasticsearchEngine(ec common.EngineConfig) common.Engine {
	return &elasticsearch.Engine{
		EngineConfig: ec,
	}
}

func TestAccessElasticsearch(t *testing.T) {
	ctx := context.Background()
	testCtx := setupTestContext(ctx, t, withElasticsearch("Elasticsearch"))
	go testCtx.startHandlingConnections()

	tests := []struct {
		desc         string
		user         string
		role         string
		allowDbUsers []string
		dbUser       string
		err          bool
	}{
		{
			desc:         "has access to all database names and users",
			user:         "alice",
			role:         "admin",
			allowDbUsers: []string{types.Wildcard},
			dbUser:       "Elasticsearch",
		},
		{
			desc:         "has access to nothing",
			user:         "alice",
			role:         "admin",
			allowDbUsers: []string{},
			dbUser:       "Elasticsearch",
			err:          true,
		},
		{
			desc:         "no access to users",
			user:         "alice",
			role:         "admin",
			allowDbUsers: []string{},
			dbUser:       "Elasticsearch",
			err:          true,
		},
		{
			desc:         "access allowed to specific user/database",
			user:         "alice",
			role:         "admin",
			allowDbUsers: []string{"alice"},
			dbUser:       "alice",
		},
		{
			desc:         "access denied to specific user/database",
			user:         "alice",
			role:         "admin",
			allowDbUsers: []string{"alice"},
			dbUser:       "",
			err:          true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			// Create user/role with the requested permissions.
			testCtx.createUserAndRole(ctx, t, test.user, test.role, test.allowDbUsers, []string{})

			// Try to connect to the database as this user.
			dbConn, proxy, err := testCtx.elasticsearchClient(ctx, test.user, "Elasticsearch", test.dbUser)

			t.Cleanup(func() {
				proxy.Close()
			})

			require.NoError(t, err)

			// Execute a query.
			result, err := dbConn.SQL.Query(strings.NewReader(`{ "query": "SELECT 42" }`))
			require.NoError(t, err)

			if test.err {
				t.Logf("result: %v", result)
				require.True(t, result.IsError())
				require.Equal(t, 401, result.StatusCode)
				return
			}
			require.NoError(t, err)
			require.False(t, result.IsError())
			require.False(t, result.HasWarnings())
			require.Equal(t, `[200 OK] {"columns":[{"name":"42","type":"integer"}],"rows":[[42]]}`, result.String())

			require.NoError(t, result.Body.Close())
		})
	}
}

func TestAuditElasticsearch(t *testing.T) {
	ctx := context.Background()
	testCtx := setupTestContext(ctx, t, withElasticsearch("Elasticsearch"))
	go testCtx.startHandlingConnections()

	testCtx.createUserAndRole(ctx, t, "alice", "admin", []string{"admin"}, []string{types.Wildcard})

	t.Run("access denied", func(t *testing.T) {
		// Access denied should trigger an unsuccessful session start event.
		dbConn, proxy, err := testCtx.elasticsearchClient(ctx, "alice", "Elasticsearch", "notadmin")
		require.NoError(t, err)

		resp, err := dbConn.Ping()

		require.NoError(t, err)
		require.True(t, resp.IsError())

		waitForEvent(t, testCtx, libevents.DatabaseSessionStartFailureCode)
		proxy.Close()
	})

	dbConn, proxy, err := testCtx.elasticsearchClient(ctx, "alice", "Elasticsearch", "admin")
	require.NoError(t, err)

	t.Cleanup(func() {
		if proxy != nil {
			proxy.Close()
		}
	})

	t.Run("session starts event", func(t *testing.T) {
		// Connect should trigger successful session start event.
		resp, err := dbConn.Ping()
		require.NoError(t, err)
		require.False(t, resp.IsError())
		waitForEvent(t, testCtx, libevents.DatabaseSessionStartCode)
	})

	t.Run("command sends", func(t *testing.T) {
		// should trigger Query event.
		result, err := dbConn.SQL.Query(strings.NewReader(`{ "query": "SELECT 42" }`))
		require.NoError(t, err)
		require.Equal(t, `[200 OK] {"columns":[{"name":"42","type":"integer"}],"rows":[[42]]}`, result.String())

		_ = waitForEvent(t, testCtx, libevents.ElasticsearchRequestCode)

		// actual query
		ev := waitForEvent(t, testCtx, libevents.ElasticsearchRequestCode)
		require.Equal(t, "/_sql", ev.(*events.ElasticsearchRequest).Path)
		require.Equal(t, []byte(`{ "query": "SELECT 42" }`), ev.(*events.ElasticsearchRequest).Body)
	})
}

func withElasticsearch(name string, opts ...elasticsearch.TestServerOption) withDatabaseOption {
	return func(t *testing.T, ctx context.Context, testCtx *testContext) types.Database {
		ElasticsearchServer, err := elasticsearch.NewTestServer(common.TestServerConfig{
			Name:       name,
			AuthClient: testCtx.authClient,
			ClientAuth: tls.RequireAndVerifyClientCert,
		}, opts...)
		require.NoError(t, err)
		go ElasticsearchServer.Serve()
		t.Cleanup(func() { ElasticsearchServer.Close() })
		database, err := types.NewDatabaseV3(types.Metadata{
			Name: name,
		}, types.DatabaseSpecV3{
			Protocol:      defaults.ProtocolElasticsearch,
			URI:           net.JoinHostPort("localhost", ElasticsearchServer.Port()),
			DynamicLabels: dynamicLabels,
		})
		require.NoError(t, err)
		testCtx.elasticsearch[name] = testElasticsearch{
			db:       ElasticsearchServer,
			resource: database,
		}
		return database
	}
}
