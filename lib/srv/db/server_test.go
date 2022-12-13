/*
Copyright 2020 Gravitational, Inc.

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	"github.com/jackc/pgconn"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/limiter"
)

// TestDatabaseServerStart validates that started database server updates its
// dynamic labels and heartbeats its presence to the auth server.
func TestDatabaseServerStart(t *testing.T) {
	ctx := context.Background()
	testCtx := setupTestContext(ctx, t,
		withSelfHostedPostgres("postgres"),
		withSelfHostedMySQL("mysql"),
		withSelfHostedMongo("mongo"),
		withSelfHostedRedis("redis"))

	tests := []struct {
		database types.Database
	}{
		{database: testCtx.postgres["postgres"].resource},
		{database: testCtx.mysql["mysql"].resource},
		{database: testCtx.mongo["mongo"].resource},
		{database: testCtx.redis["redis"].resource},
	}

	for _, test := range tests {
		labels := testCtx.server.getDynamicLabels(test.database.GetName())
		require.NotNil(t, labels)
		require.Equal(t, "test", labels.Get()["echo"].GetResult())
	}

	// Make sure servers were announced and their labels updated.
	servers, err := testCtx.authClient.GetDatabaseServers(ctx, apidefaults.Namespace)
	require.NoError(t, err)
	require.Len(t, servers, 4)
	for _, server := range servers {
		require.Equal(t, map[string]string{"echo": "test"},
			server.GetDatabase().GetAllLabels())
	}
}

func TestDatabaseServerLimiting(t *testing.T) {
	const (
		user      = "bob"
		role      = "admin"
		dbName    = "postgres"
		dbUser    = user
		connLimit = int64(5) // Arbitrary number
	)

	ctx := context.Background()
	allowDbUsers := []string{types.Wildcard}
	allowDbNames := []string{types.Wildcard}

	testCtx := setupTestContext(ctx, t,
		withSelfHostedPostgres("postgres"),
		withSelfHostedMySQL("mysql"),
		withSelfHostedMongo("mongo"),
	)

	connLimiter, err := limiter.NewLimiter(limiter.Config{MaxConnections: connLimit})
	require.NoError(t, err)

	// Set connection limit
	testCtx.server.cfg.Limiter = connLimiter

	go testCtx.startHandlingConnections()
	t.Cleanup(func() {
		err := testCtx.Close()
		require.NoError(t, err)
	})

	// Create user/role with the requested permissions.
	testCtx.createUserAndRole(ctx, t, user, role, allowDbUsers, allowDbNames)

	t.Run("postgres", func(t *testing.T) {
		dbConns := make([]*pgconn.PgConn, 0)
		t.Cleanup(func() {
			// Disconnect all clients.
			for _, pgConn := range dbConns {
				err = pgConn.Close(ctx)
				require.NoError(t, err)
			}
		})

		// Connect the maximum allowed number of clients.
		for i := int64(0); i < connLimit; i++ {
			pgConn, err := testCtx.postgresClient(ctx, user, "postgres", dbUser, dbName)
			require.NoError(t, err)

			// Save all connection, so we can close them later.
			dbConns = append(dbConns, pgConn)
		}

		// We keep the previous connections open, so this one should be rejected, because we exhausted the limit.
		_, err = testCtx.postgresClient(ctx, user, "postgres", dbUser, dbName)
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeded connection limit")
	})

	t.Run("mysql", func(t *testing.T) {
		dbConns := make([]*client.Conn, 0)
		t.Cleanup(func() {
			// Disconnect all clients.
			for _, dbConn := range dbConns {
				err = dbConn.Close()
				require.NoError(t, err)
			}
		})
		// Connect the maximum allowed number of clients.
		for i := int64(0); i < connLimit; i++ {
			mysqlConn, err := testCtx.mysqlClient(user, "mysql", dbUser)
			require.NoError(t, err)

			// Save all connection, so we can close them later.
			dbConns = append(dbConns, mysqlConn)
		}

		// We keep the previous connections open, so this one should be rejected, because we exhausted the limit.
		_, err = testCtx.mysqlClient(user, "mysql", dbUser)
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeded connection limit")
	})

	t.Run("mongodb", func(t *testing.T) {
		dbConns := make([]*mongo.Client, 0)
		t.Cleanup(func() {
			// Disconnect all clients.
			for _, dbConn := range dbConns {
				err = dbConn.Disconnect(ctx)
				require.NoError(t, err)
			}
		})
		// Mongo driver behave different from MySQL and Postgres. In this case we just want to hit the limit
		// by creating some DB connections.
		for i := int64(0); i < 2*connLimit; i++ {
			mongoConn, err := testCtx.mongoClient(ctx, user, "mongo", dbUser)

			if err == nil {
				// Save all connection, so we can close them later.
				dbConns = append(dbConns, mongoConn)

				continue
			}

			require.Contains(t, err.Error(), "exceeded connection limit")
			// When we hit the expected error we can exit.
			return
		}

		require.FailNow(t, "we should exceed the connection limit by now")
	})
}

func TestHeartbeatEvents(t *testing.T) {
	ctx := context.Background()

	dbOne, err := types.NewDatabaseV3(types.Metadata{
		Name: "dbOne",
	}, types.DatabaseSpecV3{
		Protocol: defaults.ProtocolPostgres,
		URI:      "localhost:5432",
	})
	require.NoError(t, err)

	dbTwo, err := types.NewDatabaseV3(types.Metadata{
		Name: "dbOne",
	}, types.DatabaseSpecV3{
		Protocol: defaults.ProtocolMySQL,
		URI:      "localhost:3306",
	})
	require.NoError(t, err)

	tests := map[string]struct {
		staticDatabases types.Databases
		heartbeatCount  int64
	}{
		"SingleStaticDatabase": {
			staticDatabases: types.Databases{dbOne},
			heartbeatCount:  1,
		},
		"MultipleStaticDatabases": {
			staticDatabases: types.Databases{dbOne, dbTwo},
			heartbeatCount:  2,
		},
		"EmptyStaticDatabases": {
			staticDatabases: types.Databases{},
			heartbeatCount:  1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var heartbeatEvents int64
			heartbeatRecorder := func(err error) {
				require.NoError(t, err)
				atomic.AddInt64(&heartbeatEvents, 1)
			}

			testCtx := setupTestContext(ctx, t)
			server := testCtx.setupDatabaseServer(ctx, t, agentParams{
				NoStart:     true,
				OnHeartbeat: heartbeatRecorder,
				Databases:   test.staticDatabases,
			})
			require.NoError(t, server.Start(ctx))
			t.Cleanup(func() {
				server.Close()
			})

			require.NotNil(t, server)
			require.Eventually(t, func() bool {
				return atomic.LoadInt64(&heartbeatEvents) == test.heartbeatCount
			}, 2*time.Second, 500*time.Millisecond)
		})
	}
}
