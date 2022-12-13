/*
Copyright 2018 Gravitational, Inc.

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

package helpers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/gravitational/teleport/api/breaker"
	"github.com/gravitational/teleport/api/constants"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/api/utils/retryutils"
	"github.com/gravitational/teleport/lib"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/client"
	libclient "github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/client/identityfile"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/teleagent"
	"github.com/gravitational/teleport/lib/utils"
)

// CommandOptions controls how the SSH command is built.
type CommandOptions struct {
	ForwardAgent bool
	ForcePTY     bool
	ControlPath  string
	SocketPath   string
	ProxyPort    string
	NodePort     string
	Command      string
}

// ExternalSSHCommand runs an external SSH command (if an external ssh binary
// exists) with the passed in parameters.
func ExternalSSHCommand(o CommandOptions) (*exec.Cmd, error) {
	var execArgs []string

	// Don't check the host certificate as part of the testing an external SSH
	// client, this is done elsewhere.
	execArgs = append(execArgs, "-oStrictHostKeyChecking=no")
	execArgs = append(execArgs, "-oUserKnownHostsFile=/dev/null")

	// ControlMaster is often used by applications like Ansible.
	if o.ControlPath != "" {
		execArgs = append(execArgs, "-oControlMaster=auto")
		execArgs = append(execArgs, "-oControlPersist=1s")
		execArgs = append(execArgs, "-oConnectTimeout=2")
		execArgs = append(execArgs, fmt.Sprintf("-oControlPath=%v", o.ControlPath))
	}

	// The -tt flag is used to force PTY allocation. It's often used by
	// applications like Ansible.
	if o.ForcePTY {
		execArgs = append(execArgs, "-tt")
	}

	// Connect to node on the passed in port.
	execArgs = append(execArgs, fmt.Sprintf("-p %v", o.NodePort))

	// Build proxy command.
	proxyCommand := []string{"ssh"}
	proxyCommand = append(proxyCommand, "-oStrictHostKeyChecking=no")
	proxyCommand = append(proxyCommand, "-oUserKnownHostsFile=/dev/null")
	if o.ForwardAgent {
		proxyCommand = append(proxyCommand, "-oForwardAgent=yes")
	}
	proxyCommand = append(proxyCommand, fmt.Sprintf("-p %v", o.ProxyPort))
	proxyCommand = append(proxyCommand, `%r@localhost -s proxy:%h:%p`)

	// Add in ProxyCommand option, needed for all Teleport connections.
	execArgs = append(execArgs, fmt.Sprintf("-oProxyCommand=%v", strings.Join(proxyCommand, " ")))

	// Add in the host to connect to and the command to run when connected.
	execArgs = append(execArgs, Host)
	execArgs = append(execArgs, o.Command)

	// Find the OpenSSH binary.
	sshpath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Create an exec.Command and tell it where to find the SSH agent.
	cmd, err := exec.Command(sshpath, execArgs...), nil
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cmd.Env = []string{fmt.Sprintf("SSH_AUTH_SOCK=%v", o.SocketPath)}

	return cmd, nil
}

// CreateAgent creates a SSH agent with the passed in private key and
// certificate that can be used in tests. This is useful so tests don't
// clobber your system agent.
func CreateAgent(me *user.User, key *client.Key) (*teleagent.AgentServer, string, string, error) {
	// create a path to the unix socket
	sockDirName := "int-test"
	sockName := "agent.sock"

	agentKey, err := key.AsAgentKey()
	if err != nil {
		return nil, "", "", trace.Wrap(err)
	}

	// create a (unstarted) agent and add the agent key(s) to it
	keyring, ok := agent.NewKeyring().(agent.ExtendedAgent)
	if !ok {
		return nil, "", "", trace.Errorf("unexpected keyring type: %T, expected agent.ExtendedKeyring", keyring)
	}

	if err := keyring.Add(agentKey); err != nil {
		return nil, "", "", trace.Wrap(err)
	}

	teleAgent := teleagent.NewServer(func() (teleagent.Agent, error) {
		return teleagent.NopCloser(keyring), nil
	})

	// start the SSH agent
	err = teleAgent.ListenUnixSocket(sockDirName, sockName, me)
	if err != nil {
		return nil, "", "", trace.Wrap(err)
	}
	go teleAgent.Serve()

	return teleAgent, teleAgent.Dir, teleAgent.Path, nil
}

func CloseAgent(teleAgent *teleagent.AgentServer, socketDirPath string) error {
	err := teleAgent.Close()
	if err != nil {
		return trace.Wrap(err)
	}

	err = os.RemoveAll(socketDirPath)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// GetLocalIP gets the non-loopback IP address of this host.
func GetLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", trace.Wrap(err)
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		if !ip.IsLoopback() && ip.IsPrivate() {
			return ip.String(), nil
		}
	}
	return "", trace.NotFound("No non-loopback local IP address found")
}

func MustCreateUserIdentityFile(t *testing.T, tc *TeleInstance, username string, ttl time.Duration) string {
	key, err := libclient.GenerateRSAKey()
	require.NoError(t, err)
	key.ClusterName = tc.Secrets.SiteName

	sshCert, tlsCert, err := tc.Process.GetAuthServer().GenerateUserTestCerts(
		key.MarshalSSHPublicKey(), username, ttl,
		constants.CertificateFormatStandard,
		tc.Secrets.SiteName, "",
	)
	require.NoError(t, err)

	key.Cert = sshCert
	key.TLSCert = tlsCert

	hostCAs, err := tc.Process.GetAuthServer().GetCertAuthorities(context.Background(), types.HostCA, false)
	require.NoError(t, err)
	key.TrustedCA = auth.AuthoritiesToTrustedCerts(hostCAs)

	idPath := filepath.Join(t.TempDir(), "user_identity")
	_, err = identityfile.Write(identityfile.WriteConfig{
		OutputPath: idPath,
		Key:        key,
		Format:     identityfile.FormatFile,
	})
	require.NoError(t, err)
	return idPath
}

// WaitForProxyCount waits a set time for the proxy count in clusterName to
// reach some value.
func WaitForProxyCount(t *TeleInstance, clusterName string, count int) error {
	var counts map[string]int
	start := time.Now()
	for time.Since(start) < 17*time.Second {
		counts = t.RemoteClusterWatcher.Counts()
		if counts[clusterName] == count {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return trace.BadParameter("proxy count on %v: %v (wanted %v)", clusterName, counts[clusterName], count)
}

func WaitForAuditEventTypeWithBackoff(t *testing.T, cli *auth.Server, startTime time.Time, eventType string) []apievents.AuditEvent {
	max := time.Second
	timeout := time.After(max)
	bf, err := retryutils.NewLinear(retryutils.LinearConfig{
		Step: max / 10,
		Max:  max,
	})
	if err != nil {
		t.Fatalf("failed to create linear backoff: %v", err)
	}
	for {
		events, _, err := cli.SearchEvents(startTime, time.Now().Add(time.Hour), apidefaults.Namespace, []string{eventType}, 100, types.EventOrderAscending, "")
		if err != nil {
			t.Fatalf("failed to call SearchEvents: %v", err)
		}
		if len(events) != 0 {
			return events
		}
		select {
		case <-bf.After():
			bf.Inc()
		case <-timeout:
			t.Fatalf("event type %q not found after %v", eventType, max)
		}
	}
}

func MustGetCurrentUser(t *testing.T) *user.User {
	user, err := user.Current()
	require.NoError(t, err)
	return user
}

func WaitForDatabaseServers(t *testing.T, authServer *auth.Server, dbs []service.Database) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()

	for {
		all, err := authServer.GetDatabaseServers(ctx, apidefaults.Namespace)
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

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			require.Fail(t, "database servers not registered after 10s")
		}
	}
}

// MakeTestServers starts an Auth and a Proxy Service.
// Besides those processes, it also returns a provision token which can be used to add other services.
func MakeTestServers(t *testing.T) (auth *service.TeleportProcess, proxy *service.TeleportProcess, provisionToken string) {
	provisionToken = uuid.NewString()
	var err error
	// Set up a test auth server.
	//
	// We need this to get a random port assigned to it and allow parallel
	// execution of this test.
	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()
	cfg.SetAuthServerAddress(cfg.Auth.ListenAddr)
	cfg.Auth.ListenAddr.Addr = NewListener(t, service.ListenerAuth, &cfg.FileDescriptors)
	cfg.Auth.Preference.SetSecondFactor(constants.SecondFactorOff)
	cfg.Auth.StorageConfig.Params = backend.Params{defaults.BackendPath: filepath.Join(cfg.DataDir, defaults.BackendDir)}
	cfg.Auth.StaticTokens, err = types.NewStaticTokens(types.StaticTokensSpecV2{
		StaticTokens: []types.ProvisionTokenV1{{
			Roles:   []types.SystemRole{types.RoleProxy, types.RoleDatabase, types.RoleTrustedCluster, types.RoleNode, types.RoleApp},
			Expires: time.Now().Add(time.Minute),
			Token:   provisionToken,
		}},
	})
	require.NoError(t, err)
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = true
	cfg.Proxy.Enabled = false
	cfg.Log = utils.NewLoggerForTests()

	auth, err = service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, auth.Start())

	t.Cleanup(func() {
		require.NoError(t, auth.Close())
		require.NoError(t, auth.Wait())
	})

	// Wait for proxy to become ready.
	_, err = auth.WaitForEventTimeout(30*time.Second, service.AuthTLSReady)
	// in reality, the auth server should start *much* sooner than this.  we use a very large
	// timeout here because this isn't the kind of problem that this test is meant to catch.
	require.NoError(t, err, "auth server didn't start after 30s")

	authAddr, err := auth.AuthAddr()
	require.NoError(t, err)

	// Set up a test proxy service.
	cfg = service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()

	cfg.SetAuthServerAddress(*authAddr)
	cfg.SetToken(provisionToken)
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = false
	cfg.Proxy.Enabled = true
	cfg.Proxy.ReverseTunnelListenAddr.Addr = NewListener(t, service.ListenerProxyTunnel, &cfg.FileDescriptors)
	cfg.Proxy.WebAddr.Addr = NewListener(t, service.ListenerProxyWeb, &cfg.FileDescriptors)
	cfg.Proxy.PublicAddrs = []utils.NetAddr{
		cfg.Proxy.WebAddr,
	}
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
	_, err = proxy.WaitForEventTimeout(10*time.Second, service.ProxyWebServerReady)
	require.NoError(t, err, "proxy web server didn't start after 10s")

	return auth, proxy, provisionToken
}

// MakeTestDatabaseServer creates a Database Service
// It receives the Proxy Address, a Token (to join the cluster) and a list of Datbases
func MakeTestDatabaseServer(t *testing.T, proxyAddr utils.NetAddr, token string, dbs ...service.Database) (db *service.TeleportProcess) {
	// Proxy uses self-signed certificates in tests.
	lib.SetInsecureDevMode(true)

	cfg := service.MakeDefaultConfig()
	cfg.Hostname = "localhost"
	cfg.DataDir = t.TempDir()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	cfg.SetAuthServerAddress(proxyAddr)
	cfg.SetToken(token)
	cfg.SSH.Enabled = false
	cfg.Auth.Enabled = false
	cfg.Databases.Enabled = true
	cfg.Databases.Databases = dbs
	cfg.Log = utils.NewLoggerForTests()

	db, err := service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, db.Start())

	t.Cleanup(func() {
		assert.NoError(t, db.Close())
	})

	// Wait for database agent to start.
	_, err = db.WaitForEventTimeout(10*time.Second, service.DatabasesReady)
	require.NoError(t, err, "database server didn't start after 10s")

	return db
}
