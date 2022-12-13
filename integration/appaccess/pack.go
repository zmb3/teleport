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

package appaccess

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/gravitational/oxy/forward"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/integration/helpers"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/native"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/httplib/csrf"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv/alpnproxy"
	alpncommon "github.com/zmb3/teleport/lib/srv/alpnproxy/common"
	"github.com/zmb3/teleport/lib/srv/app/common"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/web"
	"github.com/zmb3/teleport/lib/web/app"
)

// Pack contains identity as well as initialized Teleport clusters and instances.
type Pack struct {
	username string
	password string

	tc *client.TeleportClient

	user types.User

	webCookie string
	webToken  string

	rootCluster    *helpers.TeleInstance
	rootAppServers []*service.TeleportProcess
	rootCertPool   *x509.CertPool

	rootAppName        string
	rootAppPublicAddr  string
	rootAppClusterName string
	rootMessage        string
	rootAppURI         string

	rootWSAppName    string
	rootWSPublicAddr string
	rootWSMessage    string
	rootWSAppURI     string

	rootWSSAppName    string
	rootWSSPublicAddr string
	rootWSSMessage    string
	rootWSSAppURI     string

	rootTCPAppName    string
	rootTCPPublicAddr string
	rootTCPMessage    string
	rootTCPAppURI     string

	rootTCPTwoWayAppName    string
	rootTCPTwoWayPublicAddr string
	rootTCPTwoWayMessage    string
	rootTCPTwoWayAppURI     string

	jwtAppName        string
	jwtAppPublicAddr  string
	jwtAppClusterName string
	jwtAppURI         string

	dumperAppURI string

	leafCluster    *helpers.TeleInstance
	leafAppServers []*service.TeleportProcess

	leafAppName        string
	leafAppPublicAddr  string
	leafAppClusterName string
	leafMessage        string
	leafAppURI         string

	leafWSAppName    string
	leafWSPublicAddr string
	leafWSMessage    string
	leafWSAppURI     string

	leafWSSAppName    string
	leafWSSPublicAddr string
	leafWSSMessage    string
	leafWSSAppURI     string

	leafTCPAppName    string
	leafTCPPublicAddr string
	leafTCPMessage    string
	leafTCPAppURI     string

	headerAppName        string
	headerAppPublicAddr  string
	headerAppClusterName string
	headerAppURI         string

	wsHeaderAppName        string
	wsHeaderAppPublicAddr  string
	wsHeaderAppClusterName string
	wsHeaderAppURI         string

	flushAppName        string
	flushAppPublicAddr  string
	flushAppClusterName string
	flushAppURI         string
}

func (p *Pack) RootAppClusterName() string {
	return p.rootAppClusterName
}

func (p *Pack) RootAppPublicAddr() string {
	return p.rootAppPublicAddr
}

func (p *Pack) LeafAppClusterName() string {
	return p.leafAppClusterName
}

func (p *Pack) LeafAppPublicAddr() string {
	return p.leafAppPublicAddr
}

// initUser will create a user within the root cluster.
func (p *Pack) initUser(t *testing.T) {
	p.username = uuid.New().String()
	p.password = uuid.New().String()

	user, err := types.NewUser(p.username)
	require.NoError(t, err)

	role := services.RoleForUser(user)
	role.SetLogins(types.Allow, []string{p.username, "root", "ubuntu"})
	err = p.rootCluster.Process.GetAuthServer().UpsertRole(context.Background(), role)
	require.NoError(t, err)

	user.AddRole(role.GetName())
	user.SetTraits(map[string][]string{"env": {"production"}, "empty": {}, "nil": nil})
	err = p.rootCluster.Process.GetAuthServer().CreateUser(context.Background(), user)
	require.NoError(t, err)

	err = p.rootCluster.Process.GetAuthServer().UpsertPassword(user.GetName(), []byte(p.password))
	require.NoError(t, err)

	p.user = user
}

