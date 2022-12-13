/*
Copyright 2015-2020 Gravitational, Inc.

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

package web

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/gogo/protobuf/proto"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/gravitational/roundtrip"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/julienschmidt/httprouter"
	lemma_secret "github.com/mailgun/lemma/secret"
	"github.com/pquerna/otp/totp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	otlp "go.opentelemetry.io/proto/otlp/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"golang.org/x/crypto/ssh"
	"golang.org/x/exp/slices"
	"golang.org/x/text/encoding/unicode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/testing/protocmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	authztypes "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	authproto "github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/client/webclient"
	"github.com/zmb3/teleport/api/constants"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/mocku2f"
	"github.com/zmb3/teleport/lib/auth/native"
	"github.com/zmb3/teleport/lib/auth/testauthority"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/bpf"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/client/conntest"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/fixtures"
	"github.com/zmb3/teleport/lib/httplib"
	"github.com/zmb3/teleport/lib/httplib/csrf"
	kubeproxy "github.com/zmb3/teleport/lib/kube/proxy"
	"github.com/zmb3/teleport/lib/limiter"
	"github.com/zmb3/teleport/lib/modules"
	"github.com/zmb3/teleport/lib/observability/tracing"
	"github.com/zmb3/teleport/lib/pam"
	"github.com/zmb3/teleport/lib/proxy"
	restricted "github.com/zmb3/teleport/lib/restrictedsession"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/secret"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/session"
	"github.com/zmb3/teleport/lib/srv"
	"github.com/zmb3/teleport/lib/srv/desktop"
	"github.com/zmb3/teleport/lib/srv/desktop/tdp"
	"github.com/zmb3/teleport/lib/srv/regular"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/web/ui"
)

const hostID = "00000000-0000-0000-0000-000000000000"

type WebSuite struct {
	ctx    context.Context
	cancel context.CancelFunc

	node        *regular.Server
	proxy       *regular.Server
	proxyTunnel reversetunnel.Server
	srvID       string

	user       string
	webServer  *httptest.Server
	webHandler *APIHandler

	mockU2F     *mocku2f.Key
	server      *auth.TestServer
	proxyClient *auth.Client
	clock       clockwork.FakeClock
}

// TestMain will re-execute Teleport to run a command if "exec" is passed to
// it as an argument. Otherwise it will run tests as normal.
func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	// If the test is re-executing itself, execute the command that comes over
	// the pipe.
	if srv.IsReexec() {
		srv.RunAndExit(os.Args[1])
		return
	}

	// Otherwise run tests as normal.
	code := m.Run()
	os.Exit(code)
}

func newWebSuite(t *testing.T) *WebSuite {
	return newWebSuiteWithConfig(t, webSuiteConfig{})
}

type webSuiteConfig struct {
	// AuthPreferenceSpec is custom initial AuthPreference spec for the test.
	authPreferenceSpec *types.AuthPreferenceSpecV2
}

func newWebSuiteWithConfig(t *testing.T, cfg webSuiteConfig) *WebSuite {
	mockU2F, err := mocku2f.Create()
	require.NoError(t, err)
	require.NotNil(t, mockU2F)

	u, err := user.Current()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	s := &WebSuite{
		mockU2F: mockU2F,
		clock:   clockwork.NewFakeClock(),
		user:    u.Username,
		ctx:     ctx,
		cancel:  cancel,
	}

	networkingConfig, err := types.NewClusterNetworkingConfigFromConfigFile(types.ClusterNetworkingConfigSpecV2{
		KeepAliveInterval: types.Duration(10 * time.Second),
	})
	require.NoError(t, err)

	s.server, err = auth.NewTestServer(auth.TestServerConfig{
		Auth: auth.TestAuthServerConfig{
			ClusterName:             "localhost",
			Dir:                     t.TempDir(),
			Clock:                   s.clock,
			ClusterNetworkingConfig: networkingConfig,
			AuthPreferenceSpec:      cfg.authPreferenceSpec,
		},
	})
	require.NoError(t, err)

	// Register the auth server, since test auth server doesn't start its own
	// heartbeat.
	err = s.server.Auth().UpsertAuthServer(&types.ServerV2{
		Kind:    types.KindAuthServer,
		Version: types.V2,
		Metadata: types.Metadata{
			Namespace: apidefaults.Namespace,
			Name:      "auth",
		},
		Spec: types.ServerSpecV2{
			Addr:     s.server.TLS.Listener.Addr().String(),
			Hostname: "localhost",
			Version:  teleport.Version,
		},
	})
	require.NoError(t, err)

	priv, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	tlsPub, err := auth.PrivateKeyToPublicKeyTLS(priv)
	require.NoError(t, err)

	// start node
	certs, err := s.server.Auth().GenerateHostCerts(s.ctx,
		&authproto.HostCertsRequest{
			HostID:       hostID,
			NodeName:     s.server.ClusterName(),
			Role:         types.RoleNode,
			PublicSSHKey: pub,
			PublicTLSKey: tlsPub,
		})
	require.NoError(t, err)

	signer, err := sshutils.NewSigner(priv, certs.SSH)
	require.NoError(t, err)

	nodeID := "node"
	nodeClient, err := s.server.NewClient(auth.TestIdentity{
		I: auth.BuiltinRole{
			Role:     types.RoleNode,
			Username: nodeID,
		},
	})
	require.NoError(t, err)

	nodeLockWatcher, err := services.NewLockWatcher(s.ctx, services.LockWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentNode,
			Client:    nodeClient,
		},
	})
	require.NoError(t, err)

	nodeSessionController, err := srv.NewSessionController(srv.SessionControllerConfig{
		Semaphores:   nodeClient,
		AccessPoint:  nodeClient,
		LockEnforcer: nodeLockWatcher,
		Emitter:      nodeClient,
		Component:    teleport.ComponentNode,
		ServerID:     nodeID,
	})
	require.NoError(t, err)

	// create SSH service:
	nodeDataDir := t.TempDir()
	node, err := regular.New(
		ctx,
		utils.NetAddr{AddrNetwork: "tcp", Addr: "127.0.0.1:0"},
		s.server.ClusterName(),
		[]ssh.Signer{signer},
		nodeClient,
		nodeDataDir,
		"",
		utils.NetAddr{},
		nodeClient,
		regular.SetUUID(nodeID),
		regular.SetNamespace(apidefaults.Namespace),
		regular.SetShell("/bin/sh"),
		regular.SetEmitter(nodeClient),
		regular.SetPAMConfig(&pam.Config{Enabled: false}),
		regular.SetBPF(&bpf.NOP{}),
		regular.SetRestrictedSessionManager(&restricted.NOP{}),
		regular.SetClock(s.clock),
		regular.SetLockWatcher(nodeLockWatcher),
		regular.SetSessionController(nodeSessionController),
	)
	require.NoError(t, err)
	s.node = node
	s.srvID = node.ID()
	require.NoError(t, s.node.Start())

	// create reverse tunnel service:
	proxyID := "proxy"
	s.proxyClient, err = s.server.NewClient(auth.TestIdentity{
		I: auth.BuiltinRole{
			Role:     types.RoleProxy,
			Username: proxyID,
		},
	})
	require.NoError(t, err)

	revTunListener, err := net.Listen("tcp", fmt.Sprintf("%v:0", s.server.ClusterName()))
	require.NoError(t, err)

	proxyLockWatcher, err := services.NewLockWatcher(s.ctx, services.LockWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    s.proxyClient,
		},
	})
	require.NoError(t, err)

	proxyNodeWatcher, err := services.NewNodeWatcher(s.ctx, services.NodeWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    s.proxyClient,
		},
	})
	require.NoError(t, err)

	caWatcher, err := services.NewCertAuthorityWatcher(s.ctx, services.CertAuthorityWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    s.proxyClient,
		},
		Types: []types.CertAuthType{types.HostCA, types.UserCA},
	})
	require.NoError(t, err)
	defer caWatcher.Close()

	revTunServer, err := reversetunnel.NewServer(reversetunnel.Config{
		ID:                    node.ID(),
		Listener:              revTunListener,
		ClientTLS:             s.proxyClient.TLSConfig(),
		ClusterName:           s.server.ClusterName(),
		HostSigners:           []ssh.Signer{signer},
		LocalAuthClient:       s.proxyClient,
		LocalAccessPoint:      s.proxyClient,
		Emitter:               s.proxyClient,
		NewCachingAccessPoint: noCache,
		DataDir:               t.TempDir(),
		LockWatcher:           proxyLockWatcher,
		NodeWatcher:           proxyNodeWatcher,
		CertAuthorityWatcher:  caWatcher,
		CircuitBreakerConfig:  breaker.NoopBreakerConfig(),
		LocalAuthAddresses:    []string{s.server.TLS.Listener.Addr().String()},
		Clock:                 s.clock,
	})
	require.NoError(t, err)
	s.proxyTunnel = revTunServer

	router, err := proxy.NewRouter(proxy.RouterConfig{
		ClusterName:         s.server.ClusterName(),
		Log:                 utils.NewLoggerForTests().WithField(trace.Component, "test"),
		RemoteClusterGetter: s.proxyClient,
		SiteGetter:          revTunServer,
		TracerProvider:      tracing.NoopProvider(),
	})
	require.NoError(t, err)

	proxySessionController, err := srv.NewSessionController(srv.SessionControllerConfig{
		Semaphores:   s.proxyClient,
		AccessPoint:  s.proxyClient,
		LockEnforcer: proxyLockWatcher,
		Emitter:      s.proxyClient,
		Component:    teleport.ComponentProxy,
		ServerID:     proxyID,
	})
	require.NoError(t, err)

	// proxy server:
	s.proxy, err = regular.New(
		ctx,
		utils.NetAddr{AddrNetwork: "tcp", Addr: "127.0.0.1:0"},
		s.server.ClusterName(),
		[]ssh.Signer{signer},
		s.proxyClient,
		t.TempDir(),
		"",
		utils.NetAddr{},
		s.proxyClient,
		regular.SetUUID(proxyID),
		regular.SetProxyMode("", revTunServer, s.proxyClient, router),
		regular.SetEmitter(s.proxyClient),
		regular.SetNamespace(apidefaults.Namespace),
		regular.SetBPF(&bpf.NOP{}),
		regular.SetRestrictedSessionManager(&restricted.NOP{}),
		regular.SetClock(s.clock),
		regular.SetLockWatcher(proxyLockWatcher),
		regular.SetNodeWatcher(proxyNodeWatcher),
		regular.SetSessionController(proxySessionController),
	)
	require.NoError(t, err)

	// Expired sessions are purged immediately
	var sessionLingeringThreshold time.Duration
	fs, err := NewDebugFileSystem("../../webassets/teleport")
	require.NoError(t, err)

	handler, err := NewHandler(Config{
		ClusterFeatures:                 *modules.GetModules().Features().ToProto(), // safe to dereference because ToProto creates a struct and return a pointer to it
		Proxy:                           revTunServer,
		AuthServers:                     utils.FromAddr(s.server.TLS.Addr()),
		DomainName:                      s.server.ClusterName(),
		ProxyClient:                     s.proxyClient,
		CipherSuites:                    utils.DefaultCipherSuites(),
		AccessPoint:                     s.proxyClient,
		Context:                         s.ctx,
		HostUUID:                        proxyID,
		Emitter:                         s.proxyClient,
		StaticFS:                        fs,
		cachedSessionLingeringThreshold: &sessionLingeringThreshold,
		ProxySettings:                   &mockProxySettings{},
		SessionControl:                  proxySessionController,
		Router:                          router,
	}, SetSessionStreamPollPeriod(200*time.Millisecond), SetClock(s.clock))
	require.NoError(t, err)

	s.webServer = httptest.NewUnstartedServer(handler)
	s.webHandler = handler
	s.webServer.StartTLS()
	err = s.proxy.Start()
	require.NoError(t, err)

	// Wait for proxy to fully register before starting the test.
	for start := time.Now(); ; {
		proxies, err := s.proxyClient.GetProxies()
		require.NoError(t, err)
		if len(proxies) != 0 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("proxy didn't register within 5s after startup")
		}
	}

	proxyAddr := utils.MustParseAddr(s.proxy.Addr())

	addr := utils.MustParseAddr(s.webServer.Listener.Addr().String())
	handler.handler.cfg.ProxyWebAddr = *addr
	handler.handler.cfg.ProxySSHAddr = *proxyAddr
	_, sshPort, err := net.SplitHostPort(proxyAddr.String())
	require.NoError(t, err)
	handler.handler.sshPort = sshPort

	t.Cleanup(func() {
		// In particular close the lock watchers by canceling the context.
		s.cancel()

		s.webServer.Close()

		var errors []error
		if err := s.proxyTunnel.Close(); err != nil {
			errors = append(errors, err)
		}
		if err := s.node.Close(); err != nil {
			errors = append(errors, err)
		}
		s.webServer.Close()
		if err := s.proxy.Close(); err != nil {
			errors = append(errors, err)
		}
		if err := s.server.Shutdown(context.Background()); err != nil {
			errors = append(errors, err)
		}
		require.Empty(t, errors)
	})

	return s
}

func noCache(clt auth.ClientI, cacheName []string) (auth.RemoteProxyAccessPoint, error) {
	return clt, nil
}

func (r *authPack) renewSession(ctx context.Context, t *testing.T) *roundtrip.Response {
	resp, err := r.clt.PostJSON(ctx, r.clt.Endpoint("webapi", "sessions", "renew"), nil)
	require.NoError(t, err)
	return resp
}

func (r *authPack) validateAPI(ctx context.Context, t *testing.T) {
	_, err := r.clt.Get(ctx, r.clt.Endpoint("webapi", "sites"), url.Values{})
	require.NoError(t, err)
}

type authPack struct {
	otpSecret string
	user      string
	login     string
	password  string
	session   *CreateSessionResponse
	clt       *client.WebClient
	cookies   []*http.Cookie
}

// authPack returns new authenticated package consisting of created valid
// user, otp token, created web session and authenticated client.
func (s *WebSuite) authPack(t *testing.T, user string) *authPack {
	login := s.user
	pass := "abc123"
	rawSecret := "def456"
	otpSecret := base32.StdEncoding.EncodeToString([]byte(rawSecret))

	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOTP,
	})
	require.NoError(t, err)
	err = s.server.Auth().SetAuthPreference(s.ctx, ap)
	require.NoError(t, err)

	s.createUser(t, user, login, pass, otpSecret)

	// create a valid otp token
	validToken, err := totp.GenerateCode(otpSecret, s.clock.Now())
	require.NoError(t, err)

	clt := s.client()
	req := CreateSessionReq{
		User:              user,
		Pass:              pass,
		SecondFactorToken: validToken,
	}

	csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	re, err := s.login(clt, csrfToken, csrfToken, req)
	require.NoError(t, err)

	var rawSess *CreateSessionResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &rawSess))

	sess, err := rawSess.response()
	require.NoError(t, err)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	clt = s.client(roundtrip.BearerAuth(sess.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(s.url(), re.Cookies())

	return &authPack{
		otpSecret: otpSecret,
		user:      user,
		login:     login,
		session:   sess,
		clt:       clt,
		cookies:   re.Cookies(),
	}
}

func (s *WebSuite) createUser(t *testing.T, user string, login string, pass string, otpSecret string) {
	teleUser, err := types.NewUser(user)
	require.NoError(t, err)
	role := services.RoleForUser(teleUser)
	role.SetLogins(types.Allow, []string{login})
	options := role.GetOptions()
	options.ForwardAgent = types.NewBool(true)
	role.SetOptions(options)
	err = s.server.Auth().UpsertRole(s.ctx, role)
	require.NoError(t, err)
	teleUser.AddRole(role.GetName())

	teleUser.SetCreatedBy(types.CreatedBy{
		User: types.UserRef{Name: "some-auth-user"},
	})
	err = s.server.Auth().CreateUser(s.ctx, teleUser)
	require.NoError(t, err)

	err = s.server.Auth().UpsertPassword(user, []byte(pass))
	require.NoError(t, err)

	if otpSecret != "" {
		dev, err := services.NewTOTPDevice("otp", otpSecret, s.clock.Now())
		require.NoError(t, err)
		err = s.server.Auth().UpsertMFADevice(context.Background(), user, dev)
		require.NoError(t, err)
	}
}

func TestValidRedirectURL(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		desc, url string
		valid     bool
	}{
		{"valid absolute https url", "https://example.com?a=1", true},
		{"valid absolute http url", "http://example.com?a=1", true},
		{"valid relative url", "/path/to/something", true},
		{"garbage", "fjoiewjwpods302j09", false},
		{"empty string", "", false},
		{"block bad protocol", "javascript:alert('xss')", false},
	} {
		t.Run(tt.desc, func(t *testing.T) {
			require.Equal(t, tt.valid, isValidRedirectURL(tt.url))
		})
	}
}

func TestMetaRedirect(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	redirectHandler := h.WithMetaRedirect(func(w http.ResponseWriter, r *http.Request, p httprouter.Params) string {
		return "https://example.com"
	})
	req := httptest.NewRequest(http.MethodPost, "/some/route", nil)
	resp := httptest.NewRecorder()
	redirectHandler(resp, req, nil)
	targetElement := `<meta http-equiv="refresh" content="0;URL='https://example.com'" />`
	require.Equal(t, http.StatusOK, resp.Code)
	body := resp.Body.String()
	require.Contains(t, body, targetElement)
}

func Test_clientMetaFromReq(t *testing.T) {
	ua := "foobar"
	r := httptest.NewRequest(
		http.MethodGet, "https://example.com/webapi/foo", nil,
	)
	r.Header.Set("User-Agent", ua)

	got := clientMetaFromReq(r)
	require.Equal(t, &auth.ForwardedClientMetadata{
		UserAgent:  ua,
		RemoteAddr: "192.0.2.1:1234",
	}, got)
}

func TestSAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		rawConnector        string
		validSession        bool
		expectedRedirectURL string
	}{
		{
			name:                "success",
			rawConnector:        fixtures.SAMLOktaConnectorV2,
			validSession:        true,
			expectedRedirectURL: "/after",
		},
		{
			name:                "fail to map claims to roles",
			rawConnector:        strings.ReplaceAll(fixtures.SAMLOktaConnectorV2, "Everyone", "No-one"),
			validSession:        false,
			expectedRedirectURL: client.LoginFailedUnauthorizedRedirectURL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newWebSuite(t)
			input := tc.rawConnector

			decoder := kyaml.NewYAMLOrJSONDecoder(strings.NewReader(input), defaults.LookaheadBufSize)
			var raw services.UnknownResource
			err := decoder.Decode(&raw)
			require.NoError(t, err)

			connector, err := services.UnmarshalSAMLConnector(raw.Raw)
			require.NoError(t, err)

			role, err := types.NewRoleV3(connector.GetAttributesToRoles()[0].Roles[0], types.RoleSpecV5{
				Options: types.RoleOptions{
					MaxSessionTTL: types.NewDuration(apidefaults.MaxCertDuration),
				},
				Allow: types.RoleConditions{
					NodeLabels: types.Labels{types.Wildcard: []string{types.Wildcard}},
					Namespaces: []string{apidefaults.Namespace},
					Rules: []types.Rule{
						types.NewRule(types.Wildcard, services.RW()),
					},
				},
			})
			require.NoError(t, err)
			role.SetLogins(types.Allow, []string{s.user})
			err = s.server.Auth().UpsertRole(s.ctx, role)
			require.NoError(t, err)

			err = s.server.Auth().UpsertSAMLConnector(ctx, connector)
			require.NoError(t, err)
			s.server.Auth().SetClock(clockwork.NewFakeClockAt(time.Date(2017, 5, 10, 18, 53, 0, 0, time.UTC)))
			clt := s.clientNoRedirects()

			csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"

			baseURL, err := url.Parse(clt.Endpoint("webapi", "saml", "sso") + `?connector_id=` + connector.GetName() + `&redirect_url=http://localhost/after`)
			require.NoError(t, err)
			req, err := http.NewRequest("GET", baseURL.String(), nil)
			require.NoError(t, err)
			addCSRFCookieToReq(req, csrfToken)
			re, err := clt.Client.RoundTrip(func() (*http.Response, error) {
				return clt.Client.HTTPClient().Do(req)
			})
			require.NoError(t, err)

			// we got a redirect
			urlPattern := regexp.MustCompile(`URL='([^']*)'`)
			locationURL := urlPattern.FindStringSubmatch(string(re.Bytes()))[1]
			u, err := url.Parse(locationURL)
			require.NoError(t, err)
			require.Equal(t, fixtures.SAMLOktaSSO, u.Scheme+"://"+u.Host+u.Path)
			data, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
			require.NoError(t, err)
			buf, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
			require.NoError(t, err)
			doc := etree.NewDocument()
			err = doc.ReadFromBytes(buf)
			require.NoError(t, err)
			id := doc.Root().SelectAttr("ID")
			require.NotNil(t, id)

			authRequest, err := s.server.Auth().GetSAMLAuthRequest(context.Background(), id.Value)
			require.NoError(t, err)

			// now swap the request id to the hardcoded one in fixtures
			authRequest.ID = fixtures.SAMLOktaAuthRequestID
			authRequest.CSRFToken = csrfToken
			err = s.server.Auth().Services.CreateSAMLAuthRequest(ctx, *authRequest, backend.Forever)
			require.NoError(t, err)

			// now respond with pre-recorded request to the POST url
			in := &bytes.Buffer{}
			fw, err := flate.NewWriter(in, flate.DefaultCompression)
			require.NoError(t, err)

			_, err = fw.Write([]byte(fixtures.SAMLOktaAuthnResponseXML))
			require.NoError(t, err)
			err = fw.Close()
			require.NoError(t, err)
			encodedResponse := base64.StdEncoding.EncodeToString(in.Bytes())
			require.NotNil(t, encodedResponse)

			// now send the response to the server to exchange it for auth session
			form := url.Values{}
			form.Add("SAMLResponse", encodedResponse)
			req, err = http.NewRequest("POST", clt.Endpoint("webapi", "saml", "acs"), strings.NewReader(form.Encode()))
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			addCSRFCookieToReq(req, csrfToken)
			require.NoError(t, err)
			authRe, err := clt.Client.RoundTrip(func() (*http.Response, error) {
				return clt.Client.HTTPClient().Do(req)
			})

			require.NoError(t, err)
			// This route uses a meta redirect, so expect redirect URL in body instead of location header.
			require.Equal(t, http.StatusOK, authRe.Code(), "Response: %v", string(authRe.Bytes()))
			if tc.validSession {
				// we have got valid session
				require.NotEmpty(t, authRe.Headers().Get("Set-Cookie"))
			}
			require.Contains(t, string(authRe.Bytes()), tc.expectedRedirectURL)
		})
	}
}

func TestWebSessionsCRUD(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo")

	// make sure we can use client to make authenticated requests
	re, err := pack.clt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites"), url.Values{})
	require.NoError(t, err)

	var clusters []ui.Cluster
	require.NoError(t, json.Unmarshal(re.Bytes(), &clusters))

	// now delete session
	_, err = pack.clt.Delete(
		context.Background(),
		pack.clt.Endpoint("webapi", "sessions"))
	require.NoError(t, err)

	// subsequent requests trying to use this session will fail
	_, err = pack.clt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites"), url.Values{})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))
}

func TestCSRF(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	type input struct {
		reqToken    string
		cookieToken string
	}

	// create a valid user
	user := "csrfuser"
	pass := "abc123"
	otpSecret := base32.StdEncoding.EncodeToString([]byte("def456"))
	s.createUser(t, user, user, pass, otpSecret)

	// create a valid login form request
	validToken, err := totp.GenerateCode(otpSecret, time.Now())
	require.NoError(t, err)
	loginForm := CreateSessionReq{
		User:              user,
		Pass:              pass,
		SecondFactorToken: validToken,
	}

	encodedToken1 := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	encodedToken2 := "bf355921bbf3ef3672a03e410d4194077dfa5fe863c652521763b3e7f81e7b11"
	invalid := []input{
		{reqToken: encodedToken2, cookieToken: encodedToken1},
		{reqToken: "", cookieToken: encodedToken1},
		{reqToken: "", cookieToken: ""},
		{reqToken: encodedToken1, cookieToken: ""},
	}

	clt := s.client()

	// valid
	_, err = s.login(clt, encodedToken1, encodedToken1, loginForm)
	require.NoError(t, err)

	// invalid
	for i := range invalid {
		_, err := s.login(clt, invalid[i].cookieToken, invalid[i].reqToken, loginForm)
		require.Error(t, err)
		require.True(t, trace.IsAccessDenied(err))
	}
}

func TestPasswordChange(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo")

	// invalidate the token
	s.clock.Advance(1 * time.Minute)
	validToken, err := totp.GenerateCode(pack.otpSecret, s.clock.Now())
	require.NoError(t, err)

	req := changePasswordReq{
		OldPassword:       []byte("abc123"),
		NewPassword:       []byte("abc1234"),
		SecondFactorToken: validToken,
	}

	_, err = pack.clt.PutJSON(context.Background(), pack.clt.Endpoint("webapi", "users", "password"), req)
	require.NoError(t, err)
}

// TestValidateBearerToken tests that the bearer token's user name
// matches the user name on the cookie.
func TestValidateBearerToken(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack1 := proxy.authPack(t, "user1", nil /* roles */)
	pack2 := proxy.authPack(t, "user2", nil /* roles */)

	// Swap pack1's session token with pack2's sessionToken
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	pack1.clt = proxy.newClient(t, roundtrip.BearerAuth(pack2.session.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(&proxy.webURL, pack1.cookies)

	// Auth protected endpoint.
	req := changePasswordReq{}
	_, err = pack1.clt.PutJSON(context.Background(), pack1.clt.Endpoint("webapi", "users", "password"), req)
	require.True(t, trace.IsAccessDenied(err))
	require.True(t, strings.Contains(err.Error(), "bad bearer token"))
}

func TestWebSessionsBadInput(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	user := "bob"
	pass := "abc123"
	rawSecret := "def456"
	otpSecret := base32.StdEncoding.EncodeToString([]byte(rawSecret))

	err := s.server.Auth().UpsertPassword(user, []byte(pass))
	require.NoError(t, err)

	dev, err := services.NewTOTPDevice("otp", otpSecret, s.clock.Now())
	require.NoError(t, err)
	err = s.server.Auth().UpsertMFADevice(context.Background(), user, dev)
	require.NoError(t, err)

	// create valid token
	validToken, err := totp.GenerateCode(otpSecret, time.Now())
	require.NoError(t, err)

	clt := s.client()

	reqs := []CreateSessionReq{
		// empty request
		{},
		// missing user
		{
			Pass:              pass,
			SecondFactorToken: validToken,
		},
		// missing pass
		{
			User:              user,
			SecondFactorToken: validToken,
		},
		// bad pass
		{
			User:              user,
			Pass:              "bla bla",
			SecondFactorToken: validToken,
		},
		// bad otp token
		{
			User:              user,
			Pass:              pass,
			SecondFactorToken: "bad token",
		},
		// missing otp token
		{
			User: user,
			Pass: pass,
		},
	}
	for i, req := range reqs {
		t.Run(fmt.Sprintf("tc %v", i), func(t *testing.T) {
			_, err := clt.PostJSON(s.ctx, clt.Endpoint("webapi", "sessions"), req)
			require.Error(t, err)
			require.True(t, trace.IsAccessDenied(err))
		})
	}
}

type clusterNodesGetResponse struct {
	Items      []ui.Server `json:"items"`
	StartKey   string      `json:"startKey"`
	TotalCount int         `json:"totalCount"`
}

func TestClusterNodesGet(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, "test-user@example.com", nil /* roles */)

	// Get the node already added by `newWebPack`
	servers, err := env.server.Auth().GetNodes(context.Background(), apidefaults.Namespace)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	server1 := servers[0]

	// Add another node.
	server2, err := types.NewServerWithLabels("server2", types.KindNode, types.ServerSpecV2{}, map[string]string{"test-field": "test-value"})
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertNode(context.Background(), server2)
	require.NoError(t, err)

	// Get nodes from endpoint.
	clusterName := env.server.ClusterName()
	endpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "nodes")

	query := url.Values{"sort": []string{"name"}}

	// Get nodes.
	re, err := pack.clt.Get(context.Background(), endpoint, query)
	require.NoError(t, err)

	// Test response.
	res := clusterNodesGetResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &res))
	require.Len(t, res.Items, 2)
	require.Equal(t, 2, res.TotalCount)
	require.ElementsMatch(t, res.Items, []ui.Server{
		{
			ClusterName: clusterName,
			Name:        server1.GetName(),
			Hostname:    server1.GetHostname(),
			Tunnel:      server1.GetUseTunnel(),
			Addr:        server1.GetAddr(),
			Labels:      []ui.Label{},
			SSHLogins:   []string{pack.login},
		},
		{ClusterName: clusterName,
			Name:      "server2",
			Labels:    []ui.Label{{Name: "test-field", Value: "test-value"}},
			Tunnel:    false,
			SSHLogins: []string{pack.login}},
	})

	// Get nodes using shortcut.
	re, err = pack.clt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites", currentSiteShortcut, "nodes"), query)
	require.NoError(t, err)

	res2 := clusterNodesGetResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &res2))
	require.Len(t, res.Items, 2)
	require.Equal(t, res, res2)
}

