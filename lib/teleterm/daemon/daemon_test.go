package daemon

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/gravitational/teleport/api/breaker"
	"github.com/gravitational/teleport/api/constants"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/teleterm/clusters"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/stretchr/testify/require"
)

const staticToken = "test-static-token"

func init() {
	var err error
	ports, err = utils.GetFreeTCPPorts(500, utils.PortStartingNumber)
	if err != nil {
		panic(fmt.Sprintf("failed to allocate tcp ports for tests: %v", err))
	}
}

func TestRestartGateway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

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
	})

	authServer := authProcess.GetAuthServer()
	require.NotNil(t, authServer)

	proxyAddr, err := proxyProcess.ProxyWebAddr()
	require.NoError(t, err)

	storage, err := clusters.NewStorage(clusters.Config{
		Dir:                tmpHomePath,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)
	storage.MockSSOLogin = mockSSOLogin(t, authServer, alice)

	daemonService, err := New(Config{
		Storage:            storage,
		InsecureSkipVerify: true,
	})
	require.NoError(t, err)

	cluster, err := daemonService.AddCluster(ctx, proxyAddr.String())
	require.NoError(t, err)

	err = cluster.SSOLogin(ctx, constants.OIDC, connector.GetName())
	require.NoError(t, err)
	// TODO: Login

	targetURI := cluster.URI.AppendDB("postgres")

	_, err = daemonService.CreateGateway(ctx, clusters.CreateGatewayParams{
		TargetURI:             targetURI.String(),
		TargetUser:            "postgres",
		TargetSubresourceName: "",
		LocalPort:             "",
	})
	require.NoError(t, err)

}

func mockConnector(t *testing.T) types.OIDCConnector {
	// Connector need not be functional since we are going to mock the actual
	// login operation.
	connector, err := types.NewOIDCConnector("auth.example.com", types.OIDCConnectorSpecV3{
		IssuerURL:    "https://auth.example.com",
		RedirectURLs: []string{"https://cluster.example.com"},
		ClientID:     "fake-client",
		ClaimsToRoles: []types.ClaimMapping{
			{
				Claim: "groups",
				Value: "dummy",
				Roles: []string{"dummy"},
			},
		},
	})
	require.NoError(t, err)
	return connector
}

func makeTestServers(t *testing.T, opts ...testServerOptFunc) (auth *service.TeleportProcess, proxy *service.TeleportProcess) {
	var options testServersOpts
	for _, opt := range opts {
		opt(&options)
	}

	var err error
	// Set up a test auth server.
	//
	// We need this to get a random port assigned to it and allow parallel
	// execution of this test.
	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()

	cfg.AuthServers = []utils.NetAddr{{AddrNetwork: "tcp", Addr: net.JoinHostPort("127.0.0.1", ports.Pop())}}
	cfg.Auth.Resources = options.bootstrap
	cfg.Auth.StorageConfig.Params = backend.Params{defaults.BackendPath: filepath.Join(cfg.DataDir, defaults.BackendDir)}
	cfg.Auth.StaticTokens, err = types.NewStaticTokens(types.StaticTokensSpecV2{
		StaticTokens: []types.ProvisionTokenV1{{
			Roles:   []types.SystemRole{types.RoleProxy, types.RoleDatabase, types.RoleTrustedCluster, types.RoleNode},
			Expires: time.Now().Add(time.Minute),
			Token:   staticToken,
		}},
	})
	require.NoError(t, err)
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = true
	cfg.Auth.SSHAddr = utils.NetAddr{AddrNetwork: "tcp", Addr: net.JoinHostPort("127.0.0.1", ports.Pop())}
	cfg.Proxy.Enabled = false
	cfg.Log = utils.NewLoggerForTests()

	for _, fn := range options.configFuncs {
		fn(cfg)
	}

	auth, err = service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, auth.Start())

	t.Cleanup(func() {
		require.NoError(t, auth.Close())
		require.NoError(t, auth.Wait())
	})

	// Wait for proxy to become ready.
	eventCh := make(chan service.Event, 1)
	auth.WaitForEvent(auth.ExitContext(), service.AuthTLSReady, eventCh)
	select {
	case <-eventCh:
	case <-time.After(30 * time.Second):
		// in reality, the auth server should start *much* sooner than this.  we use a very large
		// timeout here because this isn't the kind of problem that this test is meant to catch.
		t.Fatal("auth server didn't start after 30s")
	}

	authAddr, err := auth.AuthSSHAddr()
	require.NoError(t, err)

	// Set up a test proxy service.
	cfg = service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()

	cfg.AuthServers = []utils.NetAddr{*authAddr}
	cfg.Token = staticToken
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = false
	cfg.Proxy.Enabled = true
	cfg.Proxy.WebAddr = utils.NetAddr{AddrNetwork: "tcp", Addr: net.JoinHostPort("127.0.0.1", ports.Pop())}
	cfg.Proxy.SSHAddr = utils.NetAddr{AddrNetwork: "tcp", Addr: net.JoinHostPort("127.0.0.1", ports.Pop())}
	cfg.Proxy.ReverseTunnelListenAddr = utils.NetAddr{AddrNetwork: "tcp", Addr: net.JoinHostPort("127.0.0.1", ports.Pop())}
	cfg.Proxy.DisableWebInterface = true
	cfg.Log = utils.NewLoggerForTests()

	proxy, err = service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, proxy.Start())

	t.Cleanup(func() {
		require.NoError(t, proxy.Close())
		require.NoError(t, proxy.Wait())
	})

	// Wait for proxy to become ready.
	proxy.WaitForEvent(proxy.ExitContext(), service.ProxyWebServerReady, eventCh)
	select {
	case <-eventCh:
	case <-time.After(10 * time.Second):
		t.Fatal("proxy web server didn't start after 10s")
	}

	return auth, proxy
}