// initWebSession creates a Web UI session within the root cluster.
func (p *Pack) initWebSession(t *testing.T) {
	csReq, err := json.Marshal(web.CreateSessionReq{
		User: p.username,
		Pass: p.password,
	})
	require.NoError(t, err)

	// Create POST request to create session.
	u := url.URL{
		Scheme: "https",
		Host:   p.rootCluster.Web,
		Path:   "/v1/webapi/sessions/web",
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewBuffer(csReq))
	require.NoError(t, err)

	// Attach CSRF token in cookie and header.
	csrfToken, err := utils.CryptoRandomHex(32)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{
		Name:  csrf.CookieName,
		Value: csrfToken,
	})
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set(csrf.HeaderName, csrfToken)

	// Issue request.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Read in response.
	var csResp *web.CreateSessionResponse
	err = json.NewDecoder(resp.Body).Decode(&csResp)
	require.NoError(t, err)

	// Extract session cookie and bearer token.
	require.Len(t, resp.Cookies(), 1)
	cookie := resp.Cookies()[0]
	require.Equal(t, cookie.Name, web.CookieName)

	p.webCookie = cookie.Value
	p.webToken = csResp.Token
}

// initTeleportClient initializes a Teleport client with this pack's user
// credentials.
func (p *Pack) initTeleportClient(t *testing.T, opts AppTestOptions) {
	creds, err := helpers.GenerateUserCreds(helpers.UserCredsRequest{
		Process:  p.rootCluster.Process,
		Username: p.user.GetName(),
	})
	require.NoError(t, err)

	tc, err := p.rootCluster.NewClientWithCreds(helpers.ClientConfig{
		Login:   p.user.GetName(),
		Cluster: p.rootCluster.Secrets.SiteName,
		Host:    helpers.Loopback,
		Port:    helpers.Port(t, p.rootCluster.SSH),
	}, *creds)
	require.NoError(t, err)

	p.tc = tc
}

// CreateAppSession creates an application session with the root cluster. The
// application that the user connects to may be running in a leaf cluster.
func (p *Pack) CreateAppSession(t *testing.T, publicAddr, clusterName string) string {
	require.NotEmpty(t, p.webCookie)
	require.NotEmpty(t, p.webToken)

	casReq, err := json.Marshal(web.CreateAppSessionRequest{
		FQDNHint:    publicAddr,
		PublicAddr:  publicAddr,
		ClusterName: clusterName,
	})
	require.NoError(t, err)
	statusCode, body, err := p.makeWebapiRequest(http.MethodPost, "sessions/app", casReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, statusCode)

	var casResp *web.CreateAppSessionResponse
	err = json.Unmarshal(body, &casResp)
	require.NoError(t, err)

	return casResp.CookieValue
}

// LockUser will lock the configured user for this pack.
func (p *Pack) LockUser(t *testing.T) {
	err := p.rootCluster.Process.GetAuthServer().UpsertLock(context.Background(), &types.LockV2{
		Spec: types.LockSpecV2{
			Target: types.LockTarget{
				User: p.username,
			},
		},
		Metadata: types.Metadata{
			Name: "test-lock",
		},
	})
	require.NoError(t, err)
}

// makeWebapiRequest makes a request to the root cluster Web API.
func (p *Pack) makeWebapiRequest(method, endpoint string, payload []byte) (int, []byte, error) {
	u := url.URL{
		Scheme: "https",
		Host:   p.rootCluster.Web,
		Path:   fmt.Sprintf("/v1/webapi/%s", endpoint),
	}

	req, err := http.NewRequest(method, u.String(), bytes.NewBuffer(payload))
	if err != nil {
		return 0, nil, trace.Wrap(err)
	}

	req.AddCookie(&http.Cookie{
		Name:  web.CookieName,
		Value: p.webCookie,
	})
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", p.webToken))
	req.Header.Add("Content-Type", "application/json")

	statusCode, body, err := p.sendRequest(req, nil)
	return statusCode, []byte(body), trace.Wrap(err)
}

func (p *Pack) ensureAuditEvent(t *testing.T, eventType string, checkEvent func(event apievents.AuditEvent)) {
	require.Eventuallyf(t, func() bool {
		events, _, err := p.rootCluster.Process.GetAuthServer().SearchEvents(
			time.Now().Add(-time.Hour),
			time.Now().Add(time.Hour),
			apidefaults.Namespace,
			[]string{eventType},
			1,
			types.EventOrderDescending,
			"",
		)
		require.NoError(t, err)
		if len(events) == 0 {
			return false
		}

		checkEvent(events[0])
		return true
	}, 500*time.Millisecond, 50*time.Millisecond, "failed to fetch audit event \"%s\"", eventType)
}