type clusterAlertsGetResponse struct {
	Alerts []types.ClusterAlert `json:"alerts"`
}

func TestClusterAlertsGet(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)

	// generate alert
	alert, err := types.NewClusterAlert(
		"test-alert",
		"test alert message",
		types.WithAlertSeverity(0),
		types.WithAlertLabel(types.AlertOnLogin, "yes"),
		// AlertPermitAll is necessary because the alert is only shown to
		// admin clients by default.
		types.WithAlertLabel(types.AlertPermitAll, "yes"),
	)
	require.NoError(t, err)
	err = env.server.Auth().UpsertClusterAlert(context.Background(), alert)
	require.NoError(t, err)

	// get alerts.
	clusterName := env.server.ClusterName()
	pack := env.proxies[0].authPack(t, "test-user@example.com", nil)
	endpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "alerts")
	re, err := pack.clt.Get(context.Background(), endpoint, nil)
	require.NoError(t, err)

	alerts := clusterAlertsGetResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &alerts))
	require.Len(t, alerts.Alerts, 1)
}

func TestSiteNodeConnectInvalidSessionID(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	_, _, err := s.makeTerminal(t, s.authPack(t, "foo"), withSessionID("/../../../foo"))
	require.Error(t, err)
}

func TestResolveServerHostPort(t *testing.T) {
	t.Parallel()
	sampleNode := types.ServerV2{}
	sampleNode.SetName("eca53e45-86a9-11e7-a893-0242ac0a0101")
	sampleNode.Spec.Hostname = "nodehostname"

	// valid cases
	validCases := []struct {
		server       string
		nodes        []types.Server
		expectedHost string
		expectedPort int
	}{
		{
			server:       "localhost",
			expectedHost: "localhost",
			expectedPort: 0,
		},
		{
			server:       "localhost:8080",
			expectedHost: "localhost",
			expectedPort: 8080,
		},
		{
			server:       "eca53e45-86a9-11e7-a893-0242ac0a0101",
			nodes:        []types.Server{&sampleNode},
			expectedHost: "nodehostname",
			expectedPort: 0,
		},
	}

	// invalid cases
	invalidCases := []struct {
		server      string
		expectedErr string
	}{
		{
			server:      ":22",
			expectedErr: "empty hostname",
		},
		{
			server:      ":",
			expectedErr: "empty hostname",
		},
		{
			server:      "",
			expectedErr: "empty server name",
		},
		{
			server:      "host:",
			expectedErr: "invalid port",
		},
		{
			server:      "host:port",
			expectedErr: "invalid port",
		},
	}

	for _, testCase := range validCases {
		host, port, err := resolveServerHostPort(testCase.server, testCase.nodes)
		require.NoError(t, err, testCase.server)
		require.Equal(t, testCase.expectedHost, host, testCase.server)
		require.Equal(t, testCase.expectedPort, port, testCase.server)
	}

	for _, testCase := range invalidCases {
		_, _, err := resolveServerHostPort(testCase.server, nil)
		require.Error(t, err, testCase.server)
		require.Regexp(t, ".*"+testCase.expectedErr+".*", err.Error(), testCase.server)
	}
}

func TestNewTerminalHandler(t *testing.T) {
	ctx := context.Background()

	invalidCases := []struct {
		expectedErr string
		cfg         TerminalHandlerConfig
	}{
		{
			expectedErr: "sid: invalid session id",
			cfg: TerminalHandlerConfig{
				SessionData: session.Session{
					ID: session.ID("not a uuid"),
				},
			},
		}, {
			expectedErr: "login: missing login",
			cfg: TerminalHandlerConfig{
				SessionData: session.Session{
					ID:    session.NewID(),
					Login: "",
				},
			},
		}, {
			expectedErr: "server: missing server",
			cfg: TerminalHandlerConfig{
				SessionData: session.Session{
					ID:       session.NewID(),
					Login:    "root",
					ServerID: "",
				},
			},
		},
		{
			expectedErr: "term: bad dimensions(-1x0)",
			cfg: TerminalHandlerConfig{
				SessionData: session.Session{
					ID:       session.NewID(),
					Login:    "root",
					ServerID: uuid.New().String(),
				},
				Term: session.TerminalParams{
					W: -1,
					H: 0,
				},
			},
		},
		{
			expectedErr: "term: bad dimensions(1x4097)",
			cfg: TerminalHandlerConfig{
				SessionData: session.Session{
					ID:       session.NewID(),
					Login:    "root",
					ServerID: uuid.New().String(),
				},
				Term: session.TerminalParams{
					W: 1,
					H: 4097,
				},
			},
		},
	}

	for _, testCase := range invalidCases {
		_, err := NewTerminal(ctx, testCase.cfg)
		require.Equal(t, err.Error(), testCase.expectedErr)
	}

	validNode := types.ServerV2{}
	validNode.SetName("eca53e45-86a9-11e7-a893-0242ac0a0101")
	validNode.Spec.Hostname = "nodehostname"

	// Valid Case
	validCfg := TerminalHandlerConfig{
		Term: session.TerminalParams{
			W: 100,
			H: 100,
		},
		SessionCtx: &SessionContext{},
		AuthProvider: authProviderMock{
			server: validNode,
		},
		SessionData: session.Session{
			ID:       session.NewID(),
			Login:    "root",
			ServerID: uuid.New().String(),
		},
		KeepAliveInterval:  time.Duration(100),
		ProxyHostPort:      "1234",
		InteractiveCommand: make([]string, 1),
		DisplayLogin:       "tree",
		Router:             &proxy.Router{},
	}

	term, err := NewTerminal(ctx, validCfg)
	require.NoError(t, err)
	// passed through
	require.Equal(t, validCfg.SessionCtx, term.ctx)
	require.Equal(t, validCfg.AuthProvider, term.authProvider)
	require.Equal(t, validCfg.SessionData, term.sessionData)
	require.Equal(t, validCfg.KeepAliveInterval, term.keepAliveInterval)
	require.Equal(t, validCfg.ProxyHostPort, term.proxyHostPort)
	require.Equal(t, validCfg.InteractiveCommand, term.interactiveCommand)
	require.Equal(t, validCfg.Term, term.term)
	require.Equal(t, validCfg.DisplayLogin, term.displayLogin)
	// newly added
	require.NotNil(t, term.encoder)
	require.NotNil(t, term.decoder)
	require.NotNil(t, term.wsLock)
	require.NotNil(t, term.log)
}

func TestResizeTerminal(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	sid := session.NewID()

	errs := make(chan error, 2)
	readLoop := func(ctx context.Context, ws *websocket.Conn, ch chan<- *Envelope) {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			typ, b, err := ws.ReadMessage()
			if err != nil {
				errs <- err
				return
			}
			if typ != websocket.BinaryMessage {
				errs <- trace.BadParameter("expected binary message, got %v", typ)
				return
			}
			var envelope Envelope
			if err := proto.Unmarshal(b, &envelope); err != nil {
				errs <- trace.Wrap(err)
				return
			}
			ch <- &envelope
		}
	}

	// Create a new user "foo", open a terminal to a new session
	pack1 := s.authPack(t, "foo")
	ws1, sess, err := s.makeTerminal(t, pack1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws1.Close()) })

	// Create a new user "bar", open a terminal to the session created above
	pack2 := s.authPack(t, "bar")
	ws2, sess2, err := s.makeTerminal(t, pack2, withSessionID(sess.ID))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws2.Close()) })

	require.Equal(t, sess.ID, sess2.ID)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws1Messages := make(chan *Envelope)
	ws2Messages := make(chan *Envelope)
	go readLoop(ctx, ws1, ws1Messages)
	go readLoop(ctx, ws2, ws2Messages)

	// consume events from the first terminal
	// we exect to see at least one raw event with PTY data (indicating terminal ready)
	// and 2 resize events from the second user joining the session (one for the default
	// size, and one for the manual resize request)
	done := time.After(10 * time.Second)
	t1ResizeEvents, t1RawEvents := 0, 0
t1ready:
	for {
		select {
		case <-done:
			require.FailNowf(t, "", "expected to receive 2 resize events (got %d) and at least 1 raw event (got %d)", t1ResizeEvents, t1RawEvents)
		case err := <-errs:
			require.NoError(t, err)
		case e := <-ws1Messages:
			if isResizeEventEnvelope(e) {
				t1ResizeEvents++
			}
			if e.GetType() == defaults.WebsocketRaw {
				t1RawEvents++
			}
			if t1ResizeEvents == 2 && t1RawEvents > 0 {
				break t1ready
			}
		}
	}

	// we should not expect to see a resize event on terminal 2,
	// since they are not broadcasted back to the originator
	select {
	case e := <-ws2Messages:
		if isResizeEventEnvelope(e) {
			require.FailNow(t, "terminal 2 should not have received a resize event")
		}
	case err := <-errs:
		require.NoError(t, err)
	case <-time.After(1 * time.Second):
	}

	// Resize the second terminal. This should be reflected only on the first terminal
	// because resize events are sent to participants but not the originator..
	params, err := session.NewTerminalParamsFromInt(300, 120)
	require.NoError(t, err)
	data, err := json.Marshal(events.EventFields{
		events.EventType:      events.ResizeEvent,
		events.EventNamespace: apidefaults.Namespace,
		events.SessionEventID: sid.String(),
		events.TerminalSize:   params.Serialize(),
	})
	require.NoError(t, err)
	envelope := &Envelope{
		Version: defaults.WebsocketVersion,
		Type:    defaults.WebsocketResize,
		Payload: string(data),
	}
	envelopeBytes, err := proto.Marshal(envelope)
	require.NoError(t, err)
	err = ws2.WriteMessage(websocket.BinaryMessage, envelopeBytes)
	require.NoError(t, err)

	// the first terminal should see the resize event
	done = time.After(5 * time.Second)
	for {
		select {
		case <-done:
			require.FailNow(t, "expected to receive a final resize event")
		case err := <-errs:
			require.NoError(t, err)
		case e := <-ws1Messages:
			if isResizeEventEnvelope(e) {
				return
			}
		}
	}
}

func isResizeEventEnvelope(e *Envelope) bool {
	if e.GetType() != defaults.WebsocketAudit {
		return false
	}
	var ef events.EventFields
	if err := json.Unmarshal([]byte(e.GetPayload()), &ef); err != nil {
		return false
	}
	return ef.GetType() == events.ResizeEvent
}

// TestTerminalPing tests that the server sends continuous ping control messages.
func TestTerminalPing(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	ws, _, err := s.makeTerminal(t, s.authPack(t, "foo"), withKeepaliveInterval(500*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws.Close()) })

	closed := false
	done := make(chan struct{})
	ws.SetPingHandler(func(message string) error {
		if closed == false {
			close(done)
			closed = true
		}

		err := ws.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(time.Second))
		if err == websocket.ErrCloseSent {
			return nil
		} else if e, ok := err.(net.Error); ok && e.Timeout() {
			return nil
		}
		return err
	})

	// We need to continuously read incoming messages in order to process ping messages.
	// We only care about receiving a ping here so dropping them is fine.
	go func() {
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("timeout waiting for ping")
	}
}

func TestTerminal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		recordingConfig types.SessionRecordingConfigV2
	}{
		{
			name: "node recording mode",
			recordingConfig: types.SessionRecordingConfigV2{
				Spec: types.SessionRecordingConfigSpecV2{
					Mode: types.RecordAtNode,
				},
			},
		},
		{
			name: "proxy recording mode",
			recordingConfig: types.SessionRecordingConfigV2{
				Spec: types.SessionRecordingConfigSpecV2{
					Mode: types.RecordAtProxySync,
				},
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newWebSuite(t)

			require.NoError(t, s.server.Auth().SetSessionRecordingConfig(context.Background(), &tt.recordingConfig))

			ws, _, err := s.makeTerminal(t, s.authPack(t, "foo"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, ws.Close()) })

			termHandler := newTerminalHandler()
			stream := termHandler.asTerminalStream(ws)

			// here we intentionally run a command where the output we're looking
			// for is not present in the command itself
			_, err = io.WriteString(stream, "echo txlxport | sed 's/x/e/g'\r\n")
			require.NoError(t, err)
			require.NoError(t, waitForOutput(stream, "teleport"))
		})
	}
}

func TestTerminalRequireSessionMfa(t *testing.T) {
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, "llama", nil /* roles */)

	clt, err := env.server.NewClient(auth.TestUser("llama"))
	require.NoError(t, err)

	cases := []struct {
		name                      string
		getAuthPreference         func() types.AuthPreference
		registerDevice            func() *auth.TestDevice
		getChallengeResponseBytes func(chals *client.MFAAuthenticateChallenge, dev *auth.TestDevice) []byte
	}{
		{
			name: "with webauthn",
			getAuthPreference: func() types.AuthPreference {
				ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
					Type:         constants.Local,
					SecondFactor: constants.SecondFactorWebauthn,
					Webauthn: &types.Webauthn{
						RPID: "localhost",
					},
					RequireMFAType: types.RequireMFAType_SESSION,
				})
				require.NoError(t, err)

				return ap
			},
			registerDevice: func() *auth.TestDevice {
				webauthnDev, err := auth.RegisterTestDevice(ctx, clt, "webauthn", authproto.DeviceType_DEVICE_TYPE_WEBAUTHN, nil /* authenticator */)
				require.NoError(t, err)

				return webauthnDev
			},
			getChallengeResponseBytes: func(chals *client.MFAAuthenticateChallenge, dev *auth.TestDevice) []byte {
				res, err := dev.SolveAuthn(&authproto.MFAAuthenticateChallenge{
					WebauthnChallenge: wanlib.CredentialAssertionToProto(chals.WebauthnChallenge),
				})
				require.Nil(t, err)

				webauthnResBytes, err := json.Marshal(wanlib.CredentialAssertionResponseFromProto(res.GetWebauthn()))
				require.Nil(t, err)

				return webauthnResBytes
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err = env.server.Auth().SetAuthPreference(ctx, tc.getAuthPreference())
			require.NoError(t, err)

			dev := tc.registerDevice()

			// Open a terminal to a new session.
			ws, _ := proxy.makeTerminal(t, pack, "")

			// Wait for websocket authn challenge event.
			ty, raw, err := ws.ReadMessage()
			require.Nil(t, err)
			require.Equal(t, websocket.BinaryMessage, ty)
			var env Envelope
			require.Nil(t, proto.Unmarshal(raw, &env))

			chals := &client.MFAAuthenticateChallenge{}
			require.Nil(t, json.Unmarshal([]byte(env.Payload), &chals))

			// Send response over ws.
			termHandler := newTerminalHandler()
			_, err = termHandler.write(tc.getChallengeResponseBytes(chals, dev), ws)
			require.Nil(t, err)

			// Test we can write.
			stream := termHandler.asTerminalStream(ws)
			_, err = io.WriteString(stream, "echo alpacas\r\n")
			require.Nil(t, err)
			require.Nil(t, waitForOutput(stream, "alpacas"))
		})
	}
}

type windowsDesktopServiceMock struct {
	listener net.Listener
}

func mustStartWindowsDesktopMock(t *testing.T, authClient *auth.Server) *windowsDesktopServiceMock {
	l, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, l.Close())
	})
	authID := auth.IdentityID{
		Role:     types.RoleWindowsDesktop,
		HostUUID: "windows_server",
		NodeName: "windows_server",
	}
	n, err := authClient.GetClusterName()
	require.NoError(t, err)
	dns := []string{"localhost", "127.0.0.1", desktop.WildcardServiceDNS}
	identity, err := auth.LocalRegister(authID, authClient, nil, dns, "", nil)
	require.NoError(t, err)

	tlsConfig, err := identity.TLSConfig(nil)
	require.NoError(t, err)
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	require.NoError(t, err)

	ca, err := authClient.GetCertAuthority(context.Background(), types.CertAuthID{Type: types.UserCA, DomainName: n.GetClusterName()}, false)
	require.NoError(t, err)

	for _, kp := range services.GetTLSCerts(ca) {
		require.True(t, tlsConfig.ClientCAs.AppendCertsFromPEM(kp))
	}

	wd := &windowsDesktopServiceMock{
		listener: l,
	}
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		tlsConn := tls.Server(conn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			t.Errorf("Unexpected error %v", err)
			return
		}
		wd.handleConn(t, tlsConn)
	}()

	return wd
}

func (w *windowsDesktopServiceMock) handleConn(t *testing.T, conn *tls.Conn) {
	tdpConn := tdp.NewConn(conn)

	// Ensure that incoming connection is MFAVerified.
	require.NotEmpty(t, conn.ConnectionState().PeerCertificates)
	cert := conn.ConnectionState().PeerCertificates[0]
	identity, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	require.NoError(t, err)
	require.NotEmpty(t, identity.MFAVerified)

	msg, err := tdpConn.ReadMessage()
	require.NoError(t, err)
	require.IsType(t, tdp.ClientUsername{}, msg)

	msg, err = tdpConn.ReadMessage()
	require.NoError(t, err)
	require.IsType(t, tdp.ClientScreenSpec{}, msg)

	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	err = tdpConn.WriteMessage(tdp.NewPNG(img, tdp.PNGEncoder()))
	require.NoError(t, err)
}

func TestDesktopAccessMFARequiresMfa(t *testing.T) {
	tests := []struct {
		name           string
		authPref       types.AuthPreferenceSpecV2
		mfaHandler     func(t *testing.T, ws *websocket.Conn, dev *auth.TestDevice)
		registerDevice func(t *testing.T, ctx context.Context, clt *auth.Client) *auth.TestDevice
	}{
		{
			name: "webauthn",
			authPref: types.AuthPreferenceSpecV2{
				Type:         constants.Local,
				SecondFactor: constants.SecondFactorWebauthn,
				Webauthn: &types.Webauthn{
					RPID: "localhost",
				},
				RequireMFAType: types.RequireMFAType_SESSION,
			},
			mfaHandler: handleMFAWebauthnChallenge,
			registerDevice: func(t *testing.T, ctx context.Context, clt *auth.Client) *auth.TestDevice {
				webauthnDev, err := auth.RegisterTestDevice(ctx, clt, "webauthn", authproto.DeviceType_DEVICE_TYPE_WEBAUTHN, nil /* authenticator */)
				require.NoError(t, err)
				return webauthnDev
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newWebPack(t, 1)
			proxy := env.proxies[0]
			pack := proxy.authPack(t, "llama", nil /* roles */)

			clt, err := env.server.NewClient(auth.TestUser("llama"))
			require.NoError(t, err)
			wdID := uuid.New().String()

			wdMock := mustStartWindowsDesktopMock(t, env.server.Auth())
			wd, err := types.NewWindowsDesktopV3("desktop1", nil, types.WindowsDesktopSpecV3{
				Addr:   wdMock.listener.Addr().String(),
				Domain: "CORP",
				HostID: wdID,
			})
			require.NoError(t, err)

			err = env.server.Auth().UpsertWindowsDesktop(context.Background(), wd)
			require.NoError(t, err)
			wds, err := types.NewWindowsDesktopServiceV3(types.Metadata{Name: wdID}, types.WindowsDesktopServiceSpecV3{
				Addr:            wdMock.listener.Addr().String(),
				TeleportVersion: teleport.Version,
			})
			require.NoError(t, err)

			_, err = env.server.Auth().UpsertWindowsDesktopService(context.Background(), wds)
			require.NoError(t, err)

			ap, err := types.NewAuthPreference(tc.authPref)
			require.NoError(t, err)
			err = env.server.Auth().SetAuthPreference(ctx, ap)
			require.NoError(t, err)

			dev := tc.registerDevice(t, ctx, clt)

			ws := proxy.makeDesktopSession(t, pack, session.NewID(), env.server.TLS.Listener.Addr())
			tc.mfaHandler(t, ws, dev)

			tdpClient := tdp.NewConn(&WebsocketIO{Conn: ws})

			msg, err := tdpClient.ReadMessage()
			require.NoError(t, err)
			require.IsType(t, tdp.PNG2Frame{}, msg)
		})
	}
}

func handleMFAWebauthnChallenge(t *testing.T, ws *websocket.Conn, dev *auth.TestDevice) {
	br := bufio.NewReader(&WebsocketIO{Conn: ws})
	mt, err := br.ReadByte()
	require.NoError(t, err)
	require.Equal(t, tdp.TypeMFA, tdp.MessageType(mt))

	mfaChallange, err := tdp.DecodeMFAChallenge(br)
	require.NoError(t, err)
	res, err := dev.SolveAuthn(&authproto.MFAAuthenticateChallenge{
		WebauthnChallenge: wanlib.CredentialAssertionToProto(mfaChallange.WebauthnChallenge),
	})
	require.NoError(t, err)
	err = tdp.NewConn(&WebsocketIO{Conn: ws}).WriteMessage(tdp.MFA{
		Type: defaults.WebsocketWebauthnChallenge[0],
		MFAAuthenticateResponse: &authproto.MFAAuthenticateResponse{
			Response: &authproto.MFAAuthenticateResponse_Webauthn{
				Webauthn: res.GetWebauthn(),
			},
		},
	})
	require.NoError(t, err)
}

func TestWebAgentForward(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	ws, _, err := s.makeTerminal(t, s.authPack(t, "foo"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws.Close()) })

	termHandler := newTerminalHandler()
	stream := termHandler.asTerminalStream(ws)

	_, err = io.WriteString(stream, "echo $SSH_AUTH_SOCK\r\n")
	require.NoError(t, err)

	err = waitForOutput(stream, "/")
	require.NoError(t, err)
}

func TestActiveSessions(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo")

	start := time.Now()
	kinds := []types.SessionKind{
		types.SSHSessionKind,
		types.KubernetesSessionKind,
		types.WindowsDesktopSessionKind,
		types.DatabaseSessionKind,
		types.AppSessionKind,
	}
	ids := make(map[string]struct{})

	for _, kind := range kinds {
		tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
			SessionID:    string(session.NewID()),
			ClusterName:  s.server.ClusterName(),
			Kind:         string(kind),
			State:        types.SessionState_SessionStateRunning,
			Created:      start,
			Expires:      start.Add(1 * time.Hour),
			Hostname:     s.node.GetInfo().GetHostname(),
			DesktopName:  s.node.GetInfo().GetHostname(),
			AppName:      s.node.GetInfo().GetHostname(),
			DatabaseName: s.node.GetInfo().GetHostname(),
			Address:      s.srvID,
			Login:        pack.login,
			Participants: []types.Participant{
				{ID: "id", User: "user-1", LastActive: start},
			},
		})
		require.NoError(t, err)
		ids[tracker.GetSessionID()] = struct{}{}

		_, err = s.server.Auth().CreateSessionTracker(context.Background(), tracker)
		require.NoError(t, err)
	}

	// create an inactive session, which should not show up
	inactive, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
		SessionID:    string(session.NewID()),
		ClusterName:  s.server.ClusterName(),
		Kind:         string(types.SSHSessionKind),
		State:        types.SessionState_SessionStateTerminated,
		Created:      time.Now(),
		Expires:      time.Now().Add(1 * time.Hour),
		Hostname:     s.node.GetInfo().GetHostname(),
		Address:      s.srvID,
		Login:        pack.login,
		Participants: nil,
	})
	require.NoError(t, err)
	_, err = s.server.Auth().CreateSessionTracker(context.Background(), inactive)
	require.NoError(t, err)

	re, err := pack.clt.Get(s.ctx, pack.clt.Endpoint("webapi", "sites", s.server.ClusterName(), "sessions"), url.Values{})
	require.NoError(t, err)

	var sessResp siteSessionsGetResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &sessResp))
	require.Len(t, sessResp.Sessions, len(kinds))

	for _, session := range sessResp.Sessions {
		require.Contains(t, ids, string(session.ID))
		require.Equal(t, s.node.GetNamespace(), session.Namespace)
		require.NotNil(t, session.Parties)
		require.Greater(t, session.TerminalParams.H, 0)
		require.Greater(t, session.TerminalParams.W, 0)
		require.Equal(t, pack.login, session.Login)
		require.False(t, session.Created.IsZero())
		require.False(t, session.LastActive.IsZero())
		require.Equal(t, s.srvID, session.ServerID)
		require.Equal(t, s.node.GetInfo().GetHostname(), session.ServerHostname)
		require.Equal(t, s.srvID, session.ServerAddr)
		require.Equal(t, s.server.ClusterName(), session.ClusterName)
	}
}

