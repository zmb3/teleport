/*
Copyright 2020-2021 Gravitational, Inc.

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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	"github.com/gravitational/trace"
	"github.com/jackc/pgconn"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/integration/helpers"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv/db"
	"github.com/zmb3/teleport/lib/srv/db/cassandra"
	"github.com/zmb3/teleport/lib/srv/db/common"
	"github.com/zmb3/teleport/lib/srv/db/mongodb"
	"github.com/zmb3/teleport/lib/srv/db/mysql"
	"github.com/zmb3/teleport/lib/srv/db/postgres"
	"github.com/zmb3/teleport/lib/tlsca"
)

// TestDatabaseAccess runs the database access integration test suite.
//
// It allows to make the entire cluster set up once, instead of per test,
// which speeds things up significantly.
func TestDatabaseAccess(t *testing.T) {
	pack := SetupDatabaseTest(t,
		// set tighter rotation intervals
		WithLeafConfig(func(config *service.Config) {
			config.PollingPeriod = 5 * time.Second
			config.RotationConnectionInterval = 2 * time.Second
		}),
		WithRootConfig(func(config *service.Config) {
			config.PollingPeriod = 5 * time.Second
			config.RotationConnectionInterval = 2 * time.Second
		}),
	)
	pack.WaitForLeaf(t)

	t.Run("PostgresRootCluster", pack.testPostgresRootCluster)
	t.Run("PostgresLeafCluster", pack.testPostgresLeafCluster)
	t.Run("MySQLRootCluster", pack.testMySQLRootCluster)
	t.Run("MySQLLeafCluster", pack.testMySQLLeafCluster)
	t.Run("MongoRootCluster", pack.testMongoRootCluster)
	t.Run("MongoLeafCluster", pack.testMongoLeafCluster)
	t.Run("MongoConnectionCount", pack.testMongoConnectionCount)
	t.Run("HARootCluster", pack.testHARootCluster)
	t.Run("HALeafCluster", pack.testHALeafCluster)
	t.Run("LargeQuery", pack.testLargeQuery)
	t.Run("AgentState", pack.testAgentState)
	t.Run("CassandraRootCluster", pack.testCassandraRootCluster)
	t.Run("CassandraLeafCluster", pack.testCassandraLeafCluster)

	// This test should go last because it rotates the Database CA.
	t.Run("RotateTrustedCluster", pack.testRotateTrustedCluster)
}

// TestDatabaseAccessSeparateListeners tests the Mongo and Postgres separate port setup.
func TestDatabaseAccessSeparateListeners(t *testing.T) {
	pack := SetupDatabaseTest(t,
		WithListenerSetupDatabaseTest(helpers.SeparateMongoAndPostgresPortSetup),
	)

	t.Run("PostgresSeparateListener", pack.testPostgresSeparateListener)
	t.Run("MongoSeparateListener", pack.testMongoSeparateListener)
}

// testPostgresRootCluster tests a scenario where a user connects
// to a Postgres database running in a root cluster.
func (p *DatabasePack) testPostgresRootCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.PostgresService.Name,
			Protocol:    p.Root.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.postgres.QueryCount() + 1
	wantLeafQueryCount := p.Leaf.postgres.QueryCount()

	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

// testPostgresLeafCluster tests a scenario where a user connects
// to a Postgres database running in a leaf cluster via a root cluster.
func (p *DatabasePack) testPostgresLeafCluster(t *testing.T) {
	// Connect to the database service in leaf cluster via root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web, // Connecting via root cluster.
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.PostgresService.Name,
			Protocol:    p.Leaf.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.postgres.QueryCount()
	wantLeafQueryCount := p.Leaf.postgres.QueryCount() + 1

	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

func (p *DatabasePack) testRotateTrustedCluster(t *testing.T) {
	// TODO(jakule): Fix flaky test
	t.Skip("flaky test, skip for now")

	var (
		ctx             = context.Background()
		rootCluster     = p.Root.Cluster
		authServer      = rootCluster.Process.GetAuthServer()
		clusterRootName = rootCluster.Secrets.SiteName
		clusterLeafName = p.Leaf.Cluster.Secrets.SiteName
	)

	pw := phaseWatcher{
		clusterRootName: clusterRootName,
		pollingPeriod:   rootCluster.Process.Config.PollingPeriod,
		clock:           p.clock,
		siteAPI:         rootCluster.GetSiteAPI(clusterLeafName),
		certType:        types.DatabaseCA,
	}

	currentDbCA, err := p.Root.dbAuthClient.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.DatabaseCA,
		DomainName: clusterRootName,
	}, false)
	require.NoError(t, err)

	rotationPhases := []string{
		types.RotationPhaseInit, types.RotationPhaseUpdateClients,
		types.RotationPhaseUpdateServers, types.RotationPhaseStandby,
	}

	waitForEvent := func(process *service.TeleportProcess, event string) {
		_, err := process.WaitForEventTimeout(20*time.Second, event)
		require.NoError(t, err, "timeout waiting for service to broadcast event %s", event)
	}

	for _, phase := range rotationPhases {
		errChan := make(chan error, 1)

		go func() {
			errChan <- pw.waitForPhase(phase, func() error {
				return authServer.RotateCertAuthority(ctx, auth.RotateRequest{
					Type:        types.DatabaseCA,
					TargetPhase: phase,
					Mode:        types.RotationModeManual,
				})
			})
		}()

		err = <-errChan

		if err != nil && strings.Contains(err.Error(), "context deadline exceeded") {
			// TODO(jakule): Workaround for CertAuthorityWatcher failing to get the correct rotation status.
			// Query auth server directly to see if the incorrect rotation status is a rotation or watcher problem.
			dbCA, err := p.Leaf.Cluster.Process.GetAuthServer().GetCertAuthority(ctx, types.CertAuthID{
				Type:       types.DatabaseCA,
				DomainName: clusterRootName,
			}, false)
			require.NoError(t, err)
			require.Equal(t, dbCA.GetRotation().Phase, phase)
		} else {
			require.NoError(t, err)
		}

		// Reload doesn't happen on Init
		if phase == types.RotationPhaseInit {
			continue
		}

		waitForEvent(p.Root.Cluster.Process, service.TeleportReloadEvent)
		waitForEvent(p.Leaf.Cluster.Process, service.TeleportReadyEvent)

		p.WaitForLeaf(t)
	}

	rotatedDbCA, err := authServer.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.DatabaseCA,
		DomainName: clusterRootName,
	}, false)
	require.NoError(t, err)

	// Sanity check. Check if the CA was rotated.
	require.NotEqual(t, currentDbCA.GetActiveKeys(), rotatedDbCA.GetActiveKeys())

	// Connect to the database service in leaf cluster via root cluster.
	dbClient, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web, // Connecting via root cluster.
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.PostgresService.Name,
			Protocol:    p.Leaf.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantLeafQueryCount := p.Leaf.postgres.QueryCount() + 1
	wantRootQueryCount := p.Root.postgres.QueryCount()

	result, err := dbClient.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())

	// Disconnect.
	err = dbClient.Close(context.Background())
	require.NoError(t, err)
}

// phaseWatcher holds all arguments required by rotation watcher.
type phaseWatcher struct {
	clusterRootName string
	pollingPeriod   time.Duration
	clock           clockwork.Clock
	siteAPI         types.Events
	certType        types.CertAuthType
}

// waitForPhase waits until rootCluster cluster detects the rotation. fn is a rotation function that is called after
// watcher is created.
func (p *phaseWatcher) waitForPhase(phase string, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), p.pollingPeriod*10)
	defer cancel()

	watcher, err := services.NewCertAuthorityWatcher(ctx, services.CertAuthorityWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Clock:     p.clock,
			Client:    p.siteAPI,
		},
		Types: []types.CertAuthType{p.certType},
	})
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := fn(); err != nil {
		return trace.Wrap(err)
	}

	sub, err := watcher.Subscribe(ctx, types.CertAuthorityFilter{
		p.certType: p.clusterRootName,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	defer sub.Close()

	var lastPhase string
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return trace.CompareFailed("failed to converge to phase %q, last phase %q certType: %v err: %v", phase, lastPhase, p.certType, ctx.Err())
		case <-sub.Done():
			return trace.CompareFailed("failed to converge to phase %q, last phase %q certType: %v err: %v", phase, lastPhase, p.certType, sub.Error())
		case evt := <-sub.Events():
			switch evt.Type {
			case types.OpPut:
				ca, ok := evt.Resource.(types.CertAuthority)
				if !ok {
					return trace.BadParameter("expected a ca got type %T", evt.Resource)
				}
				if ca.GetRotation().Phase == phase {
					return nil
				}
				lastPhase = ca.GetRotation().Phase
			}
		}
	}
	return trace.CompareFailed("failed to converge to phase %q, last phase %q", phase, lastPhase)
}

// testMySQLRootCluster tests a scenario where a user connects
// to a MySQL database running in a root cluster.
func (p *DatabasePack) testMySQLRootCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := mysql.MakeTestClient(common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.MySQL,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.MysqlService.Name,
			Protocol:    p.Root.MysqlService.Protocol,
			Username:    "root",
			// With MySQL database name doesn't matter as it's not subject to RBAC atm.
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.mysql.QueryCount() + 1
	wantLeafQueryCount := p.Leaf.mysql.QueryCount()

	// Execute a query.
	result, err := client.Execute("select 1")
	require.NoError(t, err)
	require.Equal(t, mysql.TestQueryResponse, result)
	require.Equal(t, wantRootQueryCount, p.Root.mysql.QueryCount())
	require.Equal(t, wantLeafQueryCount, p.Leaf.mysql.QueryCount())

	// Disconnect.
	err = client.Close()
	require.NoError(t, err)
}

// testMySQLLeafCluster tests a scenario where a user connects
// to a MySQL database running in a leaf cluster via a root cluster.
func (p *DatabasePack) testMySQLLeafCluster(t *testing.T) {
	// Connect to the database service in leaf cluster via root cluster.
	client, err := mysql.MakeTestClient(common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.MySQL, // Connecting via root cluster.
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.MysqlService.Name,
			Protocol:    p.Leaf.MysqlService.Protocol,
			Username:    "root",
			// With MySQL database name doesn't matter as it's not subject to RBAC atm.
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.mysql.QueryCount()
	wantLeafQueryCount := p.Leaf.mysql.QueryCount() + 1

	// Execute a query.
	result, err := client.Execute("select 1")
	require.NoError(t, err)
	require.Equal(t, mysql.TestQueryResponse, result)
	require.Equal(t, wantLeafQueryCount, p.Leaf.mysql.QueryCount())
	require.Equal(t, wantRootQueryCount, p.Root.mysql.QueryCount())

	// Disconnect.
	err = client.Close()
	require.NoError(t, err)
}

// testMongoRootCluster tests a scenario where a user connects
// to a Mongo database running in a root cluster.
func (p *DatabasePack) testMongoRootCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.MongoService.Name,
			Protocol:    p.Root.MongoService.Protocol,
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
}

// testMongoConnectionCount tests if mongo service releases
// resource after a mongo client disconnect.
func (p *DatabasePack) testMongoConnectionCount(t *testing.T) {
	connectMongoClient := func(t *testing.T) (serverConnectionCount int32) {
		// Connect to the database service in root cluster.
		client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
			AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
			AuthServer: p.Root.Cluster.Process.GetAuthServer(),
			Address:    p.Root.Cluster.Web,
			Cluster:    p.Root.Cluster.Secrets.SiteName,
			Username:   p.Root.User.GetName(),
			RouteToDatabase: tlsca.RouteToDatabase{
				ServiceName: p.Root.MongoService.Name,
				Protocol:    p.Root.MongoService.Protocol,
				Username:    "admin",
			},
		})
		require.NoError(t, err)

		// Execute a query.
		_, err = client.Database("test").Collection("test").Find(context.Background(), bson.M{})
		require.NoError(t, err)

		// Get a server connection count before disconnect.
		serverConnectionCount = p.Root.mongo.GetActiveConnectionsCount()

		// Disconnect.
		err = client.Disconnect(context.Background())
		require.NoError(t, err)

		return serverConnectionCount
	}

	// Get connection count while the first client is connected.
	initialConnectionCount := connectMongoClient(t)

	// Check if active connections count is not growing over time when new
	// clients connect to the mongo server.
	clientCount := 8
	for i := 0; i < clientCount; i++ {
		// Note that connection count per client fluctuates between 6 and 9.
		// Use InDelta to avoid flaky test.
		require.InDelta(t, initialConnectionCount, connectMongoClient(t), 3)
	}

	// Wait until the server reports no more connections. This usually happens
	// really quick but wait a little longer just in case.
	waitUntilNoConnections := func() bool {
		return p.Root.mongo.GetActiveConnectionsCount() == 0
	}
	require.Eventually(t, waitUntilNoConnections, 5*time.Second, 100*time.Millisecond)
}

// testMongoLeafCluster tests a scenario where a user connects
// to a Mongo database running in a leaf cluster.
func (p *DatabasePack) testMongoLeafCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web, // Connecting via root cluster.
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.MongoService.Name,
			Protocol:    p.Leaf.MongoService.Protocol,
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
}

// TestRootLeafIdleTimeout tests idle client connection termination by proxy and DB services in
// trusted cluster setup.
func TestDatabaseRootLeafIdleTimeout(t *testing.T) {
	clock := clockwork.NewFakeClockAt(time.Now())
	pack := SetupDatabaseTest(t, WithClock(clock))
	pack.WaitForLeaf(t)

	var (
		rootAuthServer = pack.Root.Cluster.Process.GetAuthServer()
		rootRole       = pack.Root.role
		leafAuthServer = pack.Leaf.Cluster.Process.GetAuthServer()
		leafRole       = pack.Leaf.role

		idleTimeout = time.Minute
	)

	mkMySQLLeafDBClient := func(t *testing.T) *client.Conn {
		// Connect to the database service in leaf cluster via root cluster.
		client, err := mysql.MakeTestClient(common.TestClientConfig{
			AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
			AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
			Address:    pack.Root.Cluster.MySQL, // Connecting via root cluster.
			Cluster:    pack.Leaf.Cluster.Secrets.SiteName,
			Username:   pack.Root.User.GetName(),
			RouteToDatabase: tlsca.RouteToDatabase{
				ServiceName: pack.Leaf.MysqlService.Name,
				Protocol:    pack.Leaf.MysqlService.Protocol,
				Username:    "root",
			},
		})
		require.NoError(t, err)
		return client
	}

	t.Run("root role without idle timeout", func(t *testing.T) {
		client := mkMySQLLeafDBClient(t)
		_, err := client.Execute("select 1")
		require.NoError(t, err)

		clock.Advance(idleTimeout)
		_, err = client.Execute("select 1")
		require.NoError(t, err)
		err = client.Close()
		require.NoError(t, err)
	})

	t.Run("root role with idle timeout", func(t *testing.T) {
		setRoleIdleTimeout(t, rootAuthServer, rootRole, idleTimeout)
		require.Eventually(t, func() bool {
			role, err := rootAuthServer.GetRole(context.Background(), rootRole.GetName())
			assert.NoError(t, err)
			return time.Duration(role.GetOptions().ClientIdleTimeout) == idleTimeout

		}, time.Second, time.Millisecond*100, "role idle timeout propagation filed")

		client := mkMySQLLeafDBClient(t)
		_, err := client.Execute("select 1")
		require.NoError(t, err)

		now := clock.Now()
		clock.Advance(idleTimeout)
		helpers.WaitForAuditEventTypeWithBackoff(t, pack.Root.Cluster.Process.GetAuthServer(), now, events.ClientDisconnectEvent)

		_, err = client.Execute("select 1")
		require.Error(t, err)
		setRoleIdleTimeout(t, rootAuthServer, rootRole, time.Hour)
	})

	t.Run("leaf role with idle timeout", func(t *testing.T) {
		setRoleIdleTimeout(t, leafAuthServer, leafRole, idleTimeout)
		require.Eventually(t, func() bool {
			role, err := leafAuthServer.GetRole(context.Background(), leafRole.GetName())
			assert.NoError(t, err)
			return time.Duration(role.GetOptions().ClientIdleTimeout) == idleTimeout

		}, time.Second, time.Millisecond*100, "role idle timeout propagation filed")

		client := mkMySQLLeafDBClient(t)
		_, err := client.Execute("select 1")
		require.NoError(t, err)

		now := clock.Now()
		clock.Advance(idleTimeout)
		helpers.WaitForAuditEventTypeWithBackoff(t, pack.Leaf.Cluster.Process.GetAuthServer(), now, events.ClientDisconnectEvent)

		_, err = client.Execute("select 1")
		require.Error(t, err)
		setRoleIdleTimeout(t, leafAuthServer, leafRole, time.Hour)
	})
}

// TestDatabaseAccessUnspecifiedHostname tests DB agent reverse tunnel connection in case where host address is
// unspecified thus is not present in the valid principal list. The DB agent should replace unspecified address (0.0.0.0)
// with localhost and successfully establish reverse tunnel connection.
func TestDatabaseAccessUnspecifiedHostname(t *testing.T) {
	pack := SetupDatabaseTest(t,
		WithNodeName("0.0.0.0"),
	)

	// Connect to the database service in root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: pack.Root.Cluster.GetSiteAPI(pack.Root.Cluster.Secrets.SiteName),
		AuthServer: pack.Root.Cluster.Process.GetAuthServer(),
		Address:    pack.Root.Cluster.Web,
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

	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, uint32(1), pack.Root.postgres.QueryCount())
	require.Equal(t, uint32(0), pack.Leaf.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

func (p *DatabasePack) testPostgresSeparateListener(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Postgres,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.PostgresService.Name,
			Protocol:    p.Root.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.postgres.QueryCount() + 1
	wantLeafQueryCount := p.Root.postgres.QueryCount()

	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

// TestDatabaseAccessPostgresSeparateListener tests postgres proxy listener running on separate port
// with DisableTLS.
func TestDatabaseAccessPostgresSeparateListenerTLSDisabled(t *testing.T) {
	pack := SetupDatabaseTest(t,
		WithListenerSetupDatabaseTest(helpers.SeparatePostgresPortSetup),
		WithRootConfig(func(config *service.Config) {
			config.Proxy.DisableTLS = true
		}),
	)
	pack.testPostgresSeparateListener(t)
}

func init() {
	// Override database agents shuffle behavior to ensure they're always
	// tried in the same order during tests. Used for HA tests.
	db.SetShuffleFunc(db.ShuffleSort)
}

// testHARootCluster verifies that proxy falls back to a healthy
// database agent when multiple agents are serving the same database and one
// of them is down in a root cluster.
func (p *DatabasePack) testHARootCluster(t *testing.T) {
	// Insert a database server entry not backed by an actual running agent
	// to simulate a scenario when an agent is down but the resource hasn't
	// expired from the backend yet.
	dbServer, err := types.NewDatabaseServerV3(types.Metadata{
		Name: p.Root.PostgresService.Name,
	}, types.DatabaseServerSpecV3{
		Protocol: defaults.ProtocolPostgres,
		URI:      p.Root.postgresAddr,
		// To make sure unhealthy server is always picked in tests first, make
		// sure its host ID always compares as "smaller" as the tests sort
		// agents.
		HostID:   "0000",
		Hostname: "test",
	})
	require.NoError(t, err)

	_, err = p.Root.Cluster.Process.GetAuthServer().UpsertDatabaseServer(
		context.Background(), dbServer)
	require.NoError(t, err)

	// Connect to the database service in root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.PostgresService.Name,
			Protocol:    p.Root.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.postgres.QueryCount() + 1
	wantLeafQueryCount := p.Leaf.postgres.QueryCount()
	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

// testHALeafCluster verifies that proxy falls back to a healthy
// database agent when multiple agents are serving the same database and one
// of them is down in a leaf cluster.
func (p *DatabasePack) testHALeafCluster(t *testing.T) {
	// Insert a database server entry not backed by an actual running agent
	// to simulate a scenario when an agent is down but the resource hasn't
	// expired from the backend yet.
	dbServer, err := types.NewDatabaseServerV3(types.Metadata{
		Name: p.Leaf.PostgresService.Name,
	}, types.DatabaseServerSpecV3{
		Protocol: defaults.ProtocolPostgres,
		URI:      p.Leaf.postgresAddr,
		// To make sure unhealthy server is always picked in tests first, make
		// sure its host ID always compares as "smaller" as the tests sort
		// agents.
		HostID:   "0000",
		Hostname: "test",
	})
	require.NoError(t, err)

	_, err = p.Leaf.Cluster.Process.GetAuthServer().UpsertDatabaseServer(
		context.Background(), dbServer)
	require.NoError(t, err)

	// Connect to the database service in leaf cluster via root cluster.
	client, err := postgres.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web, // Connecting via root cluster.
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.PostgresService.Name,
			Protocol:    p.Leaf.PostgresService.Protocol,
			Username:    "postgres",
			Database:    "test",
		},
	})
	require.NoError(t, err)

	wantRootQueryCount := p.Root.postgres.QueryCount()
	wantLeafQueryCount := p.Leaf.postgres.QueryCount() + 1

	// Execute a query.
	result, err := client.Exec(context.Background(), "select 1").ReadAll()
	require.NoError(t, err)
	require.Equal(t, []*pgconn.Result{postgres.TestQueryResponse}, result)
	require.Equal(t, wantLeafQueryCount, p.Leaf.postgres.QueryCount())
	require.Equal(t, wantRootQueryCount, p.Root.postgres.QueryCount())

	// Disconnect.
	err = client.Close(context.Background())
	require.NoError(t, err)
}

// testDatabaseAccessMongoSeparateListener tests mongo proxy listener running on separate port.
func (p *DatabasePack) testMongoSeparateListener(t *testing.T) {
	// Connect to the database service in root cluster.
	client, err := mongodb.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Mongo,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.MongoService.Name,
			Protocol:    p.Root.MongoService.Protocol,
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
}

func (p *DatabasePack) testAgentState(t *testing.T) {
	tests := map[string]struct {
		agentParams databaseAgentStartParams
	}{
		"WithStaticDatabases": {
			agentParams: databaseAgentStartParams{
				databases: []service.Database{
					{Name: "mysql", Protocol: defaults.ProtocolMySQL, URI: "localhost:3306"},
					{Name: "pg", Protocol: defaults.ProtocolPostgres, URI: "localhost:5432"},
				},
			},
		},
		"WithResourceMatchers": {
			agentParams: databaseAgentStartParams{
				resourceMatchers: []services.ResourceMatcher{
					{Labels: types.Labels{"*": []string{"*"}}},
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// Start also ensures that the database agent has the “ready” state.
			// If the agent can’t make it, this function will fail the test.
			agent, _ := p.startRootDatabaseAgent(t, test.agentParams)

			// In addition to the checks performed during the agent start,
			// we’ll request the diagnostic server to ensure the readyz route
			// is returning to the proper state.
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%v/readyz", agent.Config.DiagnosticAddr.Addr), nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

// testCassandraRootCluster tests a scenario where a user connects
// to a Cassandra database running in a root cluster.
func (p *DatabasePack) testCassandraRootCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	dbConn, err := cassandra.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web,
		Cluster:    p.Root.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Root.CassandraService.Name,
			Protocol:    p.Root.CassandraService.Protocol,
			Username:    "cassandra",
		},
	})
	require.NoError(t, err)

	var clusterName string
	err = dbConn.Query("select cluster_name from system.local").Scan(&clusterName)
	require.NoError(t, err)
	require.Equal(t, "Test Cluster", clusterName)
	dbConn.Close()
}

// testCassandraLeafCluster tests a scenario where a user connects
// to a Cassandra database running in a root cluster.
func (p *DatabasePack) testCassandraLeafCluster(t *testing.T) {
	// Connect to the database service in root cluster.
	dbConn, err := cassandra.MakeTestClient(context.Background(), common.TestClientConfig{
		AuthClient: p.Root.Cluster.GetSiteAPI(p.Root.Cluster.Secrets.SiteName),
		AuthServer: p.Root.Cluster.Process.GetAuthServer(),
		Address:    p.Root.Cluster.Web,
		Cluster:    p.Leaf.Cluster.Secrets.SiteName,
		Username:   p.Root.User.GetName(),
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: p.Leaf.CassandraService.Name,
			Protocol:    p.Leaf.CassandraService.Protocol,
			Username:    "cassandra",
		},
	})
	require.NoError(t, err)

	var clusterName string
	err = dbConn.Query("select cluster_name from system.local").Scan(&clusterName)
	require.NoError(t, err)
	require.Equal(t, "Test Cluster", clusterName)
	dbConn.Close()
}

func setRoleIdleTimeout(t *testing.T, authServer *auth.Server, role types.Role, idleTimout time.Duration) {
	opts := role.GetOptions()
	opts.ClientIdleTimeout = types.Duration(idleTimout)
	role.SetOptions(opts)
	err := authServer.UpsertRole(context.Background(), role)
	require.NoError(t, err)
}