// initCertPool initializes root cluster CA pool.
func (p *Pack) initCertPool(t *testing.T) {
	authClient := p.rootCluster.GetSiteAPI(p.rootCluster.Secrets.SiteName)
	ca, err := authClient.GetCertAuthority(context.Background(), types.CertAuthID{
		Type:       types.HostCA,
		DomainName: p.rootCluster.Secrets.SiteName,
	}, false)
	require.NoError(t, err)

	pool, err := services.CertPool(ca)
	require.NoError(t, err)

	p.rootCertPool = pool
}

// startLocalProxy starts a local ALPN proxy for the specified application.
func (p *Pack) startLocalProxy(t *testing.T, publicAddr, clusterName string) string {
	tlsConfig := p.makeTLSConfig(t, publicAddr, clusterName)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	proxy, err := alpnproxy.NewLocalProxy(alpnproxy.LocalProxyConfig{
		RemoteProxyAddr:    p.rootCluster.Web,
		Protocols:          []alpncommon.Protocol{alpncommon.ProtocolTCP},
		InsecureSkipVerify: true,
		Listener:           listener,
		ParentContext:      context.Background(),
		Certs:              tlsConfig.Certificates,
	})
	require.NoError(t, err)
	t.Cleanup(func() { proxy.Close() })

	go proxy.Start(context.Background())

	return proxy.GetAddr()
}

// makeTLSConfig returns TLS config suitable for making an app access request.
func (p *Pack) makeTLSConfig(t *testing.T, publicAddr, clusterName string) *tls.Config {
	privateKey, publicKey, err := native.GenerateKeyPair()
	require.NoError(t, err)

	ws, err := p.tc.CreateAppSession(context.Background(), types.CreateAppSessionRequest{
		Username:    p.user.GetName(),
		PublicAddr:  publicAddr,
		ClusterName: clusterName,
	})
	require.NoError(t, err)

	// Make sure the session ID can be seen in the backend before we continue onward.
	require.Eventually(t, func() bool {
		_, err := p.rootCluster.Process.GetAuthServer().GetAppSession(context.Background(), types.GetAppSessionRequest{
			SessionID: ws.GetMetadata().Name,
		})
		return err == nil
	}, 5*time.Second, 100*time.Millisecond)

	certificate, err := p.rootCluster.Process.GetAuthServer().GenerateUserAppTestCert(
		auth.AppTestCertRequest{
			PublicKey:   publicKey,
			Username:    p.user.GetName(),
			TTL:         time.Hour,
			PublicAddr:  publicAddr,
			ClusterName: clusterName,
			SessionID:   ws.GetName(),
		})
	require.NoError(t, err)

	tlsCert, err := tls.X509KeyPair(certificate, privateKey)
	require.NoError(t, err)

	return &tls.Config{
		RootCAs:            p.rootCertPool,
		Certificates:       []tls.Certificate{tlsCert},
		InsecureSkipVerify: true,
	}
}

// makeTLSConfigNoSession returns TLS config for application access without
// creating session to simulate nonexistent session scenario.
func (p *Pack) makeTLSConfigNoSession(t *testing.T, publicAddr, clusterName string) *tls.Config {
	privateKey, publicKey, err := native.GenerateKeyPair()
	require.NoError(t, err)

	certificate, err := p.rootCluster.Process.GetAuthServer().GenerateUserAppTestCert(
		auth.AppTestCertRequest{
			PublicKey:   publicKey,
			Username:    p.user.GetName(),
			TTL:         time.Hour,
			PublicAddr:  publicAddr,
			ClusterName: clusterName,
			// Use arbitrary session ID
			SessionID: uuid.New().String(),
		})
	require.NoError(t, err)

	tlsCert, err := tls.X509KeyPair(certificate, privateKey)
	require.NoError(t, err)

	return &tls.Config{
		RootCAs:            p.rootCertPool,
		Certificates:       []tls.Certificate{tlsCert},
		InsecureSkipVerify: true,
	}
}