func TestCloseConnectionsOnLogout(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo")

	ws, _, err := s.makeTerminal(t, pack)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws.Close()) })

	termHandler := newTerminalHandler()
	stream := termHandler.asTerminalStream(ws)

	// to make sure we have a session
	_, err = io.WriteString(stream, "expr 137 + 39\r\n")
	require.NoError(t, err)

	// make sure server has replied
	out := make([]byte, 100)
	_, err = stream.Read(out)
	require.NoError(t, err)

	_, err = pack.clt.Delete(s.ctx, pack.clt.Endpoint("webapi", "sessions"))
	require.NoError(t, err)

	// wait until we timeout or detect that connection has been closed
	after := time.After(5 * time.Second)
	errC := make(chan error)
	go func() {
		for {
			_, err := stream.Read(out)
			if err != nil {
				errC <- err
			}
		}
	}()

	select {
	case <-after:
		t.Fatalf("timeout")
	case err := <-errC:
		require.ErrorIs(t, err, io.EOF)
	}
}

func TestCreateSession(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	user := "test-user@example.com"
	pack := proxy.authPack(t, user, nil /* roles */)

	// get site nodes
	re, err := pack.clt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "nodes"), url.Values{})
	require.NoError(t, err)

	nodes := clusterNodesGetResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &nodes))
	node := nodes.Items[0]

	sess := session.Session{
		TerminalParams: session.TerminalParams{W: 300, H: 120},
		Login:          user,
	}

	// test using node UUID
	sess.ServerID = node.Name
	re, err = pack.clt.PostJSON(
		context.Background(),
		pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "sessions"),
		siteSessionGenerateReq{Session: sess},
	)
	require.NoError(t, err)

	var created *siteSessionGenerateResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &created))
	require.NotEmpty(t, created.Session.ID)
	require.Equal(t, node.Hostname, created.Session.ServerHostname)

	// test empty serverID (older version does not supply serverID)
	sess.ServerID = ""
	_, err = pack.clt.PostJSON(
		context.Background(),
		pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "sessions"),
		siteSessionGenerateReq{Session: sess},
	)
	require.NoError(t, err)
}

func TestPlayback(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo")
	ws, _, err := s.makeTerminal(t, pack)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ws.Close()) })
}

type httpErrorMessage struct {
	Message string `json:"message"`
}

type httpErrorResponse struct {
	Error httpErrorMessage `json:"error"`
}

func TestLogin_PrivateKeyEnabledError(t *testing.T) {
	modules.SetTestModules(t, &modules.TestModules{
		MockAttestHardwareKey: func(_ context.Context, _ interface{}, policy keys.PrivateKeyPolicy, _ *keys.AttestationStatement, _ crypto.PublicKey, _ time.Duration) (keys.PrivateKeyPolicy, error) {
			return "", keys.NewPrivateKeyPolicyError(policy)
		},
	})
	s := newWebSuite(t)
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:           constants.Local,
		SecondFactor:   constants.SecondFactorOff,
		RequireMFAType: types.RequireMFAType_HARDWARE_KEY_TOUCH,
	})
	require.NoError(t, err)
	err = s.server.Auth().SetAuthPreference(s.ctx, ap)
	require.NoError(t, err)

	// create user
	s.createUser(t, "user1", "root", "password", "")

	loginReq, err := json.Marshal(CreateSessionReq{
		User: "user1",
		Pass: "password",
	})
	require.NoError(t, err)

	clt := s.client()
	req, err := http.NewRequest("POST", clt.Endpoint("webapi", "sessions"), bytes.NewBuffer(loginReq))
	require.NoError(t, err)
	ua := "test-ua"
	req.Header.Set("User-Agent", ua)
	csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	addCSRFCookieToReq(req, csrfToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrf.HeaderName, csrfToken)

	re, err := clt.Client.RoundTrip(func() (*http.Response, error) {
		return clt.Client.HTTPClient().Do(req)
	})
	require.NoError(t, err)
	var resErr httpErrorResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &resErr))
	require.Contains(t, resErr.Error.Message, keys.PrivateKeyPolicyHardwareKeyTouch)
}

func TestLogin(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOff,
	})
	require.NoError(t, err)
	err = s.server.Auth().SetAuthPreference(s.ctx, ap)
	require.NoError(t, err)

	// create user
	s.createUser(t, "user1", "root", "password", "")

	loginReq, err := json.Marshal(CreateSessionReq{
		User: "user1",
		Pass: "password",
	})
	require.NoError(t, err)

	clt := s.client()
	ua := "test-ua"
	req, err := http.NewRequest("POST", clt.Endpoint("webapi", "sessions"), bytes.NewBuffer(loginReq))
	require.NoError(t, err)
	req.Header.Set("User-Agent", ua)

	csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	addCSRFCookieToReq(req, csrfToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrf.HeaderName, csrfToken)

	re, err := clt.Client.RoundTrip(func() (*http.Response, error) {
		return clt.Client.HTTPClient().Do(req)
	})
	require.NoError(t, err)

	events, _, err := s.server.AuthServer.AuditLog.SearchEvents(
		s.clock.Now().Add(-time.Hour),
		s.clock.Now().Add(time.Hour),
		apidefaults.Namespace,
		[]string{events.UserLoginEvent},
		1,
		types.EventOrderDescending,
		"",
	)
	require.NoError(t, err)
	event := events[0].(*apievents.UserLogin)
	require.Equal(t, true, event.Success)
	require.Equal(t, ua, event.UserAgent)
	require.True(t, strings.HasPrefix(event.RemoteAddr, "127.0.0.1:"))

	var rawSess *CreateSessionResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &rawSess))
	cookies := re.Cookies()
	require.Len(t, cookies, 1)

	// now make sure we are logged in by calling authenticated method
	// we need to supply both session cookie and bearer token for
	// request to succeed
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	clt = s.client(roundtrip.BearerAuth(rawSess.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(s.url(), re.Cookies())

	re, err = clt.Get(s.ctx, clt.Endpoint("webapi", "sites"), url.Values{})
	require.NoError(t, err)

	var clusters []ui.Cluster
	require.NoError(t, json.Unmarshal(re.Bytes(), &clusters))

	// in absence of session cookie or bearer auth the same request fill fail

	// no session cookie:
	clt = s.client(roundtrip.BearerAuth(rawSess.Token))
	_, err = clt.Get(s.ctx, clt.Endpoint("webapi", "sites"), url.Values{})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	// no bearer token:
	clt = s.client(roundtrip.CookieJar(jar))
	_, err = clt.Get(s.ctx, clt.Endpoint("webapi", "sites"), url.Values{})
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))
}

// TestEmptyMotD ensures that responses returned by both /webapi/ping and
// /webapi/motd work when no MotD is set
func TestEmptyMotD(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	wc := s.client()

	// Given an auth server configured *not* to expose a Message Of The
	// Day...

	// When I issue a ping request...
	re, err := wc.Get(s.ctx, wc.Endpoint("webapi", "ping"), url.Values{})
	require.NoError(t, err)

	// Expect that the MotD flag in the ping response is *not* set
	var pingResponse *webclient.PingResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &pingResponse))
	require.False(t, pingResponse.Auth.HasMessageOfTheDay)

	// When I fetch the MotD...
	re, err = wc.Get(s.ctx, wc.Endpoint("webapi", "motd"), url.Values{})
	require.NoError(t, err)

	// Expect that an empty response returned
	var motdResponse *webclient.MotD
	require.NoError(t, json.Unmarshal(re.Bytes(), &motdResponse))
	require.Empty(t, motdResponse.Text)
}

// TestMotD ensures that a response is returned by both /webapi/ping and /webapi/motd
// and that that the response bodies contain their MOTD components
func TestMotD(t *testing.T) {
	t.Parallel()
	const motd = "Hello. I'm a Teleport cluster!"

	s := newWebSuite(t)
	wc := s.client()

	// Given an auth server configured to expose a Message Of The Day...
	prefs := types.DefaultAuthPreference()
	prefs.SetMessageOfTheDay(motd)
	require.NoError(t, s.server.AuthServer.AuthServer.SetAuthPreference(s.ctx, prefs))

	// When I issue a ping request...
	re, err := wc.Get(s.ctx, wc.Endpoint("webapi", "ping"), url.Values{})
	require.NoError(t, err)

	// Expect that the MotD flag in the ping response is set to indicate
	// a MotD
	var pingResponse *webclient.PingResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &pingResponse))
	require.True(t, pingResponse.Auth.HasMessageOfTheDay)

	// When I fetch the MotD...
	re, err = wc.Get(s.ctx, wc.Endpoint("webapi", "motd"), url.Values{})
	require.NoError(t, err)

	// Expect that the text returned is the configured value
	var motdResponse *webclient.MotD
	require.NoError(t, json.Unmarshal(re.Bytes(), &motdResponse))
	require.Equal(t, motd, motdResponse.Text)
}

func TestMultipleConnectors(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	wc := s.client()

	// create two oidc connectors, one named "foo" and another named "bar"
	oidcConnectorSpec := types.OIDCConnectorSpecV3{
		RedirectURLs: []string{"https://localhost:3080/v1/webapi/oidc/callback"},
		ClientID:     "000000000000-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com",
		ClientSecret: "AAAAAAAAAAAAAAAAAAAAAAAA",
		IssuerURL:    "https://oidc.example.com",
		Display:      "Login with Example",
		Scope:        []string{"group"},
		ClaimsToRoles: []types.ClaimMapping{
			{
				Claim: "group",
				Value: "admin",
				Roles: []string{"admin"},
			},
		},
	}
	o, err := types.NewOIDCConnector("foo", oidcConnectorSpec)
	require.NoError(t, err)
	err = s.server.Auth().UpsertOIDCConnector(s.ctx, o)
	require.NoError(t, err)
	o2, err := types.NewOIDCConnector("bar", oidcConnectorSpec)
	require.NoError(t, err)
	err = s.server.Auth().UpsertOIDCConnector(s.ctx, o2)
	require.NoError(t, err)

	// set the auth preferences to oidc with no connector name
	authPreference, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type: "oidc",
	})
	require.NoError(t, err)
	err = s.server.Auth().SetAuthPreference(s.ctx, authPreference)
	require.NoError(t, err)

	// hit the ping endpoint to get the auth type and connector name
	re, err := wc.Get(s.ctx, wc.Endpoint("webapi", "ping"), url.Values{})
	require.NoError(t, err)
	var out *webclient.PingResponse
	require.NoError(t, json.Unmarshal(re.Bytes(), &out))

	// make sure the connector name we got back was the first connector
	// in the backend, in this case it's "bar"
	oidcConnectors, err := s.server.Auth().GetOIDCConnectors(s.ctx, false)
	require.NoError(t, err)
	require.Equal(t, oidcConnectors[0].GetName(), out.Auth.OIDC.Name)

	// update the auth preferences and this time specify the connector name
	authPreference, err = types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:          "oidc",
		ConnectorName: "foo",
	})
	require.NoError(t, err)
	err = s.server.Auth().SetAuthPreference(s.ctx, authPreference)
	require.NoError(t, err)

	// hit the ping endpoing to get the auth type and connector name
	re, err = wc.Get(s.ctx, wc.Endpoint("webapi", "ping"), url.Values{})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(re.Bytes(), &out))

	// make sure the connector we get back is "foo"
	require.Equal(t, "foo", out.Auth.OIDC.Name)
}

// TestConstructSSHResponse checks if the secret package uses AES-GCM to
// encrypt and decrypt data that passes through the ConstructSSHResponse
// function.
func TestConstructSSHResponse(t *testing.T) {
	key, err := secret.NewKey()
	require.NoError(t, err)

	u, err := url.Parse("http://www.example.com/callback")
	require.NoError(t, err)
	query := u.Query()
	query.Set("secret_key", key.String())
	u.RawQuery = query.Encode()

	rawresp, err := ConstructSSHResponse(AuthParams{
		Username:          "foo",
		Cert:              []byte{0x00},
		TLSCert:           []byte{0x01},
		ClientRedirectURL: u.String(),
	})
	require.NoError(t, err)

	require.Empty(t, rawresp.Query().Get("secret"))
	require.Empty(t, rawresp.Query().Get("secret_key"))
	require.NotEmpty(t, rawresp.Query().Get("response"))

	plaintext, err := key.Open([]byte(rawresp.Query().Get("response")))
	require.NoError(t, err)

	var resp *auth.SSHLoginResponse
	err = json.Unmarshal(plaintext, &resp)
	require.NoError(t, err)
	require.Equal(t, "foo", resp.Username)
	require.EqualValues(t, []byte{0x00}, resp.Cert)
	require.EqualValues(t, []byte{0x01}, resp.TLSCert)
}

// TestConstructSSHResponseLegacy checks if the secret package uses NaCl to
// encrypt and decrypt data that passes through the ConstructSSHResponse
// function.
func TestConstructSSHResponseLegacy(t *testing.T) {
	key, err := lemma_secret.NewKey()
	require.NoError(t, err)

	lemma, err := lemma_secret.New(&lemma_secret.Config{KeyBytes: key})
	require.NoError(t, err)

	u, err := url.Parse("http://www.example.com/callback")
	require.NoError(t, err)
	query := u.Query()
	query.Set("secret", lemma_secret.KeyToEncodedString(key))
	u.RawQuery = query.Encode()

	rawresp, err := ConstructSSHResponse(AuthParams{
		Username:          "foo",
		Cert:              []byte{0x00},
		TLSCert:           []byte{0x01},
		ClientRedirectURL: u.String(),
	})
	require.NoError(t, err)

	require.Empty(t, rawresp.Query().Get("secret"))
	require.Empty(t, rawresp.Query().Get("secret_key"))
	require.NotEmpty(t, rawresp.Query().Get("response"))

	var sealedData *lemma_secret.SealedBytes
	err = json.Unmarshal([]byte(rawresp.Query().Get("response")), &sealedData)
	require.NoError(t, err)

	plaintext, err := lemma.Open(sealedData)
	require.NoError(t, err)

	var resp *auth.SSHLoginResponse
	err = json.Unmarshal(plaintext, &resp)
	require.NoError(t, err)
	require.Equal(t, "foo", resp.Username)
	require.EqualValues(t, []byte{0x00}, resp.Cert)
	require.EqualValues(t, []byte{0x01}, resp.TLSCert)
}

type byTimeAndIndex []apievents.AuditEvent

func (f byTimeAndIndex) Len() int {
	return len(f)
}

func (f byTimeAndIndex) Less(i, j int) bool {
	itime := f[i].GetTime()
	jtime := f[j].GetTime()
	if itime.Equal(jtime) && events.GetSessionID(f[i]) == events.GetSessionID(f[j]) {
		return f[i].GetIndex() < f[j].GetIndex()
	}
	return itime.Before(jtime)
}

func (f byTimeAndIndex) Swap(i, j int) {
	f[i], f[j] = f[j], f[i]
}

// TestSearchClusterEvents makes sure web API allows querying events by type.
func TestSearchClusterEvents(t *testing.T) {
	t.Parallel()

	s := newWebSuite(t)
	clock := s.clock
	sessionEvents := events.GenerateTestSession(events.SessionParams{
		PrintEvents: 3,
		Clock:       clock,
		ServerID:    s.proxy.ID(),
	})

	for _, e := range sessionEvents {
		require.NoError(t, s.proxyClient.EmitAuditEvent(s.ctx, e))
	}

	sort.Sort(sort.Reverse(byTimeAndIndex(sessionEvents)))
	sessionStart := sessionEvents[0]
	sessionPrint := sessionEvents[1]
	sessionEnd := sessionEvents[4]

	fromTime := []string{clock.Now().AddDate(0, -1, 0).UTC().Format(time.RFC3339)}
	toTime := []string{clock.Now().AddDate(0, 1, 0).UTC().Format(time.RFC3339)}

	testCases := []struct {
		// Comment is the test case description.
		Comment string
		// Query is the search query sent to the API.
		Query url.Values
		// Result is the expected returned list of events.
		Result []apievents.AuditEvent
		// TestStartKey is a flag to test start key value.
		TestStartKey bool
		// StartKeyValue is the value of start key to expect.
		StartKeyValue string
	}{
		{
			Comment: "Empty query",
			Query: url.Values{
				"from": fromTime,
				"to":   toTime,
			},
			Result: sessionEvents,
		},
		{
			Comment: "Query by session start event",
			Query: url.Values{
				"include": []string{sessionStart.GetType()},
				"from":    fromTime,
				"to":      toTime,
			},
			Result: sessionEvents[:1],
		},
		{
			Comment: "Query session start and session end events",
			Query: url.Values{
				"include": []string{sessionEnd.GetType() + "," + sessionStart.GetType()},
				"from":    fromTime,
				"to":      toTime,
			},
			Result: []apievents.AuditEvent{sessionStart, sessionEnd},
		},
		{
			Comment: "Query events with filter by type and limit",
			Query: url.Values{
				"include": []string{sessionPrint.GetType() + "," + sessionEnd.GetType()},
				"limit":   []string{"1"},
				"from":    fromTime,
				"to":      toTime,
			},
			Result: []apievents.AuditEvent{sessionPrint},
		},
		{
			Comment: "Query session start and session end events with limit and test returned start key",
			Query: url.Values{
				"include": []string{sessionEnd.GetType() + "," + sessionStart.GetType()},
				"limit":   []string{"1"},
				"from":    fromTime,
				"to":      toTime,
			},
			Result:        []apievents.AuditEvent{sessionStart},
			TestStartKey:  true,
			StartKeyValue: sessionStart.GetID(),
		},
		{
			Comment: "Query session start and session end events with limit and given start key",
			Query: url.Values{
				"include":  []string{sessionEnd.GetType() + "," + sessionStart.GetType()},
				"startKey": []string{sessionStart.GetID()},
				"from":     fromTime,
				"to":       toTime,
			},
			Result:        []apievents.AuditEvent{sessionEnd},
			TestStartKey:  true,
			StartKeyValue: "",
		},
	}

	pack := s.authPack(t, "foo")
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Comment, func(t *testing.T) {
			t.Parallel()
			response, err := pack.clt.Get(s.ctx, pack.clt.Endpoint("webapi", "sites", s.server.ClusterName(), "events", "search"), tc.Query)
			require.NoError(t, err)
			var result eventsListGetResponse
			require.NoError(t, json.Unmarshal(response.Bytes(), &result))

			// filter out irrelvant auth events
			filteredEvents := []events.EventFields{}
			for _, e := range result.Events {
				t := e.GetType()
				if t == events.SessionStartEvent ||
					t == events.SessionPrintEvent ||
					t == events.SessionEndEvent {
					filteredEvents = append(filteredEvents, e)
				}
			}

			require.Len(t, filteredEvents, len(tc.Result))
			for i, resultEvent := range filteredEvents {
				require.Equal(t, tc.Result[i].GetType(), resultEvent.GetType())
				require.Equal(t, tc.Result[i].GetID(), resultEvent.GetID())
			}

			// Session prints do not have IDs, only sessionStart and sessionEnd.
			// When retrieving events for sessionStart and sessionEnd, sessionStart is returned first.
			if tc.TestStartKey {
				require.Equal(t, tc.StartKeyValue, result.StartKey)
			}
		})
	}
}

func TestGetClusterDetails(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	site, err := s.proxyTunnel.GetSite(s.server.ClusterName())
	require.NoError(t, err)
	require.NotNil(t, site)

	cluster, err := ui.GetClusterDetails(s.ctx, site)
	require.NoError(t, err)
	require.Equal(t, s.server.ClusterName(), cluster.Name)
	require.Equal(t, teleport.Version, cluster.ProxyVersion)
	require.Equal(t, fmt.Sprintf("%v:%v", s.server.ClusterName(), defaults.HTTPListenPort), cluster.PublicURL)
	require.Equal(t, teleport.RemoteClusterStatusOnline, cluster.Status)
	require.NotNil(t, cluster.LastConnected)
	require.Equal(t, teleport.Version, cluster.AuthVersion)

	nodes, err := s.proxyClient.GetNodes(s.ctx, apidefaults.Namespace)
	require.NoError(t, err)
	require.Len(t, nodes, cluster.NodeCount)
}

func TestTokenGeneration(t *testing.T) {
	const username = "test-user@example.com"
	// Users should be able to create Tokens even if they can't update them
	roleTokenCRD, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindToken,
					[]string{types.VerbCreate, types.VerbRead}),
			},
		},
	})
	require.NoError(t, err)

	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, username, []types.Role{roleTokenCRD})
	endpoint := pack.clt.Endpoint("webapi", "token")

	tt := []struct {
		name                        string
		roles                       types.SystemRoles
		shouldErr                   bool
		joinMethod                  types.JoinMethod
		suggestedAgentMatcherLabels types.Labels
		allow                       []*types.TokenRule
	}{
		{
			name:      "single node role",
			roles:     types.SystemRoles{types.RoleNode},
			shouldErr: false,
		},
		{
			name:      "single app role",
			roles:     types.SystemRoles{types.RoleApp},
			shouldErr: false,
		},
		{
			name:      "single db role",
			roles:     types.SystemRoles{types.RoleDatabase},
			shouldErr: false,
		},
		{
			name:      "multiple roles",
			roles:     types.SystemRoles{types.RoleNode, types.RoleApp, types.RoleDatabase},
			shouldErr: false,
		},
		{
			name:      "return error if no role is requested",
			roles:     types.SystemRoles{},
			shouldErr: true,
		},
		{
			name:       "cannot request token with IAM join method without allow field",
			roles:      types.SystemRoles{types.RoleNode},
			joinMethod: types.JoinMethodIAM,
			shouldErr:  true,
		},
		{
			name:       "can request token with IAM join method",
			roles:      types.SystemRoles{types.RoleNode},
			joinMethod: types.JoinMethodIAM,
			allow:      []*types.TokenRule{{AWSAccount: "1234"}},
			shouldErr:  false,
		},
		{
			name:  "adds the agent match labels",
			roles: types.SystemRoles{types.RoleDatabase},
			suggestedAgentMatcherLabels: types.Labels{
				"*": apiutils.Strings{"*"},
			},
			shouldErr: false,
		},
	}

	for _, tc := range tt {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			re, err := pack.clt.PostJSON(context.Background(), endpoint, types.ProvisionTokenSpecV2{
				Roles:                       tc.roles,
				JoinMethod:                  tc.joinMethod,
				Allow:                       tc.allow,
				SuggestedAgentMatcherLabels: tc.suggestedAgentMatcherLabels,
			})

			if tc.shouldErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			var responseToken nodeJoinToken
			err = json.Unmarshal(re.Bytes(), &responseToken)
			require.NoError(t, err)

			require.NotEmpty(t, responseToken.SuggestedLabels)
			require.Condition(t, func() (success bool) {
				for _, uiLabel := range responseToken.SuggestedLabels {
					if uiLabel.Name == types.InternalResourceIDLabel && uiLabel.Value != "" {
						return true
					}
				}
				return false
			})

			// generated token roles should match the requested ones
			generatedToken, err := proxy.auth.Auth().GetToken(context.Background(), responseToken.ID)
			require.NoError(t, err)
			require.Equal(t, tc.roles, generatedToken.GetRoles())

			expectedJoinMethod := tc.joinMethod
			if tc.joinMethod == "" {
				expectedJoinMethod = types.JoinMethodToken
			}
			// if no joinMethod is provided, expect token method
			require.Equal(t, expectedJoinMethod, generatedToken.GetJoinMethod())

			require.Equal(t, tc.suggestedAgentMatcherLabels, generatedToken.GetSuggestedAgentMatcherLabels())
		})
	}
}

func TestInstallDatabaseScriptGeneration(t *testing.T) {
	const username = "test-user@example.com"

	// Users should be able to create Tokens even if they can't update them
	roleTokenCRD, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindToken,
					[]string{types.VerbCreate, types.VerbRead}),
			},
		},
	})
	require.NoError(t, err)

	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, username, []types.Role{roleTokenCRD})

	// Create a new token with the desired SuggestedAgentMatcherLabels
	endpointGenerateToken := pack.clt.Endpoint("webapi", "token")
	re, err := pack.clt.PostJSON(
		context.Background(),
		endpointGenerateToken,
		types.ProvisionTokenSpecV2{
			Roles: types.SystemRoles{types.RoleDatabase},
			SuggestedAgentMatcherLabels: types.Labels{
				"stage": apiutils.Strings{"prod"},
			},
		})
	require.NoError(t, err)

	var responseToken nodeJoinToken
	require.NoError(t, json.Unmarshal(re.Bytes(), &responseToken))

	// Generating the script with the token should return the SuggestedAgentMatcherLabels provided in the first request
	endpointInstallDatabase := pack.clt.Endpoint("scripts", responseToken.ID, "install-database.sh")

	t.Log(responseToken, endpointInstallDatabase)
	req, err := http.NewRequest(http.MethodGet, endpointInstallDatabase, nil)
	require.NoError(t, err)

	anonHTTPClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := anonHTTPClient.Do(req)
	require.NoError(t, err)

	scriptBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.NoError(t, resp.Body.Close())

	script := string(scriptBytes)

	// It contains the agenbtMatchLabels
	require.Contains(t, script, "stage: prod")
}

func TestSignMTLS(t *testing.T) {
	env := newWebPack(t, 1)
	clusterName := env.server.ClusterName()

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "test-user@example.com", nil)

	endpoint := pack.clt.Endpoint("webapi", "token")
	re, err := pack.clt.PostJSON(context.Background(), endpoint, types.ProvisionTokenSpecV2{
		Roles: types.SystemRoles{types.RoleDatabase},
	})
	require.NoError(t, err)

	var responseToken nodeJoinToken
	err = json.Unmarshal(re.Bytes(), &responseToken)
	require.NoError(t, err)

	// download mTLS files from /webapi/sites/:site/sign/db
	endpointSign := pack.clt.Endpoint("webapi", "sites", clusterName, "sign", "db")

	bs, err := json.Marshal(struct {
		Hostname string `json:"hostname"`
		TTL      string `json:"ttl"`
	}{
		Hostname: "mypg.example.com",
		TTL:      "2h",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, endpointSign, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+responseToken.ID)

	anonHTTPClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := anonHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	gzipReader, err := gzip.NewReader(resp.Body)
	require.NoError(t, err)

	tarReader := tar.NewReader(gzipReader)

	tarContentFileNames := []string{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, byte(tar.TypeReg), header.Typeflag)
		require.Equal(t, int64(0600), header.Mode)
		tarContentFileNames = append(tarContentFileNames, header.Name)
	}

	expectedFileNames := []string{"server.cas", "server.key", "server.crt"}
	require.ElementsMatch(t, tarContentFileNames, expectedFileNames)

	// the token is no longer valid, so trying again should return an error
	req, err = http.NewRequest(http.MethodPost, endpointSign, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+responseToken.ID)

	respSecondCall, err := anonHTTPClient.Do(req)
	require.NoError(t, err)
	defer respSecondCall.Body.Close()
	require.Equal(t, http.StatusForbidden, respSecondCall.StatusCode)
}