var ports utils.PortList

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

func makeTestDatabaseServer(t *testing.T, auth *service.TeleportProcess, proxy *service.TeleportProcess, dbs ...service.Database) (db *service.TeleportProcess) {
	// Proxy uses self-signed certificates in tests.
	lib.SetInsecureDevMode(true)

	cfg := service.MakeDefaultConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	proxyAddr, err := proxy.ProxyWebAddr()
	require.NoError(t, err)

	cfg.AuthServers = []utils.NetAddr{*proxyAddr}
	cfg.Token = proxy.Config.Token
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
	eventCh := make(chan service.Event, 1)
	db.WaitForEvent(db.ExitContext(), service.DatabasesReady, eventCh)
	select {
	case <-eventCh:
	case <-time.After(10 * time.Second):
		t.Fatal("database server didn't start after 10s")
	}

	// Wait for all databases to register to avoid races.
	for _, database := range dbs {
		waitForDatabase(t, auth, database)
	}

	return db
}

func waitForDatabase(t *testing.T, auth *service.TeleportProcess, db service.Database) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		select {
		case <-time.After(500 * time.Millisecond):
			all, err := auth.GetAuthServer().GetDatabaseServers(ctx, apidefaults.Namespace)
			require.NoError(t, err)
			for _, a := range all {
				if a.GetName() == db.Name {
					return
				}
			}
		case <-ctx.Done():
			t.Fatal("database not registered after 10s")
		}
	}
}

func mockSSOLogin(t *testing.T, authServer *auth.Server, user types.User) client.SSOLoginFunc {
	return func(ctx context.Context, connectorID string, pub []byte, protocol string) (*auth.SSHLoginResponse, error) {
		// generate certificates for our user
		sshCert, tlsCert, err := authServer.GenerateUserTestCerts(
			pub, user.GetName(), time.Hour,
			constants.CertificateFormatStandard,
			"localhost",
		)
		require.NoError(t, err)

		// load CA cert
		authority, err := authServer.GetCertAuthority(ctx, types.CertAuthID{
			Type:       types.HostCA,
			DomainName: "localhost",
		}, false)
		require.NoError(t, err)

		// build login response
		return &auth.SSHLoginResponse{
			Username:    user.GetName(),
			Cert:        sshCert,
			TLSCert:     tlsCert,
			HostSigners: auth.AuthoritiesToTrustedCerts([]types.CertAuthority{authority}),
		}, nil
	}
}