// MakeRequest makes a request to the root cluster with the given session cookie.
func (p *Pack) MakeRequest(sessionCookie string, method string, endpoint string, headers ...service.Header) (int, string, error) {
	req, err := http.NewRequest(method, p.assembleRootProxyURL(endpoint), nil)
	if err != nil {
		return 0, "", trace.Wrap(err)
	}

	// Only attach session cookie if passed in.
	if sessionCookie != "" {
		req.AddCookie(&http.Cookie{
			Name:  app.CookieName,
			Value: sessionCookie,
		})
	}

	for _, h := range headers {
		req.Header.Add(h.Name, h.Value)
	}

	return p.sendRequest(req, nil)
}

// makeRequestWithClientCert makes a request to the root cluster using the
// client certificate authentication from the provided tls config.
func (p *Pack) makeRequestWithClientCert(tlsConfig *tls.Config, method, endpoint string) (int, string, error) {
	req, err := http.NewRequest(method, p.assembleRootProxyURL(endpoint), nil)
	if err != nil {
		return 0, "", trace.Wrap(err)
	}
	return p.sendRequest(req, tlsConfig)
}

// makeWebsocketRequest makes a websocket request with the given session cookie.
func (p *Pack) makeWebsocketRequest(sessionCookie, endpoint string) (string, error) {
	header := http.Header{}
	dialer := websocket.Dialer{}

	if sessionCookie != "" {
		header.Set("Cookie", (&http.Cookie{
			Name:  app.CookieName,
			Value: sessionCookie,
		}).String())
	}
	dialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	conn, resp, err := dialer.Dial(fmt.Sprintf("wss://%s%s", p.rootCluster.Web, endpoint), header)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	defer resp.Body.Close()
	stream := &web.WebsocketIO{Conn: conn}
	data, err := io.ReadAll(stream)
	if err != nil && websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) {
		return "", err
	}
	return string(data), nil
}

// assembleRootProxyURL returns the URL string of an endpoint at the root
// cluster's proxy web.
func (p *Pack) assembleRootProxyURL(endpoint string) string {
	u := url.URL{
		Scheme: "https",
		Host:   p.rootCluster.Web,
		Path:   endpoint,
	}
	return u.String()
}

// sendReqeust sends the request to the root cluster.
func (p *Pack) sendRequest(req *http.Request, tlsConfig *tls.Config) (int, string, error) {
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", trace.Wrap(err)
	}
	defer resp.Body.Close()

	// Read in response body.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", trace.Wrap(err)
	}

	return resp.StatusCode, string(body), nil
}

// waitForLogout keeps making request with the passed in session cookie until
// they return a non-200 status.
func (p *Pack) waitForLogout(appCookie string) (int, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-ticker.C:
			status, _, err := p.MakeRequest(appCookie, http.MethodGet, "/")
			if err != nil {
				return 0, trace.Wrap(err)
			}
			if status != http.StatusOK {
				return status, nil
			}
		case <-timeout.C:
			return 0, trace.BadParameter("timed out waiting for logout")
		}
	}
}