func TestSignMTLS_failsAccessDenied(t *testing.T) {
	env := newWebPack(t, 1)
	clusterName := env.server.ClusterName()
	username := "test-user@example.com"

	roleUserUpdate, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindUser, []string{types.VerbUpdate}),
				types.NewRule(types.KindToken, []string{types.VerbCreate}),
			},
		},
	})
	require.NoError(t, err)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, username, []types.Role{roleUserUpdate})

	endpoint := pack.clt.Endpoint("webapi", "token")
	re, err := pack.clt.PostJSON(context.Background(), endpoint, types.ProvisionTokenSpecV2{
		Roles: types.SystemRoles{types.RoleProxy},
	})
	require.NoError(t, err)

	var responseToken nodeJoinToken
	err = json.Unmarshal(re.Bytes(), &responseToken)
	require.NoError(t, err)

	// download mTLS files from /webapi/sites/:site/sign/db
	endpointSign := pack.clt.Endpoint("webapi", "sites", clusterName, "sign", "db")

	bs, err := json.Marshal(struct {
		Hostname string `json:"hostname"`
		TTL      string `json:"ttl"`
		Format   string `json:"format"`
	}{
		Hostname: "mypg.example.com",
		TTL:      "2h",
		Format:   "db",
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, endpointSign, bytes.NewReader(bs))
	require.NoError(t, err)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+responseToken.ID)

	anonHTTPClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := anonHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// It fails because we passed a Provision Token with the wrong Role: Proxy
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// using a user token also returns Forbidden
	endpointResetToken := pack.clt.Endpoint("webapi", "users", "password", "token")
	_, err = pack.clt.PostJSON(context.Background(), endpointResetToken, auth.CreateUserTokenRequest{
		Name: username,
		TTL:  time.Minute,
		Type: auth.UserTokenTypeResetPassword,
	})
	require.NoError(t, err)

	req, err = http.NewRequest(http.MethodPost, endpointSign, bytes.NewReader(bs))
	require.NoError(t, err)

	resp, err = anonHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestCheckAccessToRegisteredResource_AccessDenied tests that access denied error
// is ignored.
func TestCheckAccessToRegisteredResource_AccessDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo", nil /* roles */)

	// newWebPack already registers 1 node.
	n, err := env.server.Auth().GetNodes(ctx, env.node.GetNamespace())
	require.NoError(t, err)
	require.Len(t, n, 1)

	// Checking for access returns true.
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "resources", "check")
	re, err := pack.clt.Get(ctx, endpoint, url.Values{})
	require.NoError(t, err)
	resp := checkAccessToRegisteredResourceResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.True(t, resp.HasResource)

	// Deny this resource.
	fooRole, err := env.server.Auth().GetRole(ctx, "user:foo")
	require.NoError(t, err)
	fooRole.SetRules(types.Deny, []types.Rule{types.NewRule(types.KindNode, services.RW())})
	require.NoError(t, env.server.Auth().UpsertRole(ctx, fooRole))

	// Direct querying should return a access denied error.
	endpoint = pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "nodes")
	_, err = pack.clt.Get(ctx, endpoint, url.Values{})
	require.True(t, trace.IsAccessDenied(err))

	// Checking for access returns false, not an error.
	endpoint = pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "resources", "check")
	re, err = pack.clt.Get(ctx, endpoint, url.Values{})
	require.NoError(t, err)
	resp = checkAccessToRegisteredResourceResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.False(t, resp.HasResource)
}

func TestCheckAccessToRegisteredResource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo", nil /* roles */)

	// Delete the node that was created by the `newWebPack` to start afresh.
	require.NoError(t, env.server.Auth().DeleteNode(ctx, env.node.GetNamespace(), env.node.ID()))
	n, err := env.server.Auth().GetNodes(ctx, env.node.GetNamespace())
	require.NoError(t, err)
	require.Len(t, n, 0)

	// Double check we start of with no resources.
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "resources", "check")
	re, err := pack.clt.Get(ctx, endpoint, url.Values{})
	require.NoError(t, err)
	resp := checkAccessToRegisteredResourceResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.False(t, resp.HasResource)

	// Test all cases return true.
	tests := []struct {
		name           string
		resourceKind   string
		insertResource func()
		deleteResource func()
	}{
		{
			name: "has registered windows desktop",
			insertResource: func() {
				wd, err := types.NewWindowsDesktopV3("test-desktop", nil, types.WindowsDesktopSpecV3{
					Addr:   "addr",
					HostID: "hostid",
				})
				require.NoError(t, err)
				require.NoError(t, env.server.Auth().UpsertWindowsDesktop(ctx, wd))
			},
			deleteResource: func() {
				require.NoError(t, env.server.Auth().DeleteWindowsDesktop(ctx, "hostid", "test-desktop"))
				wds, err := env.server.Auth().GetWindowsDesktops(ctx, types.WindowsDesktopFilter{})
				require.NoError(t, err)
				require.Len(t, wds, 0)
			},
		},
		{
			name: "has registered node",
			insertResource: func() {
				resource, err := types.NewServer("test-node", types.KindNode, types.ServerSpecV2{})
				require.NoError(t, err)
				_, err = env.server.Auth().UpsertNode(ctx, resource)
				require.NoError(t, err)
			},
			deleteResource: func() {
				require.NoError(t, env.server.Auth().DeleteNode(ctx, apidefaults.Namespace, "test-node"))
				nodes, err := env.server.Auth().GetNodes(ctx, apidefaults.Namespace)
				require.NoError(t, err)
				require.Len(t, nodes, 0)
			},
		},
		{
			name: "has registered app server",
			insertResource: func() {
				resource := &types.AppServerV3{
					Metadata: types.Metadata{Name: "test-app"},
					Kind:     types.KindAppServer,
					Version:  types.V2,
					Spec: types.AppServerSpecV3{
						HostID: "hostid",
						App: &types.AppV3{
							Metadata: types.Metadata{
								Name: "app-name",
							},
							Spec: types.AppSpecV3{
								URI: "https://console.aws.amazon.com",
							},
						},
					},
				}
				_, err := env.server.Auth().UpsertApplicationServer(ctx, resource)
				require.NoError(t, err)
			},
			deleteResource: func() {
				require.NoError(t, env.server.Auth().DeleteApplicationServer(ctx, apidefaults.Namespace, "hostid", "test-app"))
				apps, err := env.server.Auth().GetApplicationServers(ctx, apidefaults.Namespace)
				require.NoError(t, err)
				require.Len(t, apps, 0)
			},
		},
		{
			name: "has registered db server",
			insertResource: func() {
				db, err := types.NewDatabaseServerV3(types.Metadata{
					Name: "test-db",
				}, types.DatabaseServerSpecV3{
					Protocol: "test-protocol",
					URI:      "test-uri",
					Hostname: "test-hostname",
					HostID:   "test-hostID",
				})
				require.NoError(t, err)
				_, err = env.server.Auth().UpsertDatabaseServer(ctx, db)
				require.NoError(t, err)
			},
			deleteResource: func() {
				require.NoError(t, env.server.Auth().DeleteDatabaseServer(ctx, apidefaults.Namespace, "test-hostID", "test-db"))
				dbs, err := env.server.Auth().GetDatabaseServers(ctx, apidefaults.Namespace)
				require.NoError(t, err)
				require.Len(t, dbs, 0)
			},
		},
		{
			name: "has registered kube service",
			insertResource: func() {
				_, err := env.server.Auth().UpsertKubeServiceV2(ctx, &types.ServerV2{
					Metadata: types.Metadata{Name: "test-kube"},
					Kind:     types.KindKubeService,
					Version:  types.V2,
					Spec: types.ServerSpecV2{
						Addr:               "test",
						KubernetesClusters: []*types.KubernetesCluster{{Name: "test-kube-name"}},
					},
				})
				require.NoError(t, err)
			},
			deleteResource: func() {
				require.NoError(t, env.server.Auth().DeleteKubeService(ctx, "test-kube"))
				kubes, err := env.server.Auth().GetKubeServices(ctx)
				require.NoError(t, err)
				require.Len(t, kubes, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.insertResource()

			re, err := pack.clt.Get(ctx, endpoint, url.Values{})
			require.NoError(t, err)
			resp := checkAccessToRegisteredResourceResponse{}
			require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
			require.True(t, resp.HasResource)

			tc.deleteResource()
		})
	}
}

func TestClusterDatabasesGet(t *testing.T) {
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "test-user@example.com", nil /* roles */)

	query := url.Values{"sort": []string{"name"}}
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "databases")
	re, err := pack.clt.Get(context.Background(), endpoint, query)
	require.NoError(t, err)

	type testResponse struct {
		Items      []ui.Database `json:"items"`
		TotalCount int           `json:"totalCount"`
	}

	// No db registered.
	resp := testResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.Len(t, resp.Items, 0)

	// Register databases.
	db, err := types.NewDatabaseServerV3(types.Metadata{
		Name: "dbServer1",
	}, types.DatabaseServerSpecV3{
		Hostname: "test-hostname",
		HostID:   "test-hostID",
		Database: &types.DatabaseV3{
			Metadata: types.Metadata{
				Name:        "db1",
				Description: "test-description",
				Labels:      map[string]string{"test-field": "test-value"},
			},
			Spec: types.DatabaseSpecV3{
				Protocol: "test-protocol",
				URI:      "test-uri",
			},
		},
	})
	require.NoError(t, err)
	db2, err := types.NewDatabaseServerV3(types.Metadata{
		Name: "dbServer2",
	}, types.DatabaseServerSpecV3{
		Hostname: "test-hostname",
		HostID:   "test-hostID",
		Database: &types.DatabaseV3{
			Metadata: types.Metadata{
				Name: "db2",
			},
			Spec: types.DatabaseSpecV3{
				Protocol: "test-protocol",
				URI:      "test-uri:1234",
			},
		},
	})
	require.NoError(t, err)

	_, err = env.server.Auth().UpsertDatabaseServer(context.Background(), db)
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertDatabaseServer(context.Background(), db2)
	require.NoError(t, err)

	re, err = pack.clt.Get(context.Background(), endpoint, query)
	require.NoError(t, err)

	resp = testResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.Len(t, resp.Items, 2)
	require.Equal(t, 2, resp.TotalCount)
	require.ElementsMatch(t, resp.Items, []ui.Database{{
		Name:     "db1",
		Desc:     "test-description",
		Protocol: "test-protocol",
		Type:     types.DatabaseTypeSelfHosted,
		Labels:   []ui.Label{{Name: "test-field", Value: "test-value"}},
		Hostname: "test-uri",
	}, {
		Name:     "db2",
		Type:     types.DatabaseTypeSelfHosted,
		Labels:   []ui.Label{},
		Protocol: "test-protocol",
		Hostname: "test-uri",
	}})
}

func TestClusterDatabaseGet(t *testing.T) {
	env := newWebPack(t, 1)
	ctx := context.Background()

	proxy := env.proxies[0]

	dbNames := []string{"db1", "db2"}
	dbUsers := []string{"user1", "user2"}

	for _, tt := range []struct {
		name            string
		preRegisterDB   bool
		databaseName    string
		userRoles       func(*testing.T) []types.Role
		expectedDBUsers []string
		expectedDBNames []string
		requireError    require.ErrorAssertionFunc
	}{
		{
			name:          "valid",
			preRegisterDB: true,
			databaseName:  "valid",
			requireError:  require.NoError,
		},
		{
			name:          "notfound",
			preRegisterDB: true,
			databaseName:  "otherdb",
			requireError: func(tt require.TestingT, err error, i ...interface{}) {
				require.True(tt, trace.IsNotFound(err), "expected a not found error, got %v", err)
			},
		},
		{
			name:          "notauthorized",
			preRegisterDB: true,
			databaseName:  "notauthorized",
			userRoles: func(tt *testing.T) []types.Role {
				role, err := types.NewRole(
					"myrole",
					types.RoleSpecV5{
						Allow: types.RoleConditions{
							DatabaseLabels: types.Labels{
								"env": apiutils.Strings{"staging"},
							},
						},
					},
				)
				require.NoError(tt, err)
				return []types.Role{role}
			},
			requireError: func(tt require.TestingT, err error, i ...interface{}) {
				require.True(tt, trace.IsNotFound(err), "expected a not found error, got %v", err)
			},
		},
		{
			name:          "nodb",
			preRegisterDB: false,
			databaseName:  "nodb",
			userRoles: func(tt *testing.T) []types.Role {
				roleWithDBName, err := types.NewRole(
					"myroleWithDBName",
					types.RoleSpecV5{
						Allow: types.RoleConditions{
							DatabaseLabels: types.Labels{
								"env": apiutils.Strings{"prod"},
							},
							DatabaseNames: dbNames,
						},
					},
				)
				require.NoError(tt, err)

				return []types.Role{roleWithDBName}
			},
			expectedDBNames: dbNames,
			expectedDBUsers: dbUsers,
			requireError: func(tt require.TestingT, err error, i ...interface{}) {
				require.True(tt, trace.IsNotFound(err), "expected a not found error, got %v", err)
			},
		},
		{
			name:          "authorizedDBNamesUsers",
			preRegisterDB: true,
			databaseName:  "authorizedDBNamesUsers",
			userRoles: func(tt *testing.T) []types.Role {
				roleWithDBName, err := types.NewRole(
					"myroleWithDBName",
					types.RoleSpecV5{
						Allow: types.RoleConditions{
							DatabaseLabels: types.Labels{
								"env": apiutils.Strings{"prod"},
							},
							DatabaseNames: dbNames,
						},
					},
				)
				require.NoError(tt, err)

				roleWithDBUser, err := types.NewRole(
					"myroleWithDBUser",
					types.RoleSpecV5{
						Allow: types.RoleConditions{
							DatabaseLabels: types.Labels{
								"env": apiutils.Strings{"prod"},
							},
							DatabaseUsers: dbUsers,
						},
					},
				)
				require.NoError(tt, err)

				return []types.Role{roleWithDBUser, roleWithDBName}
			},
			expectedDBNames: dbNames,
			expectedDBUsers: dbUsers,
			requireError:    require.NoError,
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create default pre-registerDB
			if tt.preRegisterDB {
				db, err := types.NewDatabaseV3(types.Metadata{
					Name: tt.name,
					Labels: map[string]string{
						"env": "prod",
					},
				}, types.DatabaseSpecV3{
					Protocol: "test-protocol",
					URI:      "test-uri",
				})
				require.NoError(t, err)

				dbServer, err := types.NewDatabaseServerV3(types.Metadata{
					Name: tt.name,
				}, types.DatabaseServerSpecV3{
					Hostname: tt.name,
					Protocol: "test-protocol",
					URI:      "test-uri",
					HostID:   uuid.NewString(),
					Database: db,
				})
				require.NoError(t, err)

				_, err = env.server.Auth().UpsertDatabaseServer(context.Background(), dbServer)
				require.NoError(t, err)
			}

			var roles []types.Role
			if tt.userRoles != nil {
				roles = tt.userRoles(t)
			}

			pack := proxy.authPack(t, tt.name+"_user@example.com", roles)

			endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "databases", tt.databaseName)
			re, err := pack.clt.Get(ctx, endpoint, nil)
			tt.requireError(t, err)
			if err != nil {
				return
			}

			resp := ui.Database{}
			require.NoError(t, json.Unmarshal(re.Bytes(), &resp))

			require.Equal(t, tt.databaseName, resp.Name, "database name")
			require.Equal(t, types.DatabaseTypeSelfHosted, resp.Type, "database type")
			require.EqualValues(t, []ui.Label{{Name: "env", Value: "prod"}}, resp.Labels)
			require.ElementsMatch(t, tt.expectedDBUsers, resp.DatabaseUsers)
			require.ElementsMatch(t, tt.expectedDBNames, resp.DatabaseNames)
		})
	}
}

func TestClusterKubesGet(t *testing.T) {
	env := newWebPack(t, 1)

	proxy := env.proxies[0]

	extraRole := &types.RoleV5{
		Metadata: types.Metadata{Name: "extra-role"},
		Spec: types.RoleSpecV5{
			Allow: types.RoleConditions{
				KubeUsers:  []string{"user1"},
				KubeGroups: []string{"group1"},
				KubernetesLabels: types.Labels{
					"*": []string{"*"},
				},
			},
		},
	}

	cluster1, err := types.NewKubernetesClusterV3(
		types.Metadata{
			Name:   "test-kube1",
			Labels: map[string]string{"test-field": "test-value"},
		},
		types.KubernetesClusterSpecV3{},
	)
	require.NoError(t, err)

	// duplicate same server
	for i := 0; i < 3; i++ {
		server, err := types.NewKubernetesServerV3FromCluster(
			cluster1,
			fmt.Sprintf("hostname-%d", i),
			fmt.Sprintf("uid-%d", i),
		)
		require.NoError(t, err)
		// Register a kube service.
		_, err = env.server.Auth().UpsertKubernetesServer(context.Background(), server)
		require.NoError(t, err)
	}

	cluster2, err := types.NewKubernetesClusterV3(
		types.Metadata{
			Name: "test-kube2",
		},
		types.KubernetesClusterSpecV3{},
	)
	require.NoError(t, err)
	server2, err := types.NewKubernetesServerV3FromCluster(
		cluster2,
		"test-kube2-hostname",
		"test-kube2-hostid",
	)
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertKubernetesServer(context.Background(), server2)
	require.NoError(t, err)

	type testResponse struct {
		Items      []ui.KubeCluster `json:"items"`
		TotalCount int              `json:"totalCount"`
	}

	tt := []struct {
		name             string
		user             string
		extraRoles       services.RoleSet
		expectedResponse []ui.KubeCluster
	}{
		{
			name: "user with no extra roles",
			user: "test-user@example.com",
			expectedResponse: []ui.KubeCluster{
				{
					Name:       "test-kube1",
					Labels:     []ui.Label{{Name: "test-field", Value: "test-value"}},
					KubeUsers:  nil,
					KubeGroups: nil,
				},
				{
					Name:       "test-kube2",
					Labels:     []ui.Label{},
					KubeUsers:  nil,
					KubeGroups: nil,
				},
			},
		},
		{
			name:       "user with extra roles",
			user:       "test-user2@example.com",
			extraRoles: services.NewRoleSet(extraRole),
			expectedResponse: []ui.KubeCluster{
				{
					Name:       "test-kube1",
					Labels:     []ui.Label{{Name: "test-field", Value: "test-value"}},
					KubeUsers:  []string{"user1"},
					KubeGroups: []string{"group1"},
				},
				{
					Name:       "test-kube2",
					Labels:     []ui.Label{},
					KubeUsers:  []string{"user1"},
					KubeGroups: []string{"group1"},
				},
			},
		},
	}

	for _, tc := range tt {
		pack := proxy.authPack(t, tc.user, tc.extraRoles)

		endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "kubernetes")

		re, err := pack.clt.Get(context.Background(), endpoint, url.Values{})
		require.NoError(t, err)

		resp := testResponse{}
		require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
		require.Len(t, resp.Items, 2)
		require.Equal(t, 2, resp.TotalCount)
		require.ElementsMatch(t, tc.expectedResponse, resp.Items)
	}
}

func TestClusterAppsGet(t *testing.T) {
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "test-user@example.com", nil /* roles */)

	type testResponse struct {
		Items      []ui.App `json:"items"`
		TotalCount int      `json:"totalCount"`
	}

	resource := &types.AppServerV3{
		Metadata: types.Metadata{Name: "test-app"},
		Kind:     types.KindAppServer,
		Version:  types.V2,
		Spec: types.AppServerSpecV3{
			HostID: "hostid",
			App: &types.AppV3{
				Metadata: types.Metadata{
					Name:        "name",
					Description: "description",
					Labels:      map[string]string{"test-field": "test-value"},
				},
				Spec: types.AppSpecV3{
					URI:        "https://console.aws.amazon.com", // sets field awsConsole to true
					PublicAddr: "publicaddrs",
				},
			},
		},
	}

	resource2, err := types.NewAppServerV3(types.Metadata{Name: "server2"}, types.AppServerSpecV3{
		HostID: "hostid",
		App: &types.AppV3{
			Metadata: types.Metadata{Name: "app2"},
			Spec:     types.AppSpecV3{URI: "uri", PublicAddr: "publicaddrs"},
		}})
	require.NoError(t, err)

	// Test URIs with tcp is filtered out of result.
	resource3, err := types.NewAppServerV3(types.Metadata{Name: "server3"}, types.AppServerSpecV3{
		HostID: "hostid",
		App: &types.AppV3{
			Metadata: types.Metadata{Name: "app3"},
			Spec:     types.AppSpecV3{URI: "tcp://something", PublicAddr: "publicaddrs"},
		}})
	require.NoError(t, err)

	// Register apps.
	_, err = env.server.Auth().UpsertApplicationServer(context.Background(), resource)
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertApplicationServer(context.Background(), resource2)
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertApplicationServer(context.Background(), resource3)
	require.NoError(t, err)

	// Make the call.
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "apps")
	re, err := pack.clt.Get(context.Background(), endpoint, url.Values{"sort": []string{"name"}})
	require.NoError(t, err)

	// Test correct response.
	resp := testResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.Len(t, resp.Items, 2)
	require.Equal(t, 2, resp.TotalCount)
	require.ElementsMatch(t, resp.Items, []ui.App{{
		Name:        resource.Spec.App.GetName(),
		Description: resource.Spec.App.GetDescription(),
		URI:         resource.Spec.App.GetURI(),
		PublicAddr:  resource.Spec.App.GetPublicAddr(),
		Labels:      []ui.Label{{Name: "test-field", Value: "test-value"}},
		FQDN:        resource.Spec.App.GetPublicAddr(),
		ClusterID:   env.server.ClusterName(),
		AWSConsole:  true,
	}, {
		Name:       "app2",
		URI:        "uri",
		Labels:     []ui.Label{},
		ClusterID:  env.server.ClusterName(),
		FQDN:       "publicaddrs",
		PublicAddr: "publicaddrs",
		AWSConsole: false,
	}})

}

// TestApplicationAccessDisabled makes sure application access can be disabled
// via modules.
func TestApplicationAccessDisabled(t *testing.T) {
	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			App: false,
		},
	})

	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	// Register an application.
	app, err := types.NewAppV3(types.Metadata{
		Name: "panel",
	}, types.AppSpecV3{
		URI:        "localhost",
		PublicAddr: "panel.example.com",
	})
	require.NoError(t, err)
	server, err := types.NewAppServerV3FromApp(app, "host", uuid.New().String())
	require.NoError(t, err)
	_, err = env.server.Auth().UpsertApplicationServer(context.Background(), server)
	require.NoError(t, err)

	endpoint := pack.clt.Endpoint("webapi", "sessions", "app")
	_, err = pack.clt.PostJSON(context.Background(), endpoint, &CreateAppSessionRequest{
		FQDNHint:    "panel.example.com",
		PublicAddr:  "panel.example.com",
		ClusterName: "localhost",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "this Teleport cluster is not licensed for application access")
}

// TestApplicationWebSessionsDeletedAfterLogout makes sure user's application
// sessions are deleted after user logout.
func TestApplicationWebSessionsDeletedAfterLogout(t *testing.T) {
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	// Register multiple applications.
	applications := []struct {
		name       string
		publicAddr string
	}{
		{name: "panel", publicAddr: "panel.example.com"},
		{name: "admin", publicAddr: "admin.example.com"},
		{name: "metrics", publicAddr: "metrics.example.com"},
	}

	// Register and create a session for each application.
	for _, application := range applications {
		// Register an application.
		app, err := types.NewAppV3(types.Metadata{
			Name: application.name,
		}, types.AppSpecV3{
			URI:        "localhost",
			PublicAddr: application.publicAddr,
		})
		require.NoError(t, err)
		server, err := types.NewAppServerV3FromApp(app, "host", uuid.New().String())
		require.NoError(t, err)
		_, err = env.server.Auth().UpsertApplicationServer(context.Background(), server)
		require.NoError(t, err)

		// Create application session
		endpoint := pack.clt.Endpoint("webapi", "sessions", "app")
		_, err = pack.clt.PostJSON(context.Background(), endpoint, &CreateAppSessionRequest{
			FQDNHint:    application.publicAddr,
			PublicAddr:  application.publicAddr,
			ClusterName: "localhost",
		})
		require.NoError(t, err)
	}

	// List sessions, should have one for each application.
	sessions, err := proxy.client.GetAppSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, len(applications))

	// Logout from Telport.
	_, err = pack.clt.Delete(context.Background(), pack.clt.Endpoint("webapi", "sessions"))
	require.NoError(t, err)

	// Check sessions after logout, should be empty.
	sessions, err = proxy.client.GetAppSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, sessions, 0)
}

func TestGetWebConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)

	// Set auth preference with passwordless.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:          constants.Local,
		SecondFactor:  constants.SecondFactorOptional,
		ConnectorName: constants.PasswordlessConnector,
		Webauthn: &types.Webauthn{
			RPID: "localhost",
		},
	})
	require.NoError(t, err)
	err = env.server.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	// Add a test connector.
	github, err := types.NewGithubConnector("test-github", types.GithubConnectorSpecV3{
		TeamsToLogins: []types.TeamMapping{
			{
				Organization: "octocats",
				Team:         "dummy",
				Logins:       []string{"dummy"},
			},
		},
	})
	require.NoError(t, err)
	err = env.server.Auth().UpsertGithubConnector(ctx, github)
	require.NoError(t, err)

	expectedCfg := webclient.WebConfig{
		Auth: webclient.WebConfigAuthSettings{
			SecondFactor: constants.SecondFactorOptional,
			Providers: []webclient.WebConfigAuthProvider{{
				Name:      "test-github",
				Type:      constants.Github,
				WebAPIURL: webclient.WebConfigAuthProviderGitHubURL,
			}},
			LocalAuthEnabled:   true,
			AllowPasswordless:  true,
			AuthType:           constants.Local,
			PreferredLocalMFA:  constants.SecondFactorWebauthn,
			LocalConnectorName: constants.PasswordlessConnector,
			PrivateKeyPolicy:   keys.PrivateKeyPolicyNone,
		},
		CanJoinSessions:  true,
		ProxyClusterName: env.server.ClusterName(),
		IsCloud:          false,
	}

	// Make a request.
	clt := env.proxies[0].newClient(t)
	endpoint := clt.Endpoint("web", "config.js")
	re, err := clt.Get(ctx, endpoint, nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(re.Bytes()), "var GRV_CONFIG"))

	// Response is type application/javascript, we need to strip off the variable name
	// and the semicolon at the end, then we are left with json like object.
	var cfg webclient.WebConfig
	str := strings.ReplaceAll(string(re.Bytes()), "var GRV_CONFIG = ", "")
	err = json.Unmarshal([]byte(str[:len(str)-1]), &cfg)
	require.NoError(t, err)
	require.Equal(t, expectedCfg, cfg)
}

func TestCreatePrivilegeToken(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]

	// Create a user with second factor totp.
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	// Get a totp code.
	totpCode, err := totp.GenerateCode(pack.otpSecret, env.clock.Now().Add(30*time.Second))
	require.NoError(t, err)

	endpoint := pack.clt.Endpoint("webapi", "users", "privilege", "token")
	re, err := pack.clt.PostJSON(context.Background(), endpoint, &privilegeTokenRequest{
		SecondFactorToken: totpCode,
	})
	require.NoError(t, err)

	var privilegeToken string
	err = json.Unmarshal(re.Bytes(), &privilegeToken)
	require.NoError(t, err)
	require.NotEmpty(t, privilegeToken)
}

func TestAddMFADevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	// Enable second factor.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOptional,
		Webauthn: &types.Webauthn{
			RPID: "localhost",
		},
	})
	require.NoError(t, err)
	err = env.server.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	// Get a totp code to re-auth.
	totpCode, err := totp.GenerateCode(pack.otpSecret, env.clock.Now().Add(30*time.Second))
	require.NoError(t, err)

	// Obtain a privilege token.
	endpoint := pack.clt.Endpoint("webapi", "users", "privilege", "token")
	re, err := pack.clt.PostJSON(ctx, endpoint, &privilegeTokenRequest{
		SecondFactorToken: totpCode,
	})
	require.NoError(t, err)
	var privilegeToken string
	require.NoError(t, json.Unmarshal(re.Bytes(), &privilegeToken))

	tests := []struct {
		name            string
		deviceName      string
		getTOTPCode     func() string
		getWebauthnResp func() *wanlib.CredentialCreationResponse
	}{
		{
			name:       "new TOTP device",
			deviceName: "new-totp",
			getTOTPCode: func() string {
				// Create totp secrets.
				res, err := env.server.Auth().CreateRegisterChallenge(ctx, &authproto.CreateRegisterChallengeRequest{
					TokenID:    privilegeToken,
					DeviceType: authproto.DeviceType_DEVICE_TYPE_TOTP,
				})
				require.NoError(t, err)

				_, regRes, err := auth.NewTestDeviceFromChallenge(res, auth.WithTestDeviceClock(env.clock))
				require.NoError(t, err)

				return regRes.GetTOTP().Code
			},
		},
		{
			name:       "new Webauthn device",
			deviceName: "new-webauthn",
			getWebauthnResp: func() *wanlib.CredentialCreationResponse {
				// Get webauthn register challenge.
				res, err := env.server.Auth().CreateRegisterChallenge(ctx, &authproto.CreateRegisterChallengeRequest{
					TokenID:    privilegeToken,
					DeviceType: authproto.DeviceType_DEVICE_TYPE_WEBAUTHN,
				})
				require.NoError(t, err)

				_, regRes, err := auth.NewTestDeviceFromChallenge(res)
				require.NoError(t, err)

				return wanlib.CredentialCreationResponseFromProto(regRes.GetWebauthn())
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var totpCode string
			var webauthnRegResp *wanlib.CredentialCreationResponse

			if tc.getWebauthnResp != nil {
				webauthnRegResp = tc.getWebauthnResp()
			} else {
				totpCode = tc.getTOTPCode()
			}

			// Add device.
			endpoint := pack.clt.Endpoint("webapi", "mfa", "devices")
			_, err := pack.clt.PostJSON(ctx, endpoint, addMFADeviceRequest{
				PrivilegeTokenID:         privilegeToken,
				DeviceName:               tc.deviceName,
				SecondFactorToken:        totpCode,
				WebauthnRegisterResponse: webauthnRegResp,
			})
			require.NoError(t, err)
		})
	}
}

func TestDeleteMFA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	// setting up client manually because we need sanitizer off
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	opts := []roundtrip.ClientParam{roundtrip.BearerAuth(pack.session.Token), roundtrip.CookieJar(jar), roundtrip.HTTPClient(client.NewInsecureWebClient())}
	rclt, err := roundtrip.NewClient(proxy.webURL.String(), teleport.WebAPIVersion, opts...)
	require.NoError(t, err)
	clt := client.WebClient{Client: rclt}
	jar.SetCookies(&proxy.webURL, pack.cookies)

	totpCode, err := totp.GenerateCode(pack.otpSecret, env.clock.Now().Add(30*time.Second))
	require.NoError(t, err)

	// Obtain a privilege token.
	endpoint := pack.clt.Endpoint("webapi", "users", "privilege", "token")
	re, err := pack.clt.PostJSON(ctx, endpoint, &privilegeTokenRequest{
		SecondFactorToken: totpCode,
	})
	require.NoError(t, err)

	var privilegeToken string
	require.NoError(t, json.Unmarshal(re.Bytes(), &privilegeToken))

	names := []string{"x", "??", "%123/", "///", "my/device", "?/%&*1"}
	for _, devName := range names {
		devName := devName
		t.Run(devName, func(t *testing.T) {
			t.Parallel()
			otpSecret := base32.StdEncoding.EncodeToString([]byte(devName))
			dev, err := services.NewTOTPDevice(devName, otpSecret, env.clock.Now())
			require.NoError(t, err)
			err = env.server.Auth().UpsertMFADevice(ctx, pack.user, dev)
			require.NoError(t, err)

			enc := url.PathEscape(devName)
			_, err = clt.Delete(ctx, pack.clt.Endpoint("webapi", "mfa", "token", privilegeToken, "devices", enc))
			require.NoError(t, err)
		})
	}
}

func TestGetMFADevicesWithAuth(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo@example.com", nil /* roles */)

	endpoint := pack.clt.Endpoint("webapi", "mfa", "devices")
	re, err := pack.clt.Get(context.Background(), endpoint, url.Values{})
	require.NoError(t, err)

	var devices []ui.MFADevice
	err = json.Unmarshal(re.Bytes(), &devices)
	require.NoError(t, err)
	require.Len(t, devices, 1)
}

func TestGetAndDeleteMFADevices_WithRecoveryApprovedToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]

	// Create a user with a TOTP device.
	username := "llama"
	proxy.createUser(ctx, t, username, "root", "password", "some-otp-secret", nil /* roles */)

	// Enable second factor.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOptional,
		Webauthn: &types.Webauthn{
			RPID: env.server.ClusterName(),
		},
	})
	require.NoError(t, err)
	err = env.server.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	// Acquire an approved token.
	approvedToken, err := types.NewUserToken("some-token-id")
	require.NoError(t, err)
	approvedToken.SetUser(username)
	approvedToken.SetSubKind(auth.UserTokenTypeRecoveryApproved)
	approvedToken.SetExpiry(env.clock.Now().Add(5 * time.Minute))
	_, err = env.server.Auth().CreateUserToken(ctx, approvedToken)
	require.NoError(t, err)

	// Call the getter endpoint.
	clt := proxy.newClient(t)
	getDevicesEndpoint := clt.Endpoint("webapi", "mfa", "token", approvedToken.GetName(), "devices")
	res, err := clt.Get(ctx, getDevicesEndpoint, url.Values{})
	require.NoError(t, err)

	var devices []ui.MFADevice
	err = json.Unmarshal(res.Bytes(), &devices)
	require.NoError(t, err)
	require.Len(t, devices, 1)

	// Call the delete endpoint.
	_, err = clt.Delete(ctx, clt.Endpoint("webapi", "mfa", "token", approvedToken.GetName(), "devices", devices[0].Name))
	require.NoError(t, err)

	// Check device has been deleted.
	res, err = clt.Get(ctx, getDevicesEndpoint, url.Values{})
	require.NoError(t, err)

	err = json.Unmarshal(res.Bytes(), &devices)
	require.NoError(t, err)
	require.Len(t, devices, 0)
}

func TestCreateAuthenticateChallenge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]

	// Create a user with a TOTP device, with second factor preference to OTP only.
	authPack := proxy.authPack(t, "llama@example.com", nil /* roles */)

	// Authenticated client for private endpoints.
	authnClt := authPack.clt

	// Unauthenticated client for public endpoints.
	publicClt := proxy.newClient(t)

	// Acquire a start token, for the request the requires it.
	startToken, err := types.NewUserToken("some-token-id")
	require.NoError(t, err)
	startToken.SetUser(authPack.user)
	startToken.SetSubKind(auth.UserTokenTypeRecoveryStart)
	startToken.SetExpiry(env.clock.Now().Add(5 * time.Minute))
	_, err = env.server.Auth().CreateUserToken(ctx, startToken)
	require.NoError(t, err)

	tests := []struct {
		name    string
		clt     *client.WebClient
		ep      []string
		reqBody client.MFAChallengeRequest
	}{
		{
			name: "/webapi/mfa/authenticatechallenge/password",
			clt:  authnClt,
			ep:   []string{"webapi", "mfa", "authenticatechallenge", "password"},
			reqBody: client.MFAChallengeRequest{
				Pass: authPack.password,
			},
		},
		{
			name: "/webapi/mfa/login/begin",
			clt:  publicClt,
			ep:   []string{"webapi", "mfa", "login", "begin"},
			reqBody: client.MFAChallengeRequest{
				User: authPack.user,
				Pass: authPack.password,
			},
		},
		{
			name: "/webapi/mfa/authenticatechallenge",
			clt:  authnClt,
			ep:   []string{"webapi", "mfa", "authenticatechallenge"},
		},
		{
			name: "/webapi/mfa/token/:token/authenticatechallenge",
			clt:  publicClt,
			ep:   []string{"webapi", "mfa", "token", startToken.GetName(), "authenticatechallenge"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			endpoint := tc.clt.Endpoint(tc.ep...)
			res, err := tc.clt.PostJSON(ctx, endpoint, tc.reqBody)
			require.NoError(t, err)

			var chal client.MFAAuthenticateChallenge
			err = json.Unmarshal(res.Bytes(), &chal)
			require.NoError(t, err)
			require.True(t, chal.TOTPChallenge)
			require.Empty(t, chal.WebauthnChallenge)
		})
	}
}

func TestCreateRegisterChallenge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	clt := proxy.newClient(t)

	// Enable second factor.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOn,
		Webauthn: &types.Webauthn{
			RPID: env.server.ClusterName(),
		},
	})
	require.NoError(t, err)
	require.NoError(t, env.server.Auth().SetAuthPreference(ctx, ap))

	// Acquire an accepted token.
	token, err := types.NewUserToken("some-token-id")
	require.NoError(t, err)
	token.SetUser("llama")
	token.SetSubKind(auth.UserTokenTypePrivilege)
	token.SetExpiry(env.clock.Now().Add(5 * time.Minute))
	_, err = env.server.Auth().CreateUserToken(ctx, token)
	require.NoError(t, err)

	tests := []struct {
		name            string
		req             *createRegisterChallengeRequest
		assertChallenge func(t *testing.T, c *client.MFARegisterChallenge)
	}{
		{
			name: "totp",
			req: &createRegisterChallengeRequest{
				DeviceType: "totp",
			},
		},
		{
			name: "webauthn",
			req: &createRegisterChallengeRequest{
				DeviceType: "webauthn",
			},
		},
		{
			name: "passwordless",
			req: &createRegisterChallengeRequest{
				DeviceType:  "webauthn",
				DeviceUsage: "passwordless",
			},
			assertChallenge: func(t *testing.T, c *client.MFARegisterChallenge) {
				// rrk=true is a good proxy for passwordless.
				require.NotNil(t, c.Webauthn.Response.AuthenticatorSelection.RequireResidentKey, "rrk cannot be nil")
				require.True(t, *c.Webauthn.Response.AuthenticatorSelection.RequireResidentKey, "rrk cannot be false")
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			endpoint := clt.Endpoint("webapi", "mfa", "token", token.GetName(), "registerchallenge")
			res, err := clt.PostJSON(ctx, endpoint, tc.req)
			require.NoError(t, err)

			var chal client.MFARegisterChallenge
			require.NoError(t, json.Unmarshal(res.Bytes(), &chal))

			switch tc.req.DeviceType {
			case "totp":
				require.NotNil(t, chal.TOTP.QRCode, "TOTP QR code cannot be nil")
			case "webauthn":
				require.NotNil(t, chal.Webauthn, "WebAuthn challenge cannot be nil")
			}

			if tc.assertChallenge != nil {
				tc.assertChallenge(t, &chal)
			}
		})
	}
}

// TestCreateAppSession verifies that an existing session to the Web UI can
// be exchanged for an application specific session.
func TestCreateAppSession(t *testing.T) {
	t.Parallel()
	s := newWebSuite(t)
	pack := s.authPack(t, "foo@example.com")

	// Register an application called "panel".
	app, err := types.NewAppV3(types.Metadata{
		Name: "panel",
	}, types.AppSpecV3{
		URI:        "http://127.0.0.1:8080",
		PublicAddr: "panel.example.com",
	})
	require.NoError(t, err)
	server, err := types.NewAppServerV3FromApp(app, "host", uuid.New().String())
	require.NoError(t, err)
	_, err = s.server.Auth().UpsertApplicationServer(s.ctx, server)
	require.NoError(t, err)

	// Extract the session ID and bearer token for the current session.
	rawCookie := *pack.cookies[0]
	cookieBytes, err := hex.DecodeString(rawCookie.Value)
	require.NoError(t, err)
	var sessionCookie SessionCookie
	err = json.Unmarshal(cookieBytes, &sessionCookie)
	require.NoError(t, err)

	tests := []struct {
		name            string
		inCreateRequest *CreateAppSessionRequest
		outError        require.ErrorAssertionFunc
		outFQDN         string
		outUsername     string
	}{
		{
			name: "Valid request: all fields",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint:    "panel.example.com",
				PublicAddr:  "panel.example.com",
				ClusterName: "localhost",
			},
			outError:    require.NoError,
			outFQDN:     "panel.example.com",
			outUsername: "foo@example.com",
		},
		{
			name: "Valid request: without FQDN",
			inCreateRequest: &CreateAppSessionRequest{
				PublicAddr:  "panel.example.com",
				ClusterName: "localhost",
			},
			outError:    require.NoError,
			outFQDN:     "panel.example.com",
			outUsername: "foo@example.com",
		},
		{
			name: "Valid request: only FQDN",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint: "panel.example.com",
			},
			outError:    require.NoError,
			outFQDN:     "panel.example.com",
			outUsername: "foo@example.com",
		},
		{
			name: "Invalid request: only public address",
			inCreateRequest: &CreateAppSessionRequest{
				PublicAddr: "panel.example.com",
			},
			outError: require.Error,
		},
		{
			name: "Invalid request: only cluster name",
			inCreateRequest: &CreateAppSessionRequest{
				ClusterName: "localhost",
			},
			outError: require.Error,
		},
		{
			name: "Invalid application",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint:    "panel.example.com",
				PublicAddr:  "invalid.example.com",
				ClusterName: "localhost",
			},
			outError: require.Error,
		},
		{
			name: "Invalid cluster name",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint:    "panel.example.com",
				PublicAddr:  "panel.example.com",
				ClusterName: "example.com",
			},
			outError: require.Error,
		},
		{
			name: "Malicious request: all fields",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint:    "panel.example.com@malicious.com",
				PublicAddr:  "panel.example.com",
				ClusterName: "localhost",
			},
			outError:    require.NoError,
			outFQDN:     "panel.example.com",
			outUsername: "foo@example.com",
		},
		{
			name: "Malicious request: only FQDN",
			inCreateRequest: &CreateAppSessionRequest{
				FQDNHint: "panel.example.com@malicious.com",
			},
			outError: require.Error,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Make a request to create an application session for "panel".
			endpoint := pack.clt.Endpoint("webapi", "sessions", "app")
			resp, err := pack.clt.PostJSON(s.ctx, endpoint, tt.inCreateRequest)
			tt.outError(t, err)
			if err != nil {
				return
			}

			// Unmarshal the response.
			var response *CreateAppSessionResponse
			require.NoError(t, json.Unmarshal(resp.Bytes(), &response))
			require.Equal(t, tt.outFQDN, response.FQDN)

			// Verify that the application session was created.
			sess, err := s.server.Auth().GetAppSession(s.ctx, types.GetAppSessionRequest{
				SessionID: response.CookieValue,
			})
			require.NoError(t, err)
			require.Equal(t, tt.outUsername, sess.GetUser())
			require.Equal(t, response.CookieValue, sess.GetName())
		})
	}
}

func TestNewSessionResponseWithRenewSession(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)

	// Set a web idle timeout.
	duration := time.Duration(5) * time.Minute
	cfg := types.DefaultClusterNetworkingConfig()
	cfg.SetWebIdleTimeout(duration)
	require.NoError(t, env.server.Auth().SetClusterNetworkingConfig(context.Background(), cfg))

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo", nil /* roles */)

	var ns *CreateSessionResponse
	resp := pack.renewSession(context.Background(), t)
	require.NoError(t, json.Unmarshal(resp.Bytes(), &ns))

	require.Equal(t, int(duration.Milliseconds()), ns.SessionInactiveTimeoutMS)
	require.Equal(t, roundtrip.AuthBearer, ns.TokenType)
	require.NotEmpty(t, ns.SessionExpires)
	require.NotEmpty(t, ns.Token)
	require.NotEmpty(t, ns.TokenExpiresIn)
}

// TestWebSessionsRenewDoesNotBreakExistingTerminalSession validates that the
// session renewed via one proxy does not force the terminals created by another
// proxy to disconnect
//
// See https://github.com/gravitational/teleport/issues/5265
func TestWebSessionsRenewDoesNotBreakExistingTerminalSession(t *testing.T) {
	env := newWebPack(t, 2)

	proxy1, proxy2 := env.proxies[0], env.proxies[1]
	// Connect to both proxies
	pack1 := proxy1.authPack(t, "foo", nil /* roles */)
	pack2 := proxy2.authPackFromPack(t, pack1)

	ws, _ := proxy2.makeTerminal(t, pack2, "")

	// Advance the time before renewing the session.
	// This will allow the new session to have a more plausible
	// expiration
	const delta = 30 * time.Second
	env.clock.Advance(auth.BearerTokenTTL - delta)

	// Renew the session using the 1st proxy
	resp := pack1.renewSession(context.Background(), t)

	// Expire the old session and make sure it has been removed.
	// The bearer token is also removed after this point, so we have to
	// use the new session data for future connects
	env.clock.Advance(delta + 1*time.Second)
	pack2 = proxy2.authPackFromResponse(t, resp)

	// Verify that access via the 2nd proxy also works for the same session
	pack2.validateAPI(context.Background(), t)

	// Check whether the terminal session is still active
	validateTerminalStream(t, ws)
}

// TestWebSessionsRenewAllowsOldBearerTokenToLinger validates that the
// bearer token bound to the previous session is still active after the
// session renewal, if the renewal happens with a time margin.
//
// See https://github.com/gravitational/teleport/issues/5265
func TestWebSessionsRenewAllowsOldBearerTokenToLinger(t *testing.T) {
	// Login to implicitly create a new web session
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "foo", nil /* roles */)

	delta := 30 * time.Second
	// Advance the time before renewing the session.
	// This will allow the new session to have a more plausible
	// expiration
	env.clock.Advance(auth.BearerTokenTTL - delta)

	// make sure we can use client to make authenticated requests
	// before we issue this request, we will recover session id and bearer token
	//
	prevSessionCookie := *pack.cookies[0]
	prevBearerToken := pack.session.Token
	resp := pack.renewSession(context.Background(), t)

	newPack := proxy.authPackFromResponse(t, resp)

	// new session is functioning
	newPack.validateAPI(context.Background(), t)

	sessionCookie := *newPack.cookies[0]
	bearerToken := newPack.session.Token
	require.NotEmpty(t, bearerToken)
	require.NotEmpty(t, cmp.Diff(bearerToken, prevBearerToken))

	prevSessionID := decodeSessionCookie(t, prevSessionCookie.Value)
	activeSessionID := decodeSessionCookie(t, sessionCookie.Value)
	require.NotEmpty(t, cmp.Diff(prevSessionID, activeSessionID))

	// old session is still valid
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	oldClt := proxy.newClient(t, roundtrip.BearerAuth(prevBearerToken), roundtrip.CookieJar(jar))
	jar.SetCookies(&proxy.webURL, []*http.Cookie{&prevSessionCookie})
	_, err = oldClt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites"), url.Values{})
	require.NoError(t, err)

	// now expire the old session and make sure it has been removed
	env.clock.Advance(delta)

	_, err = proxy.client.GetWebSession(context.Background(), types.GetWebSessionRequest{
		User:      "foo",
		SessionID: prevSessionID,
	})
	require.Regexp(t, "^key.*not found$", err.Error())

	// now delete session
	_, err = newPack.clt.Delete(
		context.Background(),
		pack.clt.Endpoint("webapi", "sessions"))
	require.NoError(t, err)

	// subsequent requests to use this session will fail
	_, err = newPack.clt.Get(context.Background(), pack.clt.Endpoint("webapi", "sites"), url.Values{})
	require.True(t, trace.IsAccessDenied(err))
}

// TestChangeUserAuthentication_recoveryCodesReturnedForCloud tests for following:
// - Recovery codes are not returned for usernames that are not emails
// - Recovery codes are returned for usernames that are valid emails
func TestChangeUserAuthentication_recoveryCodesReturnedForCloud(t *testing.T) {
	env := newWebPack(t, 1)
	ctx := context.Background()

	// Enable second factor.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOTP,
	})
	require.NoError(t, err)
	err = env.server.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	// Enable cloud feature.
	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	// Creaet a username that is not a valid email format for recovery.
	teleUser, err := types.NewUser("invalid-name-for-recovery")
	require.NoError(t, err)
	require.NoError(t, env.server.Auth().CreateUser(ctx, teleUser))

	// Create a reset password token and secrets.
	resetToken, err := env.server.Auth().CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: "invalid-name-for-recovery",
	})
	require.NoError(t, err)
	res, err := env.server.Auth().CreateRegisterChallenge(ctx, &authproto.CreateRegisterChallengeRequest{
		TokenID:    resetToken.GetName(),
		DeviceType: authproto.DeviceType_DEVICE_TYPE_TOTP,
	})
	require.NoError(t, err)
	totpCode, err := totp.GenerateCode(res.GetTOTP().GetSecret(), env.clock.Now())
	require.NoError(t, err)

	// Test invalid username does not receive codes.
	clt := env.proxies[0].client
	re, err := clt.ChangeUserAuthentication(ctx, &authproto.ChangeUserAuthenticationRequest{
		TokenID:     resetToken.GetName(),
		NewPassword: []byte("abc123"),
		NewMFARegisterResponse: &authproto.MFARegisterResponse{Response: &authproto.MFARegisterResponse_TOTP{
			TOTP: &authproto.TOTPRegisterResponse{Code: totpCode},
		}},
	})
	require.NoError(t, err)
	require.Nil(t, re.Recovery)
	require.False(t, re.PrivateKeyPolicyEnabled)

	// Create a user that is valid for recovery.
	teleUser, err = types.NewUser("valid-username@example.com")
	require.NoError(t, err)
	require.NoError(t, env.server.Auth().CreateUser(ctx, teleUser))

	// Create a reset password token and secrets.
	resetToken, err = env.server.Auth().CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: "valid-username@example.com",
	})
	require.NoError(t, err)
	res, err = env.server.Auth().CreateRegisterChallenge(ctx, &authproto.CreateRegisterChallengeRequest{
		TokenID:    resetToken.GetName(),
		DeviceType: authproto.DeviceType_DEVICE_TYPE_TOTP,
	})
	require.NoError(t, err)
	totpCode, err = totp.GenerateCode(res.GetTOTP().GetSecret(), env.clock.Now())
	require.NoError(t, err)

	// Test valid username (email) returns codes.
	re, err = clt.ChangeUserAuthentication(ctx, &authproto.ChangeUserAuthenticationRequest{
		TokenID:     resetToken.GetName(),
		NewPassword: []byte("abc123"),
		NewMFARegisterResponse: &authproto.MFARegisterResponse{Response: &authproto.MFARegisterResponse_TOTP{
			TOTP: &authproto.TOTPRegisterResponse{Code: totpCode},
		}},
	})
	require.NoError(t, err)
	require.Len(t, re.Recovery.Codes, 3)
	require.NotEmpty(t, re.Recovery.Created)
	require.False(t, re.PrivateKeyPolicyEnabled)
}

// TestChangeUserAuthentication_WithPrivacyPolicyEnabledError tests
// that when there is a privacy policy enabled error, we still get
// a non error response with recovery codes and a privacy policy
// flag set to true.
func TestChangeUserAuthentication_WithPrivacyPolicyEnabledError(t *testing.T) {
	env := newWebPack(t, 1)
	ctx := context.Background()

	// Enable second factor required by cloud and a privacy policy.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:           constants.Local,
		SecondFactor:   constants.SecondFactorOTP,
		RequireMFAType: types.RequireMFAType_HARDWARE_KEY_TOUCH,
	})
	require.NoError(t, err)
	err = env.server.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	// Enable cloud feature.
	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
		MockAttestHardwareKey: func(_ context.Context, _ interface{}, policy keys.PrivateKeyPolicy, _ *keys.AttestationStatement, _ crypto.PublicKey, _ time.Duration) (keys.PrivateKeyPolicy, error) {
			return "", keys.NewPrivateKeyPolicyError(policy)
		},
	})

	// Create a user that is valid for recovery.
	teleUser, err := types.NewUser("valid-username@example.com")
	require.NoError(t, err)
	require.NoError(t, env.server.Auth().CreateUser(ctx, teleUser))

	// Create a reset password token and secrets.
	resetToken, err := env.server.Auth().CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: "valid-username@example.com",
	})
	require.NoError(t, err)
	res, err := env.server.Auth().CreateRegisterChallenge(ctx, &authproto.CreateRegisterChallengeRequest{
		TokenID:    resetToken.GetName(),
		DeviceType: authproto.DeviceType_DEVICE_TYPE_TOTP,
	})
	require.NoError(t, err)
	totpCode, err := totp.GenerateCode(res.GetTOTP().GetSecret(), env.clock.Now())
	require.NoError(t, err)

	// Craft http request data.
	clt := env.proxies[0].newClient(t)
	req := changeUserAuthenticationRequest{
		SecondFactorToken: totpCode,
		Password:          []byte("abc123"),
		TokenID:           resetToken.GetName(),
	}
	httpReqData, err := json.Marshal(req)
	require.NoError(t, err)

	// CSRF protected endpoint.
	csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	httpReq, err := http.NewRequest("PUT", clt.Endpoint("webapi", "users", "password", "token"), bytes.NewBuffer(httpReqData))
	require.NoError(t, err)
	addCSRFCookieToReq(httpReq, csrfToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(csrf.HeaderName, csrfToken)
	httpRes, err := httplib.ConvertResponse(clt.RoundTrip(func() (*http.Response, error) {
		return clt.HTTPClient().Do(httpReq)
	}))
	require.NoError(t, err)

	var apiRes ui.ChangedUserAuthn
	require.NoError(t, json.Unmarshal(httpRes.Bytes(), &apiRes))
	require.Len(t, apiRes.Recovery.Codes, 3)
	require.NotEmpty(t, apiRes.Recovery.Created)
	require.True(t, apiRes.PrivateKeyPolicyEnabled)
}

