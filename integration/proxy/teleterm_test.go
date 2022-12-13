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

package proxy

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	dbhelpers "github.com/zmb3/teleport/integration/db"
	"github.com/zmb3/teleport/integration/helpers"
	libclient "github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/srv/db/mysql"
	api "github.com/zmb3/teleport/lib/teleterm/api/protogen/golang/v1"
	"github.com/zmb3/teleport/lib/teleterm/api/uri"
	"github.com/zmb3/teleport/lib/teleterm/clusters"
	"github.com/zmb3/teleport/lib/teleterm/daemon"
)

// testTeletermGatewaysCertRenewal is run from within TestALPNSNIProxyDatabaseAccess to amortize the
// cost of setting up clusters in tests.
func testTeletermGatewaysCertRenewal(t *testing.T, pack *dbhelpers.DatabasePack) {
	rootClusterName, _, err := net.SplitHostPort(pack.Root.Cluster.Web)
	require.NoError(t, err)

	creds, err := helpers.GenerateUserCreds(helpers.UserCredsRequest{
		Process:  pack.Root.Cluster.Process,
		Username: pack.Root.User.GetName(),
	})
	require.NoError(t, err)

	t.Run("root cluster", func(t *testing.T) {
		t.Parallel()

		databaseURI := uri.NewClusterURI(rootClusterName).
			AppendDB(pack.Root.MysqlService.Name)

		testGatewayCertRenewal(t, pack, creds, databaseURI)
	})
	t.Run("leaf cluster", func(t *testing.T) {
		t.Parallel()

		leafClusterName := pack.Leaf.Cluster.Secrets.SiteName
		databaseURI := uri.NewClusterURI(rootClusterName).
			AppendLeafCluster(leafClusterName).
			AppendDB(pack.Leaf.MysqlService.Name)

		testGatewayCertRenewal(t, pack, creds, databaseURI)
	})
}

func testGatewayCertRenewal(t *testing.T, pack *dbhelpers.DatabasePack, creds *helpers.UserCreds, databaseURI uri.ResourceURI) {
	tc, err := pack.Root.Cluster.NewClientWithCreds(helpers.ClientConfig{
		Login:   pack.Root.User.GetName(),
		Cluster: pack.Root.Cluster.Secrets.SiteName,
	}, *creds)
	require.NoError(t, err)
	// The profile on disk created by NewClientWithCreds doesn't have WebProxyAddr set.
	tc.WebProxyAddr = pack.Root.Cluster.Web
	tc.SaveProfile(tc.KeysDir, false /* makeCurrent */)

	fakeClock := clockwork.NewFakeClockAt(time.Now())

	storage, err := clusters.NewStorage(clusters.Config{
		Dir:                tc.KeysDir,
		InsecureSkipVerify: tc.InsecureSkipVerify,
		// Inject a fake clock into clusters.Storage so we can control when the middleware thinks the
		// db cert has expired.
		Clock: fakeClock,
	})
	require.NoError(t, err)

	tshdEventsClient := &mockTSHDEventsClient{
		t:          t,
		tc:         tc,
		pack:       pack,
		callCounts: make(map[string]int),
	}

	gatewayCertReissuer := &daemon.GatewayCertReissuer{
		Log:              logrus.NewEntry(logrus.StandardLogger()).WithField(trace.Component, "reissuer"),
		TSHDEventsClient: tshdEventsClient,
	}

	daemonService, err := daemon.New(daemon.Config{
		Storage: storage,
		CreateTshdEventsClientCredsFunc: func() (grpc.DialOption, error) {
			return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
		},
		GatewayCertReissuer: gatewayCertReissuer,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		daemonService.Stop()
	})

	// Here the test setup ends and actual test code starts.

	gateway, err := daemonService.CreateGateway(context.Background(), daemon.CreateGatewayParams{
		TargetURI:  databaseURI.String(),
		TargetUser: "root",
	})
	require.NoError(t, err)

	// Open a new connection.
	client, err := mysql.MakeTestClientWithoutTLS(
		net.JoinHostPort(gateway.LocalAddress(), gateway.LocalPort()),
		gateway.RouteToDatabase())
	require.NoError(t, err)

	// Execute a query.
	result, err := client.Execute("select 1")
	require.NoError(t, err)
	require.Equal(t, mysql.TestQueryResponse, result)

	// Disconnect.
	require.NoError(t, client.Close())

	// Advance the fake clock to simulate the db cert expiry inside the middleware.
	fakeClock.Advance(time.Hour * 48)
	// Overwrite user certs with expired ones to simulate the user cert expiry.
	expiredCreds, err := helpers.GenerateUserCreds(helpers.UserCredsRequest{
		Process:  pack.Root.Cluster.Process,
		Username: pack.Root.User.GetName(),
		TTL:      -time.Hour,
	})
	require.NoError(t, err)
	helpers.SetupUserCreds(tc, pack.Root.Cluster.Config.Proxy.SSHAddr.Addr, *expiredCreds)

	// Open a new connection.
	// This should trigger the relogin flow. The middleware will notice that the db cert has expired
	// and then it will attempt to reissue the db cert using an expired user cert.
	// The mocked tshdEventsClient will issue a valid user cert, save it to disk, and the middleware
	// will let the connection through.
	client, err = mysql.MakeTestClientWithoutTLS(
		net.JoinHostPort(gateway.LocalAddress(), gateway.LocalPort()),
		gateway.RouteToDatabase())
	require.NoError(t, err)

	// Execute a query.
	result, err = client.Execute("select 1")
	require.NoError(t, err)
	require.Equal(t, mysql.TestQueryResponse, result)

	// Disconnect.
	require.NoError(t, client.Close())

	require.Equal(t, 1, tshdEventsClient.callCounts["Relogin"],
		"Unexpected number of calls to TSHDEventsClient.Relogin")
	require.Equal(t, 0, tshdEventsClient.callCounts["SendNotification"],
		"Unexpected number of calls to TSHDEventsClient.SendNotification")
}

type mockTSHDEventsClient struct {
	t          *testing.T
	tc         *libclient.TeleportClient
	pack       *dbhelpers.DatabasePack
	callCounts map[string]int
}

// Relogin simulates the act of the user logging in again in the Electron app by replacing the user
// cert on disk with a valid one.
func (c *mockTSHDEventsClient) Relogin(context.Context, *api.ReloginRequest, ...grpc.CallOption) (*api.ReloginResponse, error) {
	c.callCounts["Relogin"]++
	creds, err := helpers.GenerateUserCreds(helpers.UserCredsRequest{
		Process:  c.pack.Root.Cluster.Process,
		Username: c.pack.Root.User.GetName(),
	})
	require.NoError(c.t, err)
	helpers.SetupUserCreds(c.tc, c.pack.Root.Cluster.Config.Proxy.SSHAddr.Addr, *creds)

	return &api.ReloginResponse{}, nil
}

func (c *mockTSHDEventsClient) SendNotification(context.Context, *api.SendNotificationRequest, ...grpc.CallOption) (*api.SendNotificationResponse, error) {
	c.callCounts["SendNotification"]++
	return &api.SendNotificationResponse{}, nil
}