func (p *Pack) startRootAppServers(t *testing.T, count int, opts AppTestOptions) []*service.TeleportProcess {
	log := utils.NewLoggerForTests()

	configs := make([]*service.Config, count)

	for i := 0; i < count; i++ {
		raConf := service.MakeDefaultConfig()
		raConf.Clock = opts.Clock
		raConf.Console = nil
		raConf.Log = log
		raConf.DataDir = t.TempDir()
		raConf.SetToken("static-token-value")
		raConf.SetAuthServerAddress(utils.NetAddr{
			AddrNetwork: "tcp",
			Addr:        p.rootCluster.Web,
		})
		raConf.Auth.Enabled = false
		raConf.Proxy.Enabled = false
		raConf.SSH.Enabled = false
		raConf.Apps.Enabled = true
		raConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()
		raConf.Apps.MonitorCloseChannel = opts.MonitorCloseChannel
		raConf.Apps.Apps = append([]service.App{
			{
				Name:       p.rootAppName,
				URI:        p.rootAppURI,
				PublicAddr: p.rootAppPublicAddr,
			},
			{
				Name:       p.rootWSAppName,
				URI:        p.rootWSAppURI,
				PublicAddr: p.rootWSPublicAddr,
			},
			{
				Name:       p.rootWSSAppName,
				URI:        p.rootWSSAppURI,
				PublicAddr: p.rootWSSPublicAddr,
			},
			{
				Name:       p.rootTCPAppName,
				URI:        p.rootTCPAppURI,
				PublicAddr: p.rootTCPPublicAddr,
			},
			{
				Name:       p.rootTCPTwoWayAppName,
				URI:        p.rootTCPTwoWayAppURI,
				PublicAddr: p.rootTCPTwoWayPublicAddr,
			},
			{
				Name:       p.jwtAppName,
				URI:        p.jwtAppURI,
				PublicAddr: p.jwtAppPublicAddr,
			},
			{
				Name:       p.headerAppName,
				URI:        p.headerAppURI,
				PublicAddr: p.headerAppPublicAddr,
			},
			{
				Name:       p.wsHeaderAppName,
				URI:        p.wsHeaderAppURI,
				PublicAddr: p.wsHeaderAppPublicAddr,
			},
			{
				Name:       p.flushAppName,
				URI:        p.flushAppURI,
				PublicAddr: p.flushAppPublicAddr,
			},
			{
				Name:       "dumper-root",
				URI:        p.dumperAppURI,
				PublicAddr: "dumper-root.example.com",
				Rewrite: &service.Rewrite{
					Headers: []service.Header{
						{
							Name:  "X-Teleport-Cluster",
							Value: "root",
						},
						{
							Name:  "X-External-Env",
							Value: "{{external.env}}",
						},
						// Make sure can rewrite Host header.
						{
							Name:  "Host",
							Value: "example.com",
						},
						// Make sure can rewrite existing header.
						{
							Name:  "X-Existing",
							Value: "rewritten-existing-header",
						},
						// Make sure can't rewrite Teleport headers.
						{
							Name:  teleport.AppJWTHeader,
							Value: "rewritten-app-jwt-header",
						},
						{
							Name:  teleport.AppCFHeader,
							Value: "rewritten-app-cf-header",
						},
						{
							Name:  forward.XForwardedFor,
							Value: "rewritten-x-forwarded-for-header",
						},
						{
							Name:  forward.XForwardedHost,
							Value: "rewritten-x-forwarded-host-header",
						},
						{
							Name:  forward.XForwardedProto,
							Value: "rewritten-x-forwarded-proto-header",
						},
						{
							Name:  forward.XForwardedServer,
							Value: "rewritten-x-forwarded-server-header",
						},
						{
							Name:  common.XForwardedSSL,
							Value: "rewritten-x-forwarded-ssl-header",
						},
						{
							Name:  forward.XForwardedPort,
							Value: "rewritten-x-forwarded-port-header",
						},
						// Make sure we can insert JWT token in custom header.
						{
							Name:  "X-JWT",
							Value: teleport.TraitInternalJWTVariable,
						},
					},
				},
			},
		}, opts.ExtraRootApps...)

		configs[i] = raConf
	}

	servers, err := p.rootCluster.StartApps(configs)
	require.NoError(t, err)
	require.Equal(t, len(configs), len(servers))

	for i, appServer := range servers {
		srv := appServer
		t.Cleanup(func() {
			require.NoError(t, srv.Close())
		})
		waitForAppServer(t, p.rootCluster.Tunnel, p.rootAppClusterName, srv.Config.HostUUID, configs[i].Apps.Apps)
	}

	return servers
}

func waitForAppServer(t *testing.T, tunnel reversetunnel.Server, name string, hostUUID string, apps []service.App) {
	// Make sure that the app server is ready to accept connections.
	// The remote site cache needs to be filled with new registered application services.
	waitForAppRegInRemoteSiteCache(t, tunnel, name, apps, hostUUID)
}