func TestChangeUserAuthentication_settingDefaultClusterAuthPreference(t *testing.T) {
	tt := []struct {
		name                 string
		cloud                bool
		numberOfUsers        int
		password             []byte
		authPreferenceType   string
		initialConnectorName string
		resultConnectorName  string
	}{{
		name:                 "first cloud sign-in changes connector to `passwordless`",
		cloud:                true,
		numberOfUsers:        1,
		authPreferenceType:   constants.Local,
		initialConnectorName: "",
		resultConnectorName:  constants.PasswordlessConnector,
	}, {
		name:                 "first non-cloud sign-in doesn't change the connector",
		cloud:                false,
		numberOfUsers:        1,
		authPreferenceType:   constants.Local,
		initialConnectorName: "",
		resultConnectorName:  "",
	}, {
		name:                 "second cloud sign-in doesn't change the connector",
		cloud:                true,
		numberOfUsers:        2,
		authPreferenceType:   constants.Local,
		initialConnectorName: "",
		resultConnectorName:  "",
	}, {
		name:                 "first cloud sign-in does not change custom connector",
		cloud:                true,
		numberOfUsers:        1,
		authPreferenceType:   constants.OIDC,
		initialConnectorName: "custom",
		resultConnectorName:  "custom",
	}, {
		name:                 "first cloud sign-in with password does not change connector",
		cloud:                true,
		numberOfUsers:        1,
		password:             []byte("abc123"),
		authPreferenceType:   constants.Local,
		initialConnectorName: "",
		resultConnectorName:  "",
	}}

	for _, tc := range tt {
		modules.SetTestModules(t, &modules.TestModules{
			TestFeatures: modules.Features{
				Cloud: tc.cloud,
			},
		})

		const RPID = "localhost"

		s := newWebSuiteWithConfig(t, webSuiteConfig{
			authPreferenceSpec: &types.AuthPreferenceSpecV2{
				Type:          tc.authPreferenceType,
				ConnectorName: tc.initialConnectorName,
				SecondFactor:  constants.SecondFactorOn,
				Webauthn: &types.Webauthn{
					RPID: RPID,
				},
			},
		})

		// user and role
		users := make([]types.User, tc.numberOfUsers)

		for i := 0; i < tc.numberOfUsers; i++ {
			user, err := types.NewUser(fmt.Sprintf("test_user_%v", i))
			require.NoError(t, err)

			user.SetCreatedBy(types.CreatedBy{
				User: types.UserRef{Name: "other_user"},
			})

			role := services.RoleForUser(user)

			err = s.server.Auth().UpsertRole(s.ctx, role)
			require.NoError(t, err)

			user.AddRole(role.GetName())

			err = s.server.Auth().CreateUser(s.ctx, user)
			require.NoError(t, err)

			users[i] = user
		}

		initialUser := users[0]

		clt := s.client()

		// create register challenge
		token, err := s.server.Auth().CreateResetPasswordToken(s.ctx, auth.CreateUserTokenRequest{
			Name: initialUser.GetName(),
		})
		require.NoError(t, err)

		res, err := s.server.Auth().CreateRegisterChallenge(s.ctx, &authproto.CreateRegisterChallengeRequest{
			TokenID:     token.GetName(),
			DeviceType:  authproto.DeviceType_DEVICE_TYPE_WEBAUTHN,
			DeviceUsage: authproto.DeviceUsage_DEVICE_USAGE_PASSWORDLESS,
		})
		require.NoError(t, err)

		cc := wanlib.CredentialCreationFromProto(res.GetWebauthn())

		// use passwordless as auth method
		device, err := mocku2f.Create()
		require.NoError(t, err)

		device.SetPasswordless()

		ccr, err := device.SignCredentialCreation("https://"+RPID, cc)
		require.NoError(t, err)

		// send sign-in response to server
		body, err := json.Marshal(changeUserAuthenticationRequest{
			WebauthnCreationResponse: ccr,
			TokenID:                  token.GetName(),
			DeviceName:               "passwordless-device",
			Password:                 tc.password,
		})
		require.NoError(t, err)

		req, err := http.NewRequest("PUT", clt.Endpoint("webapi", "users", "password", "token"), bytes.NewBuffer(body))
		require.NoError(t, err)

		csrfToken, err := csrf.GenerateToken()
		require.NoError(t, err)
		addCSRFCookieToReq(req, csrfToken)
		req.Header.Set(csrf.HeaderName, csrfToken)
		req.Header.Set("Content-Type", "application/json")

		re, err := clt.Client.RoundTrip(func() (*http.Response, error) {
			return clt.Client.HTTPClient().Do(req)
		})

		require.NoError(t, err)
		require.Equal(t, re.Code(), http.StatusOK)

		// check if auth preference connectorName is set
		authPreference, err := s.server.Auth().GetAuthPreference(s.ctx)
		require.NoError(t, err)

		require.Equal(t, authPreference.GetConnectorName(), tc.resultConnectorName, "Found unexpected auth connector name")
	}
}

func TestParseSSORequestParams(t *testing.T) {
	t.Parallel()

	token := "someMeaninglessTokenString"

	tests := []struct {
		name, url string
		wantErr   bool
		expected  *ssoRequestParams
	}{
		{
			name: "preserve redirect's query params (escaped)",
			url:  "https://localhost/login?connector_id=oidc&redirect_url=https:%2F%2Flocalhost:8080%2Fweb%2Fcluster%2Fim-a-cluster-name%2Fnodes%3Fsearch=tunnel&sort=hostname:asc",
			expected: &ssoRequestParams{
				clientRedirectURL: "https://localhost:8080/web/cluster/im-a-cluster-name/nodes?search=tunnel&sort=hostname:asc",
				connectorID:       "oidc",
				csrfToken:         token,
			},
		},
		{
			name: "preserve redirect's query params (unescaped)",
			url:  "https://localhost/login?connector_id=github&redirect_url=https://localhost:8080/web/cluster/im-a-cluster-name/nodes?search=tunnel&sort=hostname:asc",
			expected: &ssoRequestParams{
				clientRedirectURL: "https://localhost:8080/web/cluster/im-a-cluster-name/nodes?search=tunnel&sort=hostname:asc",
				connectorID:       "github",
				csrfToken:         token,
			},
		},
		{
			name: "preserve various encoded chars",
			url:  "https://localhost/login?connector_id=saml&redirect_url=https:%2F%2Flocalhost:8080%2Fweb%2Fcluster%2Fim-a-cluster-name%2Fapps%3Fquery=search(%2522watermelon%2522%252C%2520%2522this%2522)%2520%2526%2526%2520labels%255B%2522unique-id%2522%255D%2520%253D%253D%2520%2522hi%2522&sort=name:asc",
			expected: &ssoRequestParams{
				clientRedirectURL: "https://localhost:8080/web/cluster/im-a-cluster-name/apps?query=search(%22watermelon%22%2C%20%22this%22)%20%26%26%20labels%5B%22unique-id%22%5D%20%3D%3D%20%22hi%22&sort=name:asc",
				connectorID:       "saml",
				csrfToken:         token,
			},
		},
		{
			name:    "invalid redirect_url query param",
			url:     "https://localhost/login?redirect=https://localhost/nodes&connector_id=oidc",
			wantErr: true,
		},
		{
			name:    "invalid connector_id query param",
			url:     "https://localhost/login?redirect_url=https://localhost/nodes&connector=oidc",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("", tc.url, nil)
			require.NoError(t, err)
			addCSRFCookieToReq(req, token)

			params, err := parseSSORequestParams(req)

			switch {
			case tc.wantErr:
				require.Error(t, err)
			default:
				require.NoError(t, err)
				require.Equal(t, tc.expected, params)
			}
		})
	}
}

func TestClusterDesktopsGet(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)

	proxy := env.proxies[0]
	pack := proxy.authPack(t, "test-user@example.com", nil /* roles */)

	type testResponse struct {
		Items      []ui.Desktop `json:"items"`
		TotalCount int          `json:"totalCount"`
	}

	// Add a few desktops.
	resource, err := types.NewWindowsDesktopV3("desktop1", map[string]string{"test-field": "test-value"}, types.WindowsDesktopSpecV3{
		Addr:   "addr:3389", // test stripping off rdp port
		HostID: "host",
	})
	require.NoError(t, err)
	resource2, err := types.NewWindowsDesktopV3("desktop2", map[string]string{"test-field": "test-value2"}, types.WindowsDesktopSpecV3{
		Addr:   "addr",
		HostID: "host",
	})
	require.NoError(t, err)

	err = env.server.Auth().UpsertWindowsDesktop(context.Background(), resource)
	require.NoError(t, err)
	err = env.server.Auth().UpsertWindowsDesktop(context.Background(), resource2)
	require.NoError(t, err)

	// Make the call.
	query := url.Values{"sort": []string{"name"}}
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "desktops")
	re, err := pack.clt.Get(context.Background(), endpoint, query)
	require.NoError(t, err)

	// Test correct response.
	resp := testResponse{}
	require.NoError(t, json.Unmarshal(re.Bytes(), &resp))
	require.Len(t, resp.Items, 2)
	require.Equal(t, 2, resp.TotalCount)
	require.ElementsMatch(t, resp.Items, []ui.Desktop{{
		OS:     constants.WindowsOS,
		Name:   "desktop1",
		Addr:   "addr",
		Labels: []ui.Label{{Name: "test-field", Value: "test-value"}},
		HostID: "host",
	}, {
		OS:     constants.WindowsOS,
		Name:   "desktop2",
		Addr:   "addr",
		Labels: []ui.Label{{Name: "test-field", Value: "test-value2"}},
		HostID: "host",
	}})
}

func TestDesktopActive(t *testing.T) {
	desktopName := "rickey-rock"
	env := newWebPack(t, 1)
	ctx := context.Background()

	role, err := types.NewRole("admin", types.RoleSpecV5{
		Allow: types.RoleConditions{
			WindowsDesktopLabels: types.Labels{"environment": []string{"dev"}},
		},
	})
	require.NoError(t, err)

	pack := env.proxies[0].authPack(t, "foo", []types.Role{role})

	check := func(match string) {
		resp, err := pack.clt.Get(ctx, pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "desktops", desktopName, "active"), url.Values{})
		require.NoError(t, err)
		require.Contains(t, string(resp.Bytes()), match)
	}

	check("\"active\":false")
	desktop, err := types.NewWindowsDesktopV3(desktopName, map[string]string{"environment": "dev"}, types.WindowsDesktopSpecV3{
		Domain: "ad",
		Addr:   "foo",
		HostID: "bar",
	})
	require.NoError(t, err)
	err = env.server.Auth().CreateWindowsDesktop(ctx, desktop)
	require.NoError(t, err)
	tracker, err := types.NewSessionTracker(types.SessionTrackerSpecV1{
		SessionID:   "foo",
		Kind:        string(types.WindowsDesktopSessionKind),
		State:       types.SessionState_SessionStateRunning,
		DesktopName: desktopName,
	})
	require.NoError(t, err)
	_, err = env.server.Auth().CreateSessionTracker(ctx, tracker)
	require.NoError(t, err)
	check("\"active\":true")
}

func TestGetUserOrResetToken(t *testing.T) {
	env := newWebPack(t, 1)
	ctx := context.Background()
	username := "someuser"

	// Create a username.
	teleUser, err := types.NewUser(username)
	require.NoError(t, err)
	teleUser.SetLogins([]string{"login1"})
	require.NoError(t, env.server.Auth().CreateUser(ctx, teleUser))

	// Create a reset password token and secrets.
	resetToken, err := env.server.Auth().CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: username,
		Type: auth.UserTokenTypeResetPasswordInvite,
	})
	require.NoError(t, err)

	pack := env.proxies[0].authPack(t, "foo", nil /* roles */)

	// the default roles of foo don't have users read but we need it on our tests
	fooRole, err := env.server.Auth().GetRole(ctx, "user:foo")
	require.NoError(t, err)
	fooAllowRules := fooRole.GetRules(types.Allow)
	fooAllowRules = append(fooAllowRules, types.NewRule(types.KindUser, services.RO()))
	fooRole.SetRules(types.Allow, fooAllowRules)
	require.NoError(t, env.server.Auth().UpsertRole(ctx, fooRole))

	resp, err := pack.clt.Get(ctx, pack.clt.Endpoint("webapi", "users", username), url.Values{})
	require.NoError(t, err)
	require.Contains(t, string(resp.Bytes()), "login1")

	resp, err = pack.clt.Get(ctx, pack.clt.Endpoint("webapi", "users", "password", "token", resetToken.GetName()), url.Values{})
	require.NoError(t, err)
	require.Equal(t, resp.Code(), http.StatusOK)

	_, err = pack.clt.Get(ctx, pack.clt.Endpoint("webapi", "users", "password", "notToken", resetToken.GetName()), url.Values{})
	require.True(t, trace.IsNotFound(err))
}

func TestListConnectionsDiagnostic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	username := "someuser"
	diagName := "diag1"
	roleROConnectionDiagnostics, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindConnectionDiagnostic,
					[]string{types.VerbRead}),
			},
		},
	})
	require.NoError(t, err)

	env := newWebPack(t, 1)
	clusterName := env.server.ClusterName()
	pack := env.proxies[0].authPack(t, username, []types.Role{roleROConnectionDiagnostics})

	connectionsEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "diagnostics", "connections", diagName)

	// No connection diagnostics so far, should return not found
	_, err = pack.clt.Get(ctx, connectionsEndpoint, url.Values{})
	require.True(t, trace.IsNotFound(err))

	connectionDiagnostic, err := types.NewConnectionDiagnosticV1(diagName, map[string]string{}, types.ConnectionDiagnosticSpecV1{
		Success: true,
		Message: "success for cd0",
	})
	require.NoError(t, err)
	require.NoError(t, env.server.Auth().CreateConnectionDiagnostic(ctx, connectionDiagnostic))

	resp, err := pack.clt.Get(ctx, connectionsEndpoint, url.Values{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Code())

	var receivedConnectionDiagnostic ui.ConnectionDiagnostic
	require.NoError(t, json.Unmarshal(resp.Bytes(), &receivedConnectionDiagnostic))

	require.True(t, receivedConnectionDiagnostic.Success)
	require.Equal(t, receivedConnectionDiagnostic.ID, diagName)
	require.Equal(t, receivedConnectionDiagnostic.Message, "success for cd0")

	diag, err := env.server.Auth().GetConnectionDiagnostic(ctx, diagName)
	require.NoError(t, err)

	// Adding traces
	diag.AppendTrace(&types.ConnectionDiagnosticTrace{
		Type:    types.ConnectionDiagnosticTrace_RBAC_NODE,
		Status:  types.ConnectionDiagnosticTrace_SUCCESS,
		Details: "some details",
	})
	diag.SetMessage("after update")
	require.NoError(t, env.server.Auth().UpdateConnectionDiagnostic(ctx, diag))

	resp, err = pack.clt.Get(ctx, connectionsEndpoint, url.Values{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Code())

	require.NoError(t, json.Unmarshal(resp.Bytes(), &receivedConnectionDiagnostic))

	require.True(t, receivedConnectionDiagnostic.Success)
	require.Equal(t, receivedConnectionDiagnostic.ID, diagName)
	require.Equal(t, receivedConnectionDiagnostic.Message, "after update")
	require.Len(t, receivedConnectionDiagnostic.Traces, 1)
	require.NotNil(t, receivedConnectionDiagnostic.Traces[0])
	require.Equal(t, receivedConnectionDiagnostic.Traces[0].Details, "some details")
}

func TestDiagnoseSSHConnection(t *testing.T) {
	ctx := context.Background()

	osUser, err := user.Current()
	require.NoError(t, err)

	osUsername := osUser.Username
	require.NotEmpty(t, osUsername)

	roleWithFullAccess := func(username string, login string) []types.Role {
		ret, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
			Allow: types.RoleConditions{
				Namespaces: []string{apidefaults.Namespace},
				NodeLabels: types.Labels{types.Wildcard: []string{types.Wildcard}},
				Rules: []types.Rule{
					types.NewRule(types.KindConnectionDiagnostic, services.RW()),
				},
				Logins: []string{login},
			},
		})
		require.NoError(t, err)
		return []types.Role{ret}
	}
	require.NotNil(t, roleWithFullAccess)

	rolesWithoutAccessToNode := func(username string, login string) []types.Role {
		ret, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
			Allow: types.RoleConditions{
				Namespaces: []string{apidefaults.Namespace},
				NodeLabels: types.Labels{"forbidden": []string{"yes"}},
				Rules: []types.Rule{
					types.NewRule(types.KindConnectionDiagnostic, services.RW()),
				},
				Logins: []string{login},
			},
		})
		require.NoError(t, err)
		return []types.Role{ret}
	}
	require.NotNil(t, rolesWithoutAccessToNode)

	roleWithPrincipal := func(username string, principal string) []types.Role {
		ret, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
			Allow: types.RoleConditions{
				Namespaces: []string{apidefaults.Namespace},
				NodeLabels: types.Labels{types.Wildcard: []string{types.Wildcard}},
				Rules: []types.Rule{
					types.NewRule(types.KindConnectionDiagnostic, services.RW()),
				},
				Logins: []string{principal},
			},
		})
		require.NoError(t, err)
		return []types.Role{ret}
	}
	require.NotNil(t, roleWithPrincipal)

	env := newWebPack(t, 1)
	nodeName := env.node.GetInfo().GetHostname()

	for _, tt := range []struct {
		name            string
		teleportUser    string
		roles           []types.Role
		resourceName    string
		nodeUser        string
		stopNode        bool
		expectedSuccess bool
		expectedMessage string
		expectedTraces  []types.ConnectionDiagnosticTrace
	}{
		{
			name:            "success",
			roles:           roleWithFullAccess("success", osUsername),
			teleportUser:    "success",
			resourceName:    nodeName,
			nodeUser:        osUsername,
			expectedSuccess: true,
			expectedMessage: "success",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_NODE,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "You have access to the Node.",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Node is alive and reachable.",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "The requested principal is allowed.",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_NODE_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: fmt.Sprintf("%q user exists in target node", osUsername),
				},
			},
		},
		{
			name:            "node not found",
			roles:           roleWithFullAccess("nodenotfound", osUsername),
			teleportUser:    "nodenotfound",
			resourceName:    "notanode",
			nodeUser:        osUsername,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Failed to connect to the Node. Ensure teleport service is running using "systemctl status teleport".`,
					Error:   "Teleport proxy failed to connect to",
				},
			},
		},
		{
			name:            "node not reachable",
			teleportUser:    "nodenotreachable",
			roles:           roleWithFullAccess("nodenotreachable", osUsername),
			resourceName:    nodeName,
			nodeUser:        osUsername,
			stopNode:        true,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Failed to connect to the Node. Ensure teleport service is running using "systemctl status teleport".`,
					Error:   "Teleport proxy failed to connect to",
				},
			},
		},
		{
			name:            "no access to node",
			teleportUser:    "userwithoutaccess",
			roles:           rolesWithoutAccessToNode("userwithoutaccess", osUsername),
			resourceName:    nodeName,
			nodeUser:        osUsername,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_NODE,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: "You are not authorized to access this node. Ensure your role grants access by adding it to the 'node_labels' property.",
					Error:   fmt.Sprintf("user userwithoutaccess@localhost is not authorized to login as %s@localhost: access to node denied", osUsername),
				},
			},
		},
		{
			name:            "selected principal is not part of the allowed principals",
			teleportUser:    "deniedprincipal",
			roles:           roleWithFullAccess("deniedprincipal", "otherprincipal"),
			resourceName:    nodeName,
			nodeUser:        osUsername,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Principal "` + osUsername + `" is not allowed by this certificate. Ensure your roles grants access by adding it to the 'login' property.`,
					Error:   `ssh: principal "` + osUsername + `" not in the set of valid principals for given certificate: ["otherprincipal" "-teleport-internal-join"]`,
				},
			},
		},
		{
			name:            "principal doesnt exist in target host",
			teleportUser:    "principaldoesnotexist",
			roles:           roleWithPrincipal("principaldoesnotexist", "nonvalidlinuxuser"),
			resourceName:    nodeName,
			nodeUser:        "nonvalidlinuxuser",
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_NODE_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Invalid user. Please ensure the principal "nonvalidlinuxuser" is a valid Linux login in the target node. Output from Node: Failed to launch: user: unknown user nonvalidlinuxuser.`,
					Error:   "Process exited with status 255",
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			localEnv := env

			if tt.stopNode {
				localEnv = newWebPack(t, 1)
				require.NoError(t, localEnv.node.Close())
			}

			clusterName := localEnv.server.ClusterName()
			pack := localEnv.proxies[0].authPack(t, tt.teleportUser, tt.roles)

			createConnectionEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "diagnostics", "connections")

			resp, err := pack.clt.PostJSON(ctx, createConnectionEndpoint, conntest.TestConnectionRequest{
				ResourceKind: types.KindNode,
				ResourceName: tt.resourceName,
				SSHPrincipal: tt.nodeUser,
				// Default is 30 seconds but since tests run locally, we can reduce this value to also improve test responsiveness
				DialTimeout: time.Second,
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.Code())

			var connectionDiagnostic ui.ConnectionDiagnostic
			require.NoError(t, json.Unmarshal(resp.Bytes(), &connectionDiagnostic))

			gotFailedTraces := 0
			expectedFailedTraces := 0

			t.Log(tt.name)
			t.Log(connectionDiagnostic.Message, connectionDiagnostic.Success)
			for i, trace := range connectionDiagnostic.Traces {
				if trace.Status == types.ConnectionDiagnosticTrace_FAILED.String() {
					gotFailedTraces++
				}

				t.Logf("%d status='%s' type='%s' details='%s' error='%s'\n", i, trace.Status, trace.TraceType, trace.Details, trace.Error)
			}

			require.Equal(t, tt.expectedSuccess, connectionDiagnostic.Success)
			require.Equal(t, tt.expectedMessage, connectionDiagnostic.Message)

			for _, expectedTrace := range tt.expectedTraces {
				if expectedTrace.Status == types.ConnectionDiagnosticTrace_FAILED {
					expectedFailedTraces++
				}

				foundTrace := false
				for _, returnedTrace := range connectionDiagnostic.Traces {
					if expectedTrace.Type.String() != returnedTrace.TraceType {
						continue
					}

					foundTrace = true
					require.Equal(t, returnedTrace.Status, expectedTrace.Status.String())
					require.Equal(t, returnedTrace.Details, expectedTrace.Details)
					require.Contains(t, returnedTrace.Error, expectedTrace.Error)
				}

				require.True(t, foundTrace, "expected trace %v was not found", expectedTrace)
			}
			require.Equal(t, expectedFailedTraces, gotFailedTraces)
		})
	}
}

func TestDiagnoseKubeConnection(t *testing.T) {

	var (
		validKubeUsers              = []string{}
		multiKubeUsers              = []string{"user1", "user2"}
		validKubeGroups             = []string{"validKubeGroup"}
		invalidKubeGroups           = []string{"invalidKubeGroups"}
		kubeClusterName             = "kube_cluster"
		disconnectedKubeClustername = "dis_kube_cluster"
		ctx                         = context.Background()
	)

	roleWithFullAccess := func(username string, kubeUsers, kubeGroups []string) []types.Role {
		ret, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
			Allow: types.RoleConditions{
				Namespaces:       []string{apidefaults.Namespace},
				KubernetesLabels: types.Labels{types.Wildcard: []string{types.Wildcard}},
				Rules: []types.Rule{
					types.NewRule(types.KindConnectionDiagnostic, services.RW()),
				},
				KubeGroups: kubeGroups,
				KubeUsers:  kubeUsers,
			},
		})
		require.NoError(t, err)
		return []types.Role{ret}
	}
	require.NotNil(t, roleWithFullAccess)

	rolesWithoutAccessToKubeCluster := func(username string, kubeUsers, kubeGroups []string) []types.Role {
		ret, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
			Allow: types.RoleConditions{
				Namespaces:       []string{apidefaults.Namespace},
				KubernetesLabels: types.Labels{"forbidden": []string{"yes"}},
				Rules: []types.Rule{
					types.NewRule(types.KindConnectionDiagnostic, services.RW()),
				},
				KubeGroups: kubeGroups,
				KubeUsers:  kubeUsers,
			},
		})
		require.NoError(t, err)
		return []types.Role{ret}
	}
	require.NotNil(t, rolesWithoutAccessToKubeCluster)

	env := newWebPack(t, 1)

	rt := http.NewServeMux()
	rt.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(r.Header.Values("Impersonate-Group"), invalidKubeGroups[0]) {
			marshalRBACError(t, w)
			return
		}
		marshalValidPodList(t, w)
	})
	testKube := httptest.NewTLSServer(rt)
	t.Cleanup(func() {
		testKube.Close()
	})

	startKube(
		ctx,
		t,
		startKubeOptions{
			serviceType: kubeproxy.KubeService,
			authServer:  env.server.TLS,
			clusters: []kubeClusterConfig{
				{
					name:        kubeClusterName,
					apiEndpoint: testKube.URL,
				},
			},
		},
	)

	for _, tt := range []struct {
		name               string
		teleportUser       string
		roleFunc           func(string, []string, []string) []types.Role
		kubeUsers          []string
		kubeGroups         []string
		resourceName       string
		selectedKubeUser   string
		selectedKubeGroups []string
		expectedSuccess    bool
		disconnectedKube   bool
		expectedMessage    string
		expectedTraces     []types.ConnectionDiagnosticTrace
	}{
		{
			name:            "kube cluster not found",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      validKubeGroups,
			kubeUsers:       validKubeUsers,
			teleportUser:    "notfound",
			resourceName:    "notregistered",
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Failed to connect to Kubernetes cluster. Ensure the cluster is registered and online.`,
					Error:   "kubernetes cluster \"notregistered\" is not registered or is offline",
				},
			},
		},
		{
			name:             "kube cluster disconnected",
			roleFunc:         roleWithFullAccess,
			kubeGroups:       validKubeGroups,
			kubeUsers:        validKubeUsers,
			teleportUser:     "disconnected",
			resourceName:     disconnectedKubeClustername,
			disconnectedKube: true,
			expectedSuccess:  false,
			expectedMessage:  "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `Failed to connect to Kubernetes cluster. Ensure the cluster is registered and online.`,
					Error:   fmt.Sprintf("kubernetes cluster %q is not registered or is offline", disconnectedKubeClustername),
				},
			},
		},
		{
			name:            "no access to kube cluster",
			teleportUser:    "userwithoutaccess",
			roleFunc:        rolesWithoutAccessToKubeCluster,
			kubeGroups:      validKubeGroups,
			kubeUsers:       validKubeUsers,
			resourceName:    kubeClusterName,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "User-associated roles define valid Kubernetes principals.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_KUBE,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: "You are not authorized to access this Kubernetes Cluster. Ensure your role grants access by adding it to the 'kubernetes_labels' property.",
					Error:   "[00] access denied",
				},
			},
		},
		{
			name:            "no kube principals",
			teleportUser:    "userwithoutprincipals",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      nil,
			kubeUsers:       nil,
			resourceName:    kubeClusterName,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: "User-associated roles do not configure \"kubernetes_groups\" or \"kubernetes_users\". Make sure that at least one is configured for the user.",
					Error:   "this user cannot request kubernetes access, has no assigned groups or users",
				},
			},
		},
		{
			name:            "teleport access but Kube RBAC fails",
			teleportUser:    "userbadrbac",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      invalidKubeGroups,
			kubeUsers:       validKubeUsers,
			resourceName:    kubeClusterName,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "User-associated roles define valid Kubernetes principals.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_KUBE_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: "You are not allowed to list pods in the \"default\" namespace. Make sure your \"kubernetes_groups\" or \"kubernetes_users\" exist in the cluster and grant you access to list pods.",
					Error:   "pods is forbidden: User \"USER\" cannot list resource \"pods\" in API group \"\" in the namespace \"default\"",
				},
			},
		},
		{
			name:            "user with multiple defined kube_users",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      validKubeGroups,
			kubeUsers:       multiKubeUsers,
			teleportUser:    "multiuser",
			resourceName:    kubeClusterName,
			expectedSuccess: false,
			expectedMessage: "failed",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `User-associated roles define multiple "kubernetes_users". Make sure that only one value is defined or that you select the target user.`,
					Error:   "please select a user to impersonate, refusing to select a user due to several kubernetes_users set up for this user",
				},
			},
		},
		{
			name:             "user choosed to impersonate invalid kube_users",
			roleFunc:         roleWithFullAccess,
			kubeGroups:       validKubeGroups,
			kubeUsers:        multiKubeUsers,
			teleportUser:     "userwithWrongImpUser",
			resourceName:     kubeClusterName,
			expectedSuccess:  false,
			expectedMessage:  "failed",
			selectedKubeUser: "missingUser",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `User-associated roles do now allow the desired "kubernetes_user" impersonation. Please define a "kubernetes_user" that your roles allow to impersonate.`,
					Error:   `impersonation request has been denied, user header "missingUser" is not allowed in roles`,
				},
			},
		},
		{
			name:               "user choosed to impersonate invalid kube_group",
			roleFunc:           roleWithFullAccess,
			kubeGroups:         validKubeGroups,
			kubeUsers:          multiKubeUsers,
			teleportUser:       "userwithWrongImpGroup",
			resourceName:       kubeClusterName,
			expectedSuccess:    false,
			expectedMessage:    "failed",
			selectedKubeUser:   "user1",
			selectedKubeGroups: []string{"missingGroup"},
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_FAILED,
					Details: `User-associated roles do now allow the desired "kubernetes_group" impersonation. Please define a "kubernetes_group" that your roles allow to impersonate.`,
					Error:   `impersonation request has been denied, group header "missingGroup" value is not allowed in roles`,
				},
			},
		},
		{
			name:            "user with multiple defined kube_users",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      validKubeGroups,
			kubeUsers:       validKubeUsers,
			teleportUser:    "successwithmultiusers",
			resourceName:    kubeClusterName,
			expectedSuccess: true,
			expectedMessage: "success",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "User-associated roles define valid Kubernetes principals.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_KUBE,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "You are authorized to access this Kubernetes Cluster.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_KUBE_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Access to the Kubernetes Cluster granted.",
					Error:   "",
				},
			},
		},
		{
			name:            "success",
			roleFunc:        roleWithFullAccess,
			kubeGroups:      validKubeGroups,
			kubeUsers:       validKubeUsers,
			teleportUser:    "success",
			resourceName:    kubeClusterName,
			expectedSuccess: true,
			expectedMessage: "success",
			expectedTraces: []types.ConnectionDiagnosticTrace{
				{
					Type:    types.ConnectionDiagnosticTrace_CONNECTIVITY,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Kubernetes Cluster is registered in Teleport.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "User-associated roles define valid Kubernetes principals.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_RBAC_KUBE,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "You are authorized to access this Kubernetes Cluster.",
					Error:   "",
				},
				{
					Type:    types.ConnectionDiagnosticTrace_KUBE_PRINCIPAL,
					Status:  types.ConnectionDiagnosticTrace_SUCCESS,
					Details: "Access to the Kubernetes Cluster granted.",
					Error:   "",
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			localEnv := env

			if tt.disconnectedKube {
				kubeServer, cleanup, _ := startKubeWithoutCleanup(ctx, t, startKubeOptions{
					serviceType: kubeproxy.KubeService,
					authServer:  env.server.TLS,
					clusters: []kubeClusterConfig{
						{
							name:        tt.resourceName,
							apiEndpoint: testKube.URL,
						},
					},
				})
				err := kubeServer.Close()
				require.NoError(t, err)
				require.NoError(t, cleanup())
			}

			clusterName := localEnv.server.ClusterName()
			roles := tt.roleFunc(tt.teleportUser, tt.kubeUsers, tt.kubeGroups)
			pack := localEnv.proxies[0].authPack(t, tt.teleportUser, roles)

			createConnectionEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "diagnostics", "connections")

			resp, err := pack.clt.PostJSON(ctx, createConnectionEndpoint, conntest.TestConnectionRequest{
				ResourceKind: types.KindKubernetesCluster,
				ResourceName: tt.resourceName,
				// Default is 30 seconds but since tests run locally, we can reduce this value to also improve test responsiveness
				DialTimeout: time.Second,
				KubernetesImpersonation: conntest.KubernetesImpersonation{
					KubernetesUser:   tt.selectedKubeUser,
					KubernetesGroups: tt.selectedKubeGroups,
				},
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.Code())

			var connectionDiagnostic ui.ConnectionDiagnostic
			require.NoError(t, json.Unmarshal(resp.Bytes(), &connectionDiagnostic))
			gotFailedTraces := 0
			expectedFailedTraces := 0

			t.Log(tt.name)
			t.Log(connectionDiagnostic.Message, connectionDiagnostic.Success)
			for i, trace := range connectionDiagnostic.Traces {
				if trace.Status == types.ConnectionDiagnosticTrace_FAILED.String() {
					gotFailedTraces++
				}

				t.Logf("%d status='%s' type='%s' details='%s' error='%s'\n", i, trace.Status, trace.TraceType, trace.Details, trace.Error)
			}

			require.Equal(t, tt.expectedSuccess, connectionDiagnostic.Success)
			require.Equal(t, tt.expectedMessage, connectionDiagnostic.Message)

			for _, expectedTrace := range tt.expectedTraces {
				if expectedTrace.Status == types.ConnectionDiagnosticTrace_FAILED {
					expectedFailedTraces++
				}

				foundTrace := false
				for _, returnedTrace := range connectionDiagnostic.Traces {
					if expectedTrace.Type.String() != returnedTrace.TraceType {
						continue
					}

					foundTrace = true
					require.Equal(t, returnedTrace.Status, expectedTrace.Status.String())
					require.Equal(t, returnedTrace.Details, expectedTrace.Details)
					require.Contains(t, returnedTrace.Error, expectedTrace.Error)
				}

				require.True(t, foundTrace, expectedTrace)
			}

			require.Equal(t, expectedFailedTraces, gotFailedTraces)
		})
	}
}

func TestCreateDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	username := "someuser"
	roleCreateDatabase, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindDatabase,
					[]string{types.VerbCreate}),
			},
			DatabaseLabels: types.Labels{
				types.Wildcard: {types.Wildcard},
			},
		},
	})
	require.NoError(t, err)

	env := newWebPack(t, 1)
	clusterName := env.server.ClusterName()
	pack := env.proxies[0].authPack(t, username, []types.Role{roleCreateDatabase})

	createDatabaseEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "databases")

	for _, tt := range []struct {
		name           string
		req            createDatabaseRequest
		expectedStatus int
		errAssert      require.ErrorAssertionFunc
	}{
		{
			name: "valid",
			req: createDatabaseRequest{
				Name:     "mydatabase",
				Protocol: "mysql",
				URI:      "someuri:3306",
			},
			expectedStatus: http.StatusOK,
			errAssert:      require.NoError,
		},
		{
			name: "valid with labels",
			req: createDatabaseRequest{
				Name:     "dbwithlabels",
				Protocol: "mysql",
				URI:      "someuri:3306",
				Labels: []ui.Label{
					{
						Name:  "env",
						Value: "prod",
					},
				},
			},
			expectedStatus: http.StatusOK,
			errAssert:      require.NoError,
		},
		{
			name: "empty name",
			req: createDatabaseRequest{
				Name:     "",
				Protocol: "mysql",
				URI:      "someuri:3306",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "missing database name")
			},
		},
		{
			name: "empty protocol",
			req: createDatabaseRequest{
				Name:     "emptyprotocol",
				Protocol: "",
				URI:      "someuri:3306",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "missing protocol")
			},
		},
		{
			name: "empty uri",
			req: createDatabaseRequest{
				Name:     "emptyuri",
				Protocol: "mysql",
				URI:      "",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "missing uri")
			},
		},
		{
			name: "missing port",
			req: createDatabaseRequest{
				Name:     "missingport",
				Protocol: "mysql",
				URI:      "someuri",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "missing port in address")
			},
		},
	} {
		// Create database
		resp, err := pack.clt.PostJSON(ctx, createDatabaseEndpoint, tt.req)
		tt.errAssert(t, err)

		require.Equal(t, resp.Code(), tt.expectedStatus, "invalid status code received")

		if err != nil {
			continue
		}

		// Ensure database exists
		database, err := env.proxies[0].client.GetDatabase(ctx, tt.req.Name)
		require.NoError(t, err)

		require.Equal(t, database.GetName(), tt.req.Name)
		require.Equal(t, database.GetProtocol(), tt.req.Protocol)
		require.Equal(t, database.GetURI(), tt.req.URI)

		// At least the provided labels exist in the database resource
		databaseLabels := database.GetAllLabels()
		for _, label := range tt.req.Labels {
			require.Contains(t, databaseLabels, label.Name, "label not found")
			require.Equal(t, label.Value, databaseLabels[label.Name], "label exists but has unexpected value")
		}
	}
}

func TestUpdateDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseName := "somedb"
	username := "someuser"
	roleCreateUpdateDatabase, err := types.NewRole(services.RoleNameForUser(username), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Rules: []types.Rule{
				types.NewRule(types.KindDatabase,
					[]string{types.VerbCreate, types.VerbUpdate, types.VerbRead}),
			},
			DatabaseLabels: types.Labels{
				types.Wildcard: {types.Wildcard},
			},
		},
	})
	require.NoError(t, err)

	env := newWebPack(t, 1)
	clusterName := env.server.ClusterName()
	pack := env.proxies[0].authPack(t, username, []types.Role{roleCreateUpdateDatabase})

	// Create database
	createDatabaseEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "databases")
	_, err = pack.clt.PostJSON(ctx, createDatabaseEndpoint, createDatabaseRequest{
		Name:     databaseName,
		Protocol: "mysql",
		URI:      "someuri:3306",
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name           string
		req            updateDatabaseRequest
		expectedStatus int
		errAssert      require.ErrorAssertionFunc
	}{
		{
			name: "valid",
			req: updateDatabaseRequest{
				CACert: fakeValidTLSCert,
			},
			expectedStatus: http.StatusOK,
			errAssert:      require.NoError,
		},
		{
			name: "empty ca_cert",
			req: updateDatabaseRequest{
				CACert: "",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "missing CA certificate data")
			},
		},
		{
			name: "invalid certificate",
			req: updateDatabaseRequest{
				CACert: "Not a certificate",
			},
			expectedStatus: http.StatusBadRequest,
			errAssert: func(tt require.TestingT, err error, i ...interface{}) {
				require.ErrorContains(t, err, "could not parse provided CA as X.509 PEM certificate")
			},
		},
	} {
		// Update database's CA Cert
		updateDatabaseEndpoint := pack.clt.Endpoint("webapi", "sites", clusterName, "databases", databaseName)
		resp, err := pack.clt.PutJSON(ctx, updateDatabaseEndpoint, tt.req)
		tt.errAssert(t, err)

		require.Equal(t, resp.Code(), tt.expectedStatus, "invalid status code received")

		if err != nil {
			continue
		}

		// Ensure database was updated
		database, err := env.proxies[0].client.GetDatabase(ctx, databaseName)
		require.NoError(t, err)

		require.Equal(t, database.GetCA(), fakeValidTLSCert)
	}
}

type authProviderMock struct {
	server types.ServerV2
}

func (mock authProviderMock) GetNodes(ctx context.Context, n string) ([]types.Server, error) {
	return []types.Server{&mock.server}, nil
}

func (mock authProviderMock) GetSessionEvents(n string, s session.ID, c int, p bool) ([]events.EventFields, error) {
	return []events.EventFields{}, nil
}

func (mock authProviderMock) GetSessionTracker(ctx context.Context, sessionID string) (types.SessionTracker, error) {
	return nil, trace.NotFound("foo")
}

func (mock authProviderMock) IsMFARequired(ctx context.Context, req *authproto.IsMFARequiredRequest) (*authproto.IsMFARequiredResponse, error) {
	return nil, nil
}

func (mock authProviderMock) GenerateUserSingleUseCerts(ctx context.Context) (authproto.AuthService_GenerateUserSingleUseCertsClient, error) {
	return nil, nil
}

type terminalOpt func(t *TerminalRequest)

func withSessionID(sid session.ID) terminalOpt {
	return func(t *TerminalRequest) { t.SessionID = sid }
}

func withKeepaliveInterval(d time.Duration) terminalOpt {
	return func(t *TerminalRequest) { t.KeepAliveInterval = d }
}

func (s *WebSuite) makeTerminal(t *testing.T, pack *authPack, opts ...terminalOpt) (*websocket.Conn, *session.Session, error) {
	req := TerminalRequest{
		Server: s.srvID,
		Login:  pack.login,
		Term: session.TerminalParams{
			W: 100,
			H: 100,
		},
	}
	for _, opt := range opts {
		opt(&req)
	}

	u := url.URL{
		Host:   s.url().Host,
		Scheme: client.WSS,
		Path:   fmt.Sprintf("/v1/webapi/sites/%v/connect", currentSiteShortcut),
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}

	q := u.Query()
	q.Set("params", string(data))
	q.Set(roundtrip.AccessTokenQueryParam, pack.session.Token)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{}
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	header := http.Header{}
	header.Add("Origin", "http://localhost")
	for _, cookie := range pack.cookies {
		header.Add("Cookie", cookie.String())
	}

	ws, resp, err := dialer.Dial(u.String(), header)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	ty, raw, err := ws.ReadMessage()
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	require.Equal(t, websocket.BinaryMessage, ty)
	var env Envelope

	err = proto.Unmarshal(raw, &env)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	var sessResp siteSessionGenerateResponse

	err = json.Unmarshal([]byte(env.Payload), &sessResp)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	err = resp.Body.Close()
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	return ws, &sessResp.Session, nil
}

func waitForOutput(stream *terminalStream, substr string) error {
	timeoutCh := time.After(10 * time.Second)

	for {
		select {
		case <-timeoutCh:
			return trace.BadParameter("timeout waiting on terminal for output: %v", substr)
		default:
		}

		out := make([]byte, 100)
		_, err := stream.Read(out)
		if err != nil {
			return trace.Wrap(err)
		}
		if strings.Contains(removeSpace(string(out)), substr) {
			return nil
		}
	}
}

func (s *WebSuite) clientNoRedirects(opts ...roundtrip.ClientParam) *client.WebClient {
	hclient := client.NewInsecureWebClient()
	hclient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	opts = append(opts, roundtrip.HTTPClient(hclient))
	wc, err := client.NewWebClient(s.url().String(), opts...)
	if err != nil {
		panic(err)
	}
	return wc
}

func (s *WebSuite) client(opts ...roundtrip.ClientParam) *client.WebClient {
	opts = append(opts, roundtrip.HTTPClient(client.NewInsecureWebClient()))
	wc, err := client.NewWebClient(s.url().String(), opts...)
	if err != nil {
		panic(err)
	}
	return wc
}

func (s *WebSuite) login(clt *client.WebClient, cookieToken string, reqToken string, reqData interface{}) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(clt.RoundTrip(func() (*http.Response, error) {
		data, err := json.Marshal(reqData)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest("POST", clt.Endpoint("webapi", "sessions"), bytes.NewBuffer(data))
		if err != nil {
			return nil, err
		}
		addCSRFCookieToReq(req, cookieToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrf.HeaderName, reqToken)
		return clt.HTTPClient().Do(req)
	}))
}

func (s *WebSuite) url() *url.URL {
	u, err := url.Parse("https://" + s.webServer.Listener.Addr().String())
	if err != nil {
		panic(err)
	}
	return u
}

func addCSRFCookieToReq(req *http.Request, token string) {
	cookie := &http.Cookie{
		Name:  csrf.CookieName,
		Value: token,
	}

	req.AddCookie(cookie)
}

func removeSpace(in string) string {
	for _, c := range []string{"\n", "\r", "\t"} {
		in = strings.Replace(in, c, " ", -1)
	}
	return strings.TrimSpace(in)
}

func newTerminalHandler() TerminalHandler {
	return TerminalHandler{
		log:     logrus.WithFields(logrus.Fields{}),
		encoder: unicode.UTF8.NewEncoder(),
		decoder: unicode.UTF8.NewDecoder(),
		wsLock:  &sync.Mutex{},
	}
}

func decodeSessionCookie(t *testing.T, value string) (sessionID string) {
	sessionBytes, err := hex.DecodeString(value)
	require.NoError(t, err)
	var cookie struct {
		User      string `json:"user"`
		SessionID string `json:"sid"`
	}
	require.NoError(t, json.Unmarshal(sessionBytes, &cookie))
	return cookie.SessionID
}

func (r CreateSessionResponse) response() (*CreateSessionResponse, error) {
	return &CreateSessionResponse{TokenType: r.TokenType, Token: r.Token, TokenExpiresIn: r.TokenExpiresIn, SessionInactiveTimeoutMS: r.SessionInactiveTimeoutMS}, nil
}

func newWebPack(t *testing.T, numProxies int) *webPack {
	ctx := context.Background()
	clock := clockwork.NewFakeClockAt(time.Now())

	server, err := auth.NewTestServer(auth.TestServerConfig{
		Auth: auth.TestAuthServerConfig{
			ClusterName: "localhost",
			Dir:         t.TempDir(),
			Clock:       clock,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Shutdown(ctx)) })

	// Register the auth server, since test auth server doesn't start its own
	// heartbeat.
	err = server.Auth().UpsertAuthServer(&types.ServerV2{
		Kind:    types.KindAuthServer,
		Version: types.V2,
		Metadata: types.Metadata{
			Namespace: apidefaults.Namespace,
			Name:      "auth",
		},
		Spec: types.ServerSpecV2{
			Addr:     server.TLS.Listener.Addr().String(),
			Hostname: "localhost",
			Version:  teleport.Version,
		},
	})
	require.NoError(t, err)

	priv, pub, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)

	tlsPub, err := auth.PrivateKeyToPublicKeyTLS(priv)
	require.NoError(t, err)

	// start auth server
	certs, err := server.Auth().GenerateHostCerts(ctx,
		&authproto.HostCertsRequest{
			HostID:       hostID,
			NodeName:     server.TLS.ClusterName(),
			Role:         types.RoleNode,
			PublicSSHKey: pub,
			PublicTLSKey: tlsPub,
		})
	require.NoError(t, err)

	signer, err := sshutils.NewSigner(priv, certs.SSH)
	require.NoError(t, err)
	hostSigners := []ssh.Signer{signer}

	const nodeID = "node"
	nodeClient, err := server.TLS.NewClient(auth.TestIdentity{
		I: auth.BuiltinRole{
			Role:     types.RoleNode,
			Username: nodeID,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, nodeClient.Close()) })

	nodeLockWatcher, err := services.NewLockWatcher(ctx, services.LockWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentNode,
			Client:    nodeClient,
		},
	})
	require.NoError(t, err)
	t.Cleanup(nodeLockWatcher.Close)

	nodeSessionController, err := srv.NewSessionController(srv.SessionControllerConfig{
		Semaphores:   nodeClient,
		AccessPoint:  nodeClient,
		LockEnforcer: nodeLockWatcher,
		Emitter:      nodeClient,
		Component:    teleport.ComponentNode,
		ServerID:     nodeID,
	})
	require.NoError(t, err)

	// create SSH service:
	nodeDataDir := t.TempDir()
	node, err := regular.New(
		ctx,
		utils.NetAddr{AddrNetwork: "tcp", Addr: "127.0.0.1:0"},
		server.TLS.ClusterName(),
		hostSigners,
		nodeClient,
		nodeDataDir,
		"",
		utils.NetAddr{},
		nodeClient,
		regular.SetUUID(nodeID),
		regular.SetNamespace(apidefaults.Namespace),
		regular.SetShell("/bin/sh"),
		regular.SetEmitter(nodeClient),
		regular.SetPAMConfig(&pam.Config{Enabled: false}),
		regular.SetBPF(&bpf.NOP{}),
		regular.SetRestrictedSessionManager(&restricted.NOP{}),
		regular.SetClock(clock),
		regular.SetLockWatcher(nodeLockWatcher),
		regular.SetSessionController(nodeSessionController),
	)
	require.NoError(t, err)

	require.NoError(t, node.Start())
	t.Cleanup(func() { require.NoError(t, node.Close()) })

	var proxies []*testProxy
	for p := 0; p < numProxies; p++ {
		proxyID := fmt.Sprintf("proxy%v", p)
		proxies = append(proxies, createProxy(ctx, t, proxyID, node, server.TLS, hostSigners, clock))
	}

	// Wait for proxies to fully register before starting the test.
	for start := time.Now(); ; {
		proxies, err := proxies[0].client.GetProxies()
		require.NoError(t, err)
		if len(proxies) == numProxies {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("Proxies didn't register within 5s after startup; registered: %d, want: %d", len(proxies), numProxies)
		}
	}

	return &webPack{
		proxies: proxies,
		server:  server,
		node:    node,
		clock:   clock,
	}
}

func createProxy(ctx context.Context, t *testing.T, proxyID string, node *regular.Server, authServer *auth.TestTLSServer,
	hostSigners []ssh.Signer, clock clockwork.FakeClock,
) *testProxy {
	// create reverse tunnel service:
	client, err := authServer.NewClient(auth.TestIdentity{
		I: auth.BuiltinRole{
			Role:     types.RoleProxy,
			Username: proxyID,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	revTunListener, err := net.Listen("tcp", fmt.Sprintf("%v:0", authServer.ClusterName()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, revTunListener.Close()) })

	proxyLockWatcher, err := services.NewLockWatcher(ctx, services.LockWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    client,
		},
	})
	require.NoError(t, err)
	t.Cleanup(proxyLockWatcher.Close)

	proxyCAWatcher, err := services.NewCertAuthorityWatcher(ctx, services.CertAuthorityWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    client,
		},
		Types: []types.CertAuthType{types.HostCA, types.UserCA},
	})
	require.NoError(t, err)
	t.Cleanup(proxyLockWatcher.Close)

	proxyNodeWatcher, err := services.NewNodeWatcher(ctx, services.NodeWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    client,
		},
	})
	require.NoError(t, err)
	t.Cleanup(proxyNodeWatcher.Close)

	revTunServer, err := reversetunnel.NewServer(reversetunnel.Config{
		ID:                    node.ID(),
		Listener:              revTunListener,
		ClientTLS:             client.TLSConfig(),
		ClusterName:           authServer.ClusterName(),
		HostSigners:           hostSigners,
		LocalAuthClient:       client,
		LocalAccessPoint:      client,
		Emitter:               client,
		NewCachingAccessPoint: noCache,
		DataDir:               t.TempDir(),
		LockWatcher:           proxyLockWatcher,
		NodeWatcher:           proxyNodeWatcher,
		CertAuthorityWatcher:  proxyCAWatcher,
		CircuitBreakerConfig:  breaker.NoopBreakerConfig(),
		LocalAuthAddresses:    []string{authServer.Listener.Addr().String()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, revTunServer.Close()) })

	router, err := proxy.NewRouter(proxy.RouterConfig{
		ClusterName:         authServer.ClusterName(),
		Log:                 utils.NewLoggerForTests().WithField(trace.Component, "test"),
		RemoteClusterGetter: client,
		SiteGetter:          revTunServer,
		TracerProvider:      tracing.NoopProvider(),
	})
	require.NoError(t, err)

	sessionController, err := srv.NewSessionController(srv.SessionControllerConfig{
		Semaphores:   client,
		AccessPoint:  client,
		LockEnforcer: proxyLockWatcher,
		Emitter:      client,
		Component:    teleport.ComponentProxy,
		ServerID:     proxyID,
	})
	require.NoError(t, err)

	proxyServer, err := regular.New(
		ctx,
		utils.NetAddr{AddrNetwork: "tcp", Addr: "127.0.0.1:0"},
		authServer.ClusterName(),
		hostSigners,
		client,
		t.TempDir(),
		"",
		utils.NetAddr{AddrNetwork: "tcp", Addr: "proxy-1.example.com:443"},
		client,
		regular.SetUUID(proxyID),
		regular.SetProxyMode("", revTunServer, client, router),
		regular.SetEmitter(client),
		regular.SetNamespace(apidefaults.Namespace),
		regular.SetBPF(&bpf.NOP{}),
		regular.SetRestrictedSessionManager(&restricted.NOP{}),
		regular.SetClock(clock),
		regular.SetLockWatcher(proxyLockWatcher),
		regular.SetNodeWatcher(proxyNodeWatcher),
		regular.SetSessionController(sessionController),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxyServer.Close()) })

	fs, err := NewDebugFileSystem("../../webassets/teleport")
	require.NoError(t, err)
	handler, err := NewHandler(Config{
		Proxy:            revTunServer,
		AuthServers:      utils.FromAddr(authServer.Addr()),
		DomainName:       authServer.ClusterName(),
		ProxyClient:      client,
		ProxyPublicAddrs: utils.MustParseAddrList("proxy-1.example.com", "proxy-2.example.com"),
		CipherSuites:     utils.DefaultCipherSuites(),
		AccessPoint:      client,
		Context:          ctx,
		HostUUID:         proxyID,
		Emitter:          client,
		StaticFS:         fs,
		ProxySettings:    &mockProxySettings{},
		SessionControl:   sessionController,
		Router:           router,
	}, SetSessionStreamPollPeriod(200*time.Millisecond), SetClock(clock))
	require.NoError(t, err)

	webServer := httptest.NewTLSServer(handler)
	t.Cleanup(webServer.Close)
	require.NoError(t, proxyServer.Start())

	proxyAddr := utils.MustParseAddr(proxyServer.Addr())
	addr := utils.MustParseAddr(webServer.Listener.Addr().String())
	handler.handler.cfg.ProxyWebAddr = *addr
	handler.handler.cfg.ProxySSHAddr = *proxyAddr

	_, sshPort, err := net.SplitHostPort(proxyAddr.String())
	require.NoError(t, err)
	handler.handler.sshPort = sshPort

	kubeProxyAddr := startKube(
		ctx,
		t,
		startKubeOptions{
			serviceType: kubeproxy.ProxyService,
			authServer:  authServer,
			revTunnel:   revTunServer,
		},
	)
	handler.handler.cfg.ProxyKubeAddr = utils.FromAddr(kubeProxyAddr)
	url, err := url.Parse("https://" + webServer.Listener.Addr().String())
	require.NoError(t, err)

	return &testProxy{
		clock:   clock,
		auth:    authServer,
		client:  client,
		revTun:  revTunServer,
		node:    node,
		proxy:   proxyServer,
		web:     webServer,
		handler: handler,
		webURL:  *url,
	}
}

// webPack represents the state of a single web test.
// It replicates most of the WebSuite and serves to gradually
// transition the test suite to use the testing package
// directly.
type webPack struct {
	proxies []*testProxy
	server  *auth.TestServer
	node    *regular.Server
	clock   clockwork.FakeClock
}

type testProxy struct {
	clock   clockwork.FakeClock
	client  *auth.Client
	auth    *auth.TestTLSServer
	revTun  reversetunnel.Server
	node    *regular.Server
	proxy   *regular.Server
	handler *APIHandler
	web     *httptest.Server
	webURL  url.URL
}

// authPack returns new authenticated package consisting of created valid
// user, otp token, created web session and authenticated client.
func (r *testProxy) authPack(t *testing.T, teleportUser string, roles []types.Role) *authPack {
	ctx := context.Background()
	const (
		pass      = "abc123"
		rawSecret = "def456"
	)

	u, err := user.Current()
	require.NoError(t, err)
	loginUser := u.Username

	otpSecret := base32.StdEncoding.EncodeToString([]byte(rawSecret))

	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOTP,
	})
	require.NoError(t, err)

	err = r.auth.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	r.createUser(context.Background(), t, teleportUser, loginUser, pass, otpSecret, roles)

	// create a valid otp token
	validToken, err := totp.GenerateCode(otpSecret, r.clock.Now())
	require.NoError(t, err)

	clt := r.newClient(t)
	req := CreateSessionReq{
		User:              teleportUser,
		Pass:              pass,
		SecondFactorToken: validToken,
	}

	csrfToken := "2ebcb768d0090ea4368e42880c970b61865c326172a4a2343b645cf5d7f20992"
	resp := login(t, clt, csrfToken, csrfToken, req)

	var rawSession *CreateSessionResponse
	require.NoError(t, json.Unmarshal(resp.Bytes(), &rawSession))

	session, err := rawSession.response()
	require.NoError(t, err)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	clt = r.newClient(t, roundtrip.BearerAuth(session.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(&r.webURL, resp.Cookies())

	return &authPack{
		otpSecret: otpSecret,
		user:      teleportUser,
		login:     loginUser,
		session:   session,
		clt:       clt,
		cookies:   resp.Cookies(),
		password:  pass,
	}
}

func (r *testProxy) authPackFromPack(t *testing.T, pack *authPack) *authPack {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	clt := r.newClient(t, roundtrip.BearerAuth(pack.session.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(&r.webURL, pack.cookies)

	result := *pack
	result.clt = clt
	return &result
}

func (r *testProxy) authPackFromResponse(t *testing.T, httpResp *roundtrip.Response) *authPack {
	var resp *CreateSessionResponse
	require.NoError(t, json.Unmarshal(httpResp.Bytes(), &resp))

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	clt := r.newClient(t, roundtrip.BearerAuth(resp.Token), roundtrip.CookieJar(jar))
	jar.SetCookies(&r.webURL, httpResp.Cookies())

	session, err := resp.response()
	require.NoError(t, err)
	if session.TokenExpiresIn < 0 {
		t.Errorf("Expected expiry time to be in the future but got %v", session.TokenExpiresIn)
	}
	return &authPack{
		session: session,
		clt:     clt,
		cookies: httpResp.Cookies(),
	}
}

func defaultRoleForNewUser(teleUser types.User, login string) types.Role {
	role := services.RoleForUser(teleUser)
	role.SetLogins(types.Allow, []string{login})
	role.SetWindowsDesktopLabels(types.Allow, types.Labels{types.Wildcard: {types.Wildcard}})
	options := role.GetOptions()
	options.ForwardAgent = types.NewBool(true)
	role.SetOptions(options)
	return role
}

func (r *testProxy) createUser(ctx context.Context, t *testing.T, user, login, pass, otpSecret string, roles []types.Role) {
	teleUser, err := types.NewUser(user)
	require.NoError(t, err)

	if len(roles) == 0 {
		roles = []types.Role{defaultRoleForNewUser(teleUser, login)}
	}

	for _, role := range roles {
		err = r.auth.Auth().UpsertRole(ctx, role)
		require.NoError(t, err)

		teleUser.AddRole(role.GetName())
	}

	teleUser.SetCreatedBy(types.CreatedBy{
		User: types.UserRef{Name: "some-auth-user"},
	})

	err = r.auth.Auth().CreateUser(ctx, teleUser)
	require.NoError(t, err)

	err = r.auth.Auth().UpsertPassword(user, []byte(pass))
	require.NoError(t, err)

	if otpSecret != "" {
		dev, err := services.NewTOTPDevice("otp", otpSecret, r.clock.Now())
		require.NoError(t, err)
		err = r.auth.Auth().UpsertMFADevice(ctx, user, dev)
		require.NoError(t, err)
	}
}

func (r *testProxy) newClient(t *testing.T, opts ...roundtrip.ClientParam) *client.WebClient {
	opts = append(opts, roundtrip.HTTPClient(client.NewInsecureWebClient()))
	clt, err := client.NewWebClient(r.webURL.String(), opts...)
	require.NoError(t, err)
	return clt
}

func (r *testProxy) makeTerminal(t *testing.T, pack *authPack, sessionID session.ID) (*websocket.Conn, session.Session) {
	u := url.URL{
		Host:   r.webURL.Host,
		Scheme: client.WSS,
		Path:   fmt.Sprintf("/v1/webapi/sites/%v/connect", currentSiteShortcut),
	}

	requestData := TerminalRequest{
		Server: r.node.ID(),
		Login:  pack.login,
		Term: session.TerminalParams{
			W: 100,
			H: 100,
		},
	}

	if sessionID != "" {
		requestData.SessionID = sessionID
	}

	data, err := json.Marshal(requestData)
	require.NoError(t, err)

	q := u.Query()
	q.Set("params", string(data))
	q.Set(roundtrip.AccessTokenQueryParam, pack.session.Token)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{}
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	header := http.Header{}
	header.Add("Origin", "http://localhost")
	for _, cookie := range pack.cookies {
		header.Add("Cookie", cookie.String())
	}

	ws, resp, err := dialer.Dial(u.String(), header)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ws.Close())
		require.NoError(t, resp.Body.Close())
	})

	ty, raw, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.BinaryMessage, ty)
	var env Envelope
	require.NoError(t, proto.Unmarshal(raw, &env))

	var sessResp siteSessionGenerateResponse
	require.NoError(t, json.Unmarshal([]byte(env.Payload), &sessResp))

	return ws, sessResp.Session
}

func (r *testProxy) makeDesktopSession(t *testing.T, pack *authPack, sessionID session.ID, addr net.Addr) *websocket.Conn {
	u := url.URL{
		Host:   r.webURL.Host,
		Scheme: client.WSS,
		Path:   fmt.Sprintf("/webapi/sites/%s/desktops/%s/connect", currentSiteShortcut, "desktop1"),
	}

	q := u.Query()
	q.Set("username", "marek")
	q.Set("width", "100")
	q.Set("height", "100")
	q.Set(roundtrip.AccessTokenQueryParam, pack.session.Token)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{}
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	header := http.Header{}
	for _, cookie := range pack.cookies {
		header.Add("Cookie", cookie.String())
	}

	ws, resp, err := dialer.Dial(u.String(), header)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, ws.Close())
		require.NoError(t, resp.Body.Close())
	})
	return ws
}

func login(t *testing.T, clt *client.WebClient, cookieToken, reqToken string, reqData interface{}) *roundtrip.Response {
	resp, err := httplib.ConvertResponse(clt.RoundTrip(func() (*http.Response, error) {
		data, err := json.Marshal(reqData)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest("POST", clt.Endpoint("webapi", "sessions"), bytes.NewBuffer(data))
		if err != nil {
			return nil, err
		}
		addCSRFCookieToReq(req, cookieToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrf.HeaderName, reqToken)
		return clt.HTTPClient().Do(req)
	}))
	require.NoError(t, err)
	return resp
}

func validateTerminalStream(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	termHandler := newTerminalHandler()
	stream := termHandler.asTerminalStream(conn)

	// here we intentionally run a command where the output we're looking
	// for is not present in the command itself
	_, err := io.WriteString(stream, "echo txlxport | sed 's/x/e/g'\r\n")
	require.NoError(t, err)
	require.NoError(t, waitForOutput(stream, "teleport"))
}

type mockProxySettings struct{}

func (mock *mockProxySettings) GetProxySettings(ctx context.Context) (*webclient.ProxySettings, error) {
	return &webclient.ProxySettings{}, nil
}

// TestUserContextWithAccessRequest checks that the userContext includes the ID of the
// access request after it has been consumed and the web session has been renewed.
func TestUserContextWithAccessRequest(t *testing.T) {
	t.Parallel()
	env := newWebPack(t, 1)
	proxy := env.proxies[0]
	ctx := context.Background()

	// Set user and role names.
	username := "user"
	baseRoleName := "role"
	requestableRolename := "requestable-role"

	// Create user's base role with the ability to request the requestable role.
	baseRole, err := types.NewRole(baseRoleName, types.RoleSpecV5{
		Allow: types.RoleConditions{
			Request: &types.AccessRequestConditions{
				Roles: []string{requestableRolename},
			},
		},
	})
	require.NoError(t, err)

	// Create user with the base role.
	pack := proxy.authPack(t, username, []types.Role{baseRole})

	// Create the requestable role.
	requestableRole, err := types.NewRole(requestableRolename, types.RoleSpecV5{})
	require.NoError(t, err)
	err = env.server.Auth().UpsertRole(ctx, requestableRole)
	require.NoError(t, err)

	// Create and approve an access request for the requestable role.
	accessReq, err := services.NewAccessRequest(username, requestableRolename)
	require.NoError(t, err)
	accessReq.SetState(types.RequestState_APPROVED)
	err = env.server.Auth().CreateAccessRequest(ctx, accessReq)
	require.NoError(t, err)

	// Get the ID of the created and approved access request.
	accessRequestID := accessReq.GetMetadata().Name

	// Make a request to renew the session with the ID of the access request.
	_, err = pack.clt.PostJSON(ctx, pack.clt.Endpoint("webapi", "sessions", "renew"), renewSessionRequest{
		AccessRequestID: accessRequestID,
	})
	require.NoError(t, err)

	// Make a request to fetch the userContext.
	endpoint := pack.clt.Endpoint("webapi", "sites", env.server.ClusterName(), "context")
	response, err := pack.clt.Get(context.Background(), endpoint, url.Values{})
	require.NoError(t, err)

	// Process the JSON response of the request.
	var userContext ui.UserContext
	err = json.Unmarshal(response.Bytes(), &userContext)
	require.NoError(t, err)

	// Verify that the userContext returned contains the correct Access Request ID.
	require.Equal(t, accessRequestID, userContext.ConsumedAccessRequestID)
}

// kubeClusterConfig defines the cluster to be created
type kubeClusterConfig struct {
	name        string
	apiEndpoint string
}

func newKubeConfigFile(ctx context.Context, t *testing.T, clusters ...kubeClusterConfig) string {
	tmpDir := t.TempDir()

	kubeConf := clientcmdapi.NewConfig()
	for _, cluster := range clusters {
		kubeConf.Clusters[cluster.name] = &clientcmdapi.Cluster{
			Server:                cluster.apiEndpoint,
			InsecureSkipTLSVerify: true,
		}
		kubeConf.AuthInfos[cluster.name] = &clientcmdapi.AuthInfo{}

		kubeConf.Contexts[cluster.name] = &clientcmdapi.Context{
			Cluster:  cluster.name,
			AuthInfo: cluster.name,
		}
	}
	kubeConfigLocation := filepath.Join(tmpDir, "kubeconfig")
	err := clientcmd.WriteToFile(*kubeConf, kubeConfigLocation)
	require.NoError(t, err)
	return kubeConfigLocation
}

type startKubeOptions struct {
	clusters    []kubeClusterConfig
	authServer  *auth.TestTLSServer
	revTunnel   reversetunnel.Server
	serviceType kubeproxy.KubeServiceType
}

func startKube(ctx context.Context, t *testing.T, cfg startKubeOptions) net.Addr {
	server, cleanup, addr := startKubeWithoutCleanup(ctx, t, cfg)
	t.Cleanup(func() {
		err := server.Close()
		require.NoError(t, err)
		require.NoError(t, cleanup())
	})
	return addr
}

type cleanupFunc func() error

func startKubeWithoutCleanup(ctx context.Context, t *testing.T, cfg startKubeOptions) (*kubeproxy.TLSServer, cleanupFunc, net.Addr) {
	role := types.RoleProxy
	if cfg.serviceType == kubeproxy.KubeService {
		role = types.RoleKube
	}
	var kubeConfigLocation string
	if len(cfg.clusters) > 0 {
		kubeConfigLocation = newKubeConfigFile(ctx, t, cfg.clusters...)
	}

	keyGen := native.New(ctx)
	hostID := uuid.New().String()
	// heartbeatsWaitChannel waits for clusters heartbeats to start.
	heartbeatsWaitChannel := make(chan struct{}, len(cfg.clusters))
	client, err := cfg.authServer.NewClient(auth.TestServerID(role, hostID))
	require.NoError(t, err)

	// Auth client, lock watcher and authorizer for Kube proxy.
	proxyAuthClient, err := cfg.authServer.NewClient(auth.TestBuiltin(types.RoleProxy))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxyAuthClient.Close()) })

	proxyLockWatcher, err := services.NewLockWatcher(ctx, services.LockWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: teleport.ComponentProxy,
			Client:    proxyAuthClient,
		},
	})
	require.NoError(t, err)
	proxyAuthorizer, err := auth.NewAuthorizer(
		cfg.authServer.ClusterName(),
		proxyAuthClient,
		proxyLockWatcher,
	)
	require.NoError(t, err)

	// TLS config for kube proxy and Kube service.
	authID := auth.IdentityID{
		Role:     role,
		HostUUID: hostID,
		NodeName: "kube_server",
	}
	dns := []string{"localhost", "127.0.0.1", "kube." + constants.APIDomain, "*" + constants.APIDomain}
	identity, err := auth.LocalRegister(authID, cfg.authServer.Auth(), nil, dns, "", nil)
	require.NoError(t, err)

	tlsConfig, err := identity.TLSConfig(nil)
	require.NoError(t, err)

	component := teleport.Component(teleport.ComponentProxy, teleport.ComponentProxyKube)
	if cfg.serviceType == kubeproxy.KubeService {
		component = teleport.ComponentKube
	}

	kubeServer, err := kubeproxy.NewTLSServer(kubeproxy.TLSServerConfig{
		ForwarderConfig: kubeproxy.ForwarderConfig{
			Namespace:         apidefaults.Namespace,
			Keygen:            keyGen,
			ClusterName:       cfg.authServer.ClusterName(),
			Authz:             proxyAuthorizer,
			AuthClient:        client,
			StreamEmitter:     client,
			DataDir:           t.TempDir(),
			CachingAuthClient: client,
			HostID:            hostID,
			Context:           ctx,
			KubeconfigPath:    kubeConfigLocation,
			KubeServiceType:   cfg.serviceType,
			Component:         component,
			LockWatcher:       proxyLockWatcher,
			ReverseTunnelSrv:  cfg.revTunnel,
			// skip Impersonation validation
			CheckImpersonationPermissions: func(ctx context.Context, clusterName string, sarClient authztypes.SelfSubjectAccessReviewInterface) error {
				return nil
			},
			Clock: clockwork.NewRealClock(),
		},
		TLS:           tlsConfig,
		AccessPoint:   client,
		DynamicLabels: nil,
		LimiterConfig: limiter.Config{
			MaxConnections:   1000,
			MaxNumberOfUsers: 1000,
		},
		// each time heartbeat is called we insert data into the channel.
		// this is used to make sure that heartbeat started and the clusters
		// are registered in the auth server
		OnHeartbeat: func(err error) {
			select {
			case heartbeatsWaitChannel <- struct{}{}:
			default:
			}
		},
		GetRotation:      func(role types.SystemRole) (*types.Rotation, error) { return &types.Rotation{}, nil },
		ResourceMatchers: nil,
		OnReconcile:      func(kc types.KubeClusters) {},
	})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	errChan := make(chan error, 1)
	go func() {
		defer close(errChan)
		err := kubeServer.Serve(listener)
		// ignore server closed error returned when .Close is called.
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		errChan <- err
	}()

	// Waits for len(clusters) heartbeats to start
	heartbeatsToExpect := len(cfg.clusters)
	for i := 0; i < heartbeatsToExpect; i++ {
		<-heartbeatsWaitChannel
	}

	return kubeServer, func() error {
		return <-errChan
	}, listener.Addr()
}

func marshalRBACError(t *testing.T, w http.ResponseWriter) {
	status := &metav1.Status{
		Message: "pods is forbidden: User \"USER\" cannot list resource \"pods\" in API group \"\" in the namespace \"default\"",
		Code:    http.StatusForbidden,
		Reason:  metav1.StatusReasonForbidden,
		Status:  metav1.StatusFailure,
	}

	data, err := runtime.Encode(statusCodecs.LegacyCodec(), status)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, err = w.Write(data)
	require.NoError(t, err)
}

func marshalValidPodList(t *testing.T, w http.ResponseWriter) {
	result := &corev1.PodList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "",
			APIVersion: "",
		},
		ListMeta: metav1.ListMeta{
			SelfLink:           "",
			ResourceVersion:    "1231415",
			Continue:           "",
			RemainingItemCount: nil,
		},
		Items: []corev1.Pod{},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	err := json.NewEncoder(w).Encode(result)
	require.NoError(t, err)
}

// statusScheme is private scheme for the decoding here until someone fixes the TODO in NewConnection
var statusScheme = runtime.NewScheme()

// ParameterCodec knows about query parameters used with the meta v1 API spec.
var statusCodecs = serializer.NewCodecFactory(statusScheme)

func init() {
	statusScheme.AddUnversionedTypes(metav1.SchemeGroupVersion,
		&metav1.Status{},
	)
}

// TestForwardingTraces checks that the userContext includes the ID of the
// access request after it has been consumed and the web session has been renewed.
func TestForwardingTraces(t *testing.T) {
	t.Parallel()

	env := newWebPack(t, 1)
	p := env.proxies[0]

	newRequest := func(t *testing.T) *http.Request {
		req, err := http.NewRequest(http.MethodGet, "", nil)
		require.NoError(t, err)

		return req
	}

	// Span captured from the UI which was marshaled by opentelemetry-js.
	const rawSpan = `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"web-ui"}},{"key":"telemetry.sdk.language","value":{"stringValue":"webjs"}},{"key":"telemetry.sdk.name","value":{"stringValue":"opentelemetry"}},{"key":"telemetry.sdk.version","value":{"stringValue":"1.7.0"}},{"key":"service.version","value":{"stringValue":"0.1.0"}}],"droppedAttributesCount":0},"scopeSpans":[{"scope":{"name":"@opentelemetry/instrumentation-fetch","version":"0.33.0"},"spans":[{"traceId":"255c8d876e7dbf3707ee8451ad518652","spanId":"d9edec516e598d8c","name":"HTTP GET","kind":3,"startTimeUnixNano":1668606426497000000,"endTimeUnixNano":1668502943215499800,"attributes":[{"key":"component","value":{"stringValue":"fetch"}},{"key":"http.method","value":{"stringValue":"GET"}},{"key":"http.url","value":{"stringValue":"https://proxy.example.com/v1/webapi/user/status"}},{"key":"http.status_code","value":{"intValue":0}},{"key":"http.status_text","value":{"stringValue":"Failed to fetch"}},{"key":"http.host","value":{"stringValue":"proxy.example.com"}},{"key":"http.scheme","value":{"stringValue":"https"}},{"key":"http.user_agent","value":{"stringValue":"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/107.0.0.0 Safari/537.36    "}},{"key":"http.response_content_length","value":{"intValue":0}}],"droppedAttributesCount":0,"events":[{"attributes":[],"name":"fetchStart","timeUnixNano":1668502943210900000,"droppedAttributesCount":0},{"attributes":[],"name":"domainLookupStart","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"domainLookupEnd","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"connectStart","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"secureConnectionStart","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"connectEnd","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"requestStart","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"responseStart","timeUnixNano":1668502687491499800,"droppedAttributesCount":0},{"attributes":[],"name":"responseEnd","timeUnixNano":1668502943215100000,"droppedAttributesCount":0}],"droppedEventsCount":0,"status":{"code":0},"links":[],"droppedLinksCount":0}]}]}]}`

	// dummy span with arbitrary data, needed to be able to protojson.Marshal in tests
	span := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						{
							Key: "test",
							Value: &commonv1.AnyValue{
								Value: &commonv1.AnyValue_IntValue{
									IntValue: 0,
								},
							},
						},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           []byte{1, 2, 3, 4},
								SpanId:            []byte{5, 6, 7, 8},
								TraceState:        "",
								ParentSpanId:      []byte{9, 10, 11, 12},
								Name:              "test",
								Kind:              tracepb.Span_SPAN_KIND_CLIENT,
								StartTimeUnixNano: uint64(time.Now().Add(-1 * time.Minute).Unix()),
								EndTimeUnixNano:   uint64(time.Now().Unix()),
								Attributes: []*commonv1.KeyValue{
									{
										Key: "test",
										Value: &commonv1.AnyValue{
											Value: &commonv1.AnyValue_IntValue{
												IntValue: 11,
											},
										},
									},
								},
								Status: &tracepb.Status{
									Message: "success!",
									Code:    tracepb.Status_STATUS_CODE_OK,
								},
							},
						},
					},
				},
			},
		},
	}

	cases := []struct {
		name      string
		req       func(t *testing.T) *http.Request
		assertion func(t *testing.T, spans []*otlp.ResourceSpans, err error, code int)
	}{
		{
			name: "no data",
			req: func(t *testing.T) *http.Request {
				r := newRequest(t)
				r.Body = io.NopCloser(&bytes.Buffer{})
				return r
			},
			assertion: func(t *testing.T, spans []*tracepb.ResourceSpans, err error, code int) {
				require.NoError(t, err)
				require.Equal(t, http.StatusBadRequest, code)
				require.Empty(t, spans)
			},
		},
		{
			name: "invalid data",
			req: func(t *testing.T) *http.Request {
				r := newRequest(t)
				r.Body = io.NopCloser(strings.NewReader(`{"test": "abc"}`))
				return r
			},
			assertion: func(t *testing.T, spans []*tracepb.ResourceSpans, err error, code int) {
				require.NoError(t, err)
				require.Equal(t, http.StatusBadRequest, code)
				require.Empty(t, spans)
			},
		},
		{
			name: "no traces",
			req: func(t *testing.T) *http.Request {
				r := newRequest(t)

				raw, err := protojson.Marshal(&tracepb.ResourceSpans{})
				require.NoError(t, err)
				r.Body = io.NopCloser(bytes.NewBuffer(raw))

				return r
			},
			assertion: func(t *testing.T, spans []*tracepb.ResourceSpans, err error, code int) {
				require.NoError(t, err)
				require.Equal(t, http.StatusBadRequest, code)
				require.Empty(t, spans)
			},
		},
		{
			name: "traces with base64 encoded ids",
			req: func(t *testing.T) *http.Request {
				r := newRequest(t)

				// Since the id fields of the span are all []byte,
				// protojson will marshal them into base64
				raw, err := protojson.Marshal(span)
				require.NoError(t, err)
				r.Body = io.NopCloser(bytes.NewBuffer(raw))

				return r
			},
			assertion: func(t *testing.T, spans []*tracepb.ResourceSpans, err error, code int) {
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, code)
				require.Len(t, spans, 1)
				require.Empty(t, cmp.Diff(span.ResourceSpans[0], spans[0], protocmp.Transform()))
			},
		},
		{
			name: "traces with hex encoded ids",
			req: func(t *testing.T) *http.Request {
				r := newRequest(t)

				// The id fields are hex encoded instead of base64 encoded
				// by opentelemetry-js for the rawSpan
				r.Body = io.NopCloser(strings.NewReader(rawSpan))

				return r
			},
			assertion: func(t *testing.T, spans []*otlp.ResourceSpans, err error, code int) {
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, code)
				require.Len(t, spans, 1)

				var data tracepb.TracesData
				require.NoError(t, protojson.Unmarshal([]byte(rawSpan), &data))

				// compare the spans, but ignore the ids since we know that the rawSpan
				// has hex encoded ids and protojson.Unmarshal will give us an invalid value
				require.Empty(t, cmp.Diff(data.ResourceSpans[0], spans[0], protocmp.Transform(), protocmp.IgnoreFields(&otlp.Span{}, "span_id", "trace_id")))

				// compare the ids separately
				sid1 := spans[0].ScopeSpans[0].Spans[0].SpanId
				tid1 := spans[0].ScopeSpans[0].Spans[0].TraceId

				sid2 := data.ResourceSpans[0].ScopeSpans[0].Spans[0].SpanId
				tid2 := data.ResourceSpans[0].ScopeSpans[0].Spans[0].TraceId

				require.Equal(t, hex.EncodeToString(sid1), base64.StdEncoding.EncodeToString(sid2))
				require.Equal(t, hex.EncodeToString(tid1), base64.StdEncoding.EncodeToString(tid2))
			},
		},
	}

	// NOTE: resetting the tracing client prevents
	// the test cases from running in parallel
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clt := &mockTraceClient{
				uploadReceived: make(chan struct{}),
			}
			p.handler.handler.cfg.TraceClient = clt

			recorder := httptest.NewRecorder()

			// use the handler directly because there is no easy way to pipe in our tracing
			// data using the pack client in a format that would match the ui.
			_, err := p.handler.handler.traces(recorder, tt.req(t), nil, nil)

			// if traces weren't uploaded perform the assertion
			// without waiting for traces to be forwarded
			if err != nil || recorder.Code != http.StatusOK {
				tt.assertion(t, clt.spans, err, recorder.Code)
				return
			}

			// traces are forwarded in a goroutine, wait for them
			// to be received by the trace client before doing the
			// assertion
			select {
			case <-clt.uploadReceived:
			case <-time.After(10 * time.Second):
				t.Fatal("Timed out waiting for traces to be uploaded")
			}

			tt.assertion(t, clt.spans, err, recorder.Code)
		})
	}
}

type mockTraceClient struct {
	uploadError    error
	uploadReceived chan struct{}
	spans          []*otlp.ResourceSpans
}

func (m *mockTraceClient) Start(ctx context.Context) error {
	return nil
}

func (m *mockTraceClient) Stop(ctx context.Context) error {
	return nil
}

func (m *mockTraceClient) UploadTraces(ctx context.Context, protoSpans []*otlp.ResourceSpans) error {
	m.spans = append(m.spans, protoSpans...)
	m.uploadReceived <- struct{}{}
	return m.uploadError
}