func (p *Pack) startLeafAppServers(t *testing.T, count int, opts AppTestOptions) []*service.TeleportProcess {
	log := utils.NewLoggerForTests()
	configs := make([]*service.Config, count)

	for i := 0; i < count; i++ {
		laConf := service.MakeDefaultConfig()
		laConf.Clock = opts.Clock
		laConf.Console = nil
		laConf.Log = log
		laConf.DataDir = t.TempDir()
		laConf.SetToken("static-token-value")
		laConf.SetAuthServerAddress(utils.NetAddr{
			AddrNetwork: "tcp",
			Addr:        p.leafCluster.Web,
		})
		laConf.Auth.Enabled = false
		laConf.Proxy.Enabled = false
		laConf.SSH.Enabled = false
		laConf.Apps.Enabled = true
		laConf.CircuitBreakerConfig = breaker.NoopBreakerConfig()
		laConf.Apps.MonitorCloseChannel = opts.MonitorCloseChannel
		laConf.Apps.Apps = append([]service.App{
			{
				Name:       p.leafAppName,
				URI:        p.leafAppURI,
				PublicAddr: p.leafAppPublicAddr,
			},
			{
				Name:       p.leafWSAppName,
				URI:        p.leafWSAppURI,
				PublicAddr: p.leafWSPublicAddr,
			},
			{
				Name:       p.leafWSSAppName,
				URI:        p.leafWSSAppURI,
				PublicAddr: p.leafWSSPublicAddr,
			},
			{
				Name:       p.leafTCPAppName,
				URI:        p.leafTCPAppURI,
				PublicAddr: p.leafTCPPublicAddr,
			},
			{
				Name:       "dumper-leaf",
				URI:        p.dumperAppURI,
				PublicAddr: "dumper-leaf.example.com",
				Rewrite: &service.Rewrite{
					Headers: []service.Header{
						{
							Name:  "X-Teleport-Cluster",
							Value: "leaf",
						},
						// In leaf clusters internal.logins variable is
						// populated with the user's root role logins.
						{
							Name:  "X-Teleport-Login",
							Value: "{{internal.logins}}",
						},
						{
							Name:  "X-External-Env",
							Value: "{{external.env}}",
						},
						// Make sure can rewrite Host header.
						{
							Name:  "Host",
							Value: "example.com",
						},
						// Make sure can rewrite existing header.
						{
							Name:  "X-Existing",
							Value: "rewritten-existing-header",
						},
						// Make sure can't rewrite Teleport headers.
						{
							Name:  teleport.AppJWTHeader,
							Value: "rewritten-app-jwt-header",
						},
						{
							Name:  teleport.AppCFHeader,
							Value: "rewritten-app-cf-header",
						},
						{
							Name:  forward.XForwardedFor,
							Value: "rewritten-x-forwarded-for-header",
						},
						{
							Name:  forward.XForwardedHost,
							Value: "rewritten-x-forwarded-host-header",
						},
						{
							Name:  forward.XForwardedProto,
							Value: "rewritten-x-forwarded-proto-header",
						},
						{
							Name:  forward.XForwardedServer,
							Value: "rewritten-x-forwarded-server-header",
						},
						{
							Name:  common.XForwardedSSL,
							Value: "rewritten-x-forwarded-ssl-header",
						},
						{
							Name:  forward.XForwardedPort,
							Value: "rewritten-x-forwarded-port-header",
						},
					},
				},
			},
		}, opts.ExtraLeafApps...)

		configs[i] = laConf
	}

	servers, err := p.leafCluster.StartApps(configs)
	require.NoError(t, err)
	require.Equal(t, len(configs), len(servers))

	for i, appServer := range servers {
		srv := appServer
		t.Cleanup(func() {
			require.NoError(t, srv.Close())
		})
		waitForAppServer(t, p.leafCluster.Tunnel, p.leafAppClusterName, srv.Config.HostUUID, configs[i].Apps.Apps)
	}

	return servers
}

func waitForAppRegInRemoteSiteCache(t *testing.T, tunnel reversetunnel.Server, clusterName string, cfgApps []service.App, hostUUID string) {
	require.Eventually(t, func() bool {
		site, err := tunnel.GetSite(clusterName)
		require.NoError(t, err)
		ap, err := site.CachingAccessPoint()
		require.NoError(t, err)
		apps, err := ap.GetApplicationServers(context.Background(), apidefaults.Namespace)
		require.NoError(t, err)

		counter := 0
		for _, v := range apps {
			if v.GetHostID() == hostUUID {
				counter++
			}
		}
		return len(cfgApps) == counter
	}, time.Minute*2, time.Millisecond*200)
}
