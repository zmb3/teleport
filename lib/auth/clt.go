/*
Copyright 2015-2021 Gravitational, Inc.

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

package auth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gravitational/roundtrip"
	"github.com/gravitational/trace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/constants"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/observability/tracing"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/httplib"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/session"
	"github.com/zmb3/teleport/lib/utils"
)

const (
	// CurrentVersion is a current API version
	CurrentVersion = types.V2

	// MissingNamespaceError indicates that the client failed to
	// provide the namespace in the request.
	MissingNamespaceError = "missing required parameter: namespace"
)

// Client is the Auth API client. It works by connecting to auth servers
// via gRPC and HTTP.
//
// When Teleport servers connect to auth API, they usually establish an SSH
// tunnel first, and then do HTTP-over-SSH. This client is wrapped by auth.TunClient
// in lib/auth/tun.go
//
// NOTE: This client is being deprecated in favor of the gRPC Client in
// teleport/api/client. This Client should only be used internally, or for
// functionality that hasn't been ported to the new client yet.
type Client struct {
	// APIClient is used to make gRPC requests to the server
	*APIClient
	// HTTPClient is used to make http requests to the server
	*HTTPClient
}

// Make sure Client implements all the necessary methods.
var _ ClientI = &Client{}

// NewClient creates a new API client with a connection to a Teleport server.
//
// The client will use the first credentials and the given dialer. If
// no dialer is given, the first address will be used. This address must
// be an auth server address.
//
// NOTE: This client is being deprecated in favor of the gRPC Client in
// teleport/api/client. This Client should only be used internally, or for
// functionality that hasn't been ported to the new client yet.
func NewClient(cfg client.Config, params ...roundtrip.ClientParam) (*Client, error) {
	cfg.DialInBackground = true
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	apiClient, err := client.New(cfg.Context, cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// apiClient configures the tls.Config, so we clone it and reuse it for http.
	tlsConfig := apiClient.Config().Clone()
	httpClient, err := NewHTTPClient(cfg, tlsConfig, params...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &Client{
		APIClient:  apiClient,
		HTTPClient: httpClient,
	}, nil
}

// APIClient is aliased here so that it can be embedded in Client.
type APIClient = client.Client

// HTTPClient is a teleport HTTP API client.
type HTTPClient struct {
	roundtrip.Client
	// transport defines the methods by which the client can reach the server.
	transport *http.Transport
	// TLS holds the TLS config for the http client.
	tls *tls.Config
}

// NewHTTPClient creates a new HTTP client with TLS authentication and the given dialer.
func NewHTTPClient(cfg client.Config, tls *tls.Config, params ...roundtrip.ClientParam) (*HTTPClient, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, err
	}

	dialer := cfg.Dialer
	if dialer == nil {
		if len(cfg.Addrs) == 0 {
			return nil, trace.BadParameter("no addresses to dial")
		}
		contextDialer := client.NewDialer(cfg.Context, cfg.KeepAlivePeriod, cfg.DialTimeout)
		dialer = client.ContextDialerFunc(func(ctx context.Context, network, _ string) (conn net.Conn, err error) {
			for _, addr := range cfg.Addrs {
				conn, err = contextDialer.DialContext(ctx, network, addr)
				if err == nil {
					return conn, nil
				}
			}
			// not wrapping on purpose to preserve the original error
			return nil, err
		})
	}

	// Set the next protocol. This is needed due to the Auth Server using a
	// multiplexer for protocol detection. Unless next protocol is specified
	// it will attempt to upgrade to HTTP2 and at that point there is no way
	// to distinguish between HTTP2/JSON or GPRC.
	tls.NextProtos = []string{teleport.HTTPNextProtoTLS}
	// Configure ALPN SNI direct dial TLS routing information used by ALPN SNI proxy in order to
	// dial auth service without using SSH tunnels.
	tls = client.ConfigureALPN(tls, cfg.ALPNSNIAuthDialClusterName)

	transport := &http.Transport{
		// notice that below roundtrip.Client is passed
		// teleport.APIDomain as an address for the API server, this is
		// to make sure client verifies the DNS name of the API server and
		// custom DialContext overrides this DNS name to the real address.
		// In addition this dialer tries multiple addresses if provided
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: apidefaults.DefaultDialTimeout,
		TLSClientConfig:       tls,

		// Increase the size of the connection pool. This substantially improves the
		// performance of Teleport under load as it reduces the number of TLS
		// handshakes performed.
		MaxIdleConns:        defaults.HTTPMaxIdleConns,
		MaxIdleConnsPerHost: defaults.HTTPMaxIdleConnsPerHost,

		// Limit the total number of connections to the Auth Server. Some hosts allow a low
		// number of connections per process (ulimit) to a host. This is a problem for
		// enhanced session recording auditing which emits so many events to the
		// Audit Log (using the Auth Client) that the connection pool often does not
		// have a free connection to return, so just opens a new one. This quickly
		// leads to hitting the OS limit and the client returning out of file
		// descriptors error.
		MaxConnsPerHost: defaults.HTTPMaxConnsPerHost,

		// IdleConnTimeout defines the maximum amount of time before idle connections
		// are closed. Leaving this unset will lead to connections open forever and
		// will cause memory leaks in a long running process.
		IdleConnTimeout: defaults.HTTPIdleTimeout,
	}

	cb, err := breaker.New(cfg.CircuitBreakerConfig)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clientParams := append(
		[]roundtrip.ClientParam{
			roundtrip.HTTPClient(&http.Client{
				Timeout: defaults.HTTPRequestTimeout,
				Transport: otelhttp.NewTransport(
					breaker.NewRoundTripper(cb, transport),
					otelhttp.WithSpanNameFormatter(tracing.HTTPTransportFormatter),
				),
			}),
			roundtrip.SanitizerEnabled(true),
		},
		params...,
	)

	// Since the client uses a custom dialer and SNI is used for TLS handshake, the address
	// used here is arbitrary as it just needs to be set to pass http request validation.
	httpClient, err := roundtrip.NewClient("https://"+constants.APIDomain, CurrentVersion, clientParams...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &HTTPClient{
		Client:    *httpClient,
		transport: transport,
		tls:       tls,
	}, nil
}

// Close closes the HTTP client connection to the auth server.
func (c *HTTPClient) Close() {
	c.transport.CloseIdleConnections()
}

// TLSConfig returns the HTTP client's TLS config.
func (c *HTTPClient) TLSConfig() *tls.Config {
	return c.tls
}

// GetTransport returns the HTTP client's transport.
func (c *HTTPClient) GetTransport() *http.Transport {
	return c.transport
}

// ClientTimeout sets idle and dial timeouts of the HTTP transport
// used by the client.
func ClientTimeout(timeout time.Duration) roundtrip.ClientParam {
	return func(c *roundtrip.Client) error {
		transport, ok := (c.HTTPClient().Transport).(*http.Transport)
		if !ok {
			return nil
		}
		transport.IdleConnTimeout = timeout
		transport.ResponseHeaderTimeout = timeout
		return nil
	}
}

// PostJSON is a generic method that issues http POST request to the server
func (c *Client) PostJSON(ctx context.Context, endpoint string, val interface{}) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(c.Client.PostJSON(ctx, endpoint, val))
}

// PutJSON is a generic method that issues http PUT request to the server
func (c *Client) PutJSON(ctx context.Context, endpoint string, val interface{}) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(c.Client.PutJSON(ctx, endpoint, val))
}

// PostForm is a generic method that issues http POST request to the server
func (c *Client) PostForm(ctx context.Context, endpoint string, vals url.Values, files ...roundtrip.File) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(c.Client.PostForm(ctx, endpoint, vals, files...))
}

// Get issues http GET request to the server
func (c *Client) Get(ctx context.Context, u string, params url.Values) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(c.Client.Get(ctx, u, params))
}

// Delete issues http Delete Request to the server
func (c *Client) Delete(ctx context.Context, u string) (*roundtrip.Response, error) {
	return httplib.ConvertResponse(c.Client.Delete(ctx, u))
}

// ProcessKubeCSR processes CSR request against Kubernetes CA, returns
// signed certificate if successful.
func (c *Client) ProcessKubeCSR(req KubeCSR) (*KubeCSRResponse, error) {
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	out, err := c.PostJSON(context.TODO(), c.Endpoint("kube", "csr"), req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var re KubeCSRResponse
	if err := json.Unmarshal(out.Bytes(), &re); err != nil {
		return nil, trace.Wrap(err)
	}
	return &re, nil
}

// GetSessions returns a list of active sessions in the cluster as reported by
// the auth server.
// DELETE IN 12.0.0
func (c *Client) GetSessions(ctx context.Context, namespace string) ([]session.Session, error) {
	if namespace == "" {
		return nil, trace.BadParameter(MissingNamespaceError)
	}
	out, err := c.Get(ctx, c.Endpoint("namespaces", namespace, "sessions"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var sessions []session.Session
	if err := json.Unmarshal(out.Bytes(), &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// GetSession returns a session by ID
// DELETE IN 12.0.0
func (c *Client) GetSession(ctx context.Context, namespace string, id session.ID) (*session.Session, error) {
	if namespace == "" {
		return nil, trace.BadParameter(MissingNamespaceError)
	}
	// saving extra round-trip
	if err := id.Check(); err != nil {
		return nil, trace.Wrap(err)
	}
	out, err := c.Get(ctx, c.Endpoint("namespaces", namespace, "sessions", string(id)), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var sess *session.Session
	if err := json.Unmarshal(out.Bytes(), &sess); err != nil {
		return nil, trace.Wrap(err)
	}
	return sess, nil
}

// DeleteSession removes an active session from the backend.
// DELETE IN 12.0.0
func (c *Client) DeleteSession(ctx context.Context, namespace string, id session.ID) error {
	if namespace == "" {
		return trace.BadParameter(MissingNamespaceError)
	}
	_, err := c.Delete(ctx, c.Endpoint("namespaces", namespace, "sessions", string(id)))
	return trace.Wrap(err)
}

// CreateSession creates new session
// DELETE IN 12.0.0
func (c *Client) CreateSession(ctx context.Context, sess session.Session) error {
	if sess.Namespace == "" {
		return trace.BadParameter(MissingNamespaceError)
	}
	_, err := c.PostJSON(ctx, c.Endpoint("namespaces", sess.Namespace, "sessions"), createSessionReq{Session: sess})
	return trace.Wrap(err)
}

// UpdateSession updates existing session
// DELETE IN 12.0.0
func (c *Client) UpdateSession(ctx context.Context, req session.UpdateRequest) error {
	if err := req.Check(); err != nil {
		return trace.Wrap(err)
	}
	_, err := c.PutJSON(ctx, c.Endpoint("namespaces", req.Namespace, "sessions", string(req.ID)), updateSessionReq{Update: req})
	return trace.Wrap(err)
}

func (c *Client) Close() error {
	c.HTTPClient.Close()
	return c.APIClient.Close()
}

// CreateCertAuthority not implemented: can only be called locally.
func (c *Client) CreateCertAuthority(ca types.CertAuthority) error {
	return trace.NotImplemented(notImplementedMessage)
}

// RotateCertAuthority starts or restarts certificate authority rotation process.
func (c *Client) RotateCertAuthority(ctx context.Context, req RotateRequest) error {
	caType := "all"
	if req.Type != "" {
		caType = string(req.Type)
	}
	_, err := c.PostJSON(ctx, c.Endpoint("authorities", caType, "rotate"), req)
	return trace.Wrap(err)
}

// RotateExternalCertAuthority rotates external certificate authority,
// this method is used to update only public keys and certificates of the
// the certificate authorities of trusted clusters.
func (c *Client) RotateExternalCertAuthority(ctx context.Context, ca types.CertAuthority) error {
	if err := services.ValidateCertAuthority(ca); err != nil {
		return trace.Wrap(err)
	}
	data, err := services.MarshalCertAuthority(ca)
	if err != nil {
		return trace.Wrap(err)
	}
	_, err = c.PostJSON(ctx, c.Endpoint("authorities", string(ca.GetType()), "rotate", "external"),
		&rotateExternalCertAuthorityRawReq{CA: data})
	return trace.Wrap(err)
}

// UpsertCertAuthority updates or inserts new cert authority
func (c *Client) UpsertCertAuthority(ca types.CertAuthority) error {
	if err := services.ValidateCertAuthority(ca); err != nil {
		return trace.Wrap(err)
	}
	data, err := services.MarshalCertAuthority(ca)
	if err != nil {
		return trace.Wrap(err)
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("authorities", string(ca.GetType())),
		&upsertCertAuthorityRawReq{CA: data})
	return trace.Wrap(err)
}

// CompareAndSwapCertAuthority updates existing cert authority if the existing cert authority
// value matches the value stored in the backend.
func (c *Client) CompareAndSwapCertAuthority(new, existing types.CertAuthority) error {
	return trace.BadParameter("this function is not supported on the client")
}

// GetCertAuthorities returns a list of certificate authorities
func (c *Client) GetCertAuthorities(ctx context.Context, caType types.CertAuthType, loadKeys bool, opts ...services.MarshalOption) ([]types.CertAuthority, error) {
	if err := caType.Check(); err != nil {
		return nil, trace.Wrap(err)
	}
	out, err := c.Get(ctx, c.Endpoint("authorities", string(caType)), url.Values{
		"load_keys": []string{fmt.Sprintf("%t", loadKeys)},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, err
	}
	re := make([]types.CertAuthority, len(items))
	for i, raw := range items {
		ca, err := services.UnmarshalCertAuthority(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		re[i] = ca
	}
	return re, nil
}

// GetCertAuthority returns certificate authority by given id. Parameter loadSigningKeys
// controls if signing keys are loaded
func (c *Client) GetCertAuthority(ctx context.Context, id types.CertAuthID, loadSigningKeys bool, opts ...services.MarshalOption) (types.CertAuthority, error) {
	if err := id.Check(); err != nil {
		return nil, trace.Wrap(err)
	}
	out, err := c.Get(ctx, c.Endpoint("authorities", string(id.Type), id.DomainName), url.Values{
		"load_keys": []string{fmt.Sprintf("%t", loadSigningKeys)},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalCertAuthority(out.Bytes())
}

// DeleteCertAuthority deletes cert authority by ID
func (c *Client) DeleteCertAuthority(id types.CertAuthID) error {
	if err := id.Check(); err != nil {
		return trace.Wrap(err)
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("authorities", string(id.Type), id.DomainName))
	return trace.Wrap(err)
}

// ActivateCertAuthority not implemented: can only be called locally.
func (c *Client) ActivateCertAuthority(id types.CertAuthID) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeactivateCertAuthority not implemented: can only be called locally.
func (c *Client) DeactivateCertAuthority(id types.CertAuthID) error {
	return trace.NotImplemented(notImplementedMessage)
}

// RegisterUsingToken calls the auth service API to register a new node using a registration token
// which was previously issued via GenerateToken.
func (c *Client) RegisterUsingToken(ctx context.Context, req *types.RegisterUsingTokenRequest) (*proto.Certs, error) {
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	out, err := c.PostJSON(ctx, c.Endpoint("tokens", "register"), req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var certs proto.Certs
	if err := json.Unmarshal(out.Bytes(), &certs); err != nil {
		return nil, trace.Wrap(err)
	}

	return &certs, nil
}

// DELETE IN: 5.1.0
//
// This logic has been moved to KeepAliveServer.
//
// KeepAliveNode updates node keep alive information.
func (c *Client) KeepAliveNode(ctx context.Context, keepAlive types.KeepAlive) error {
	return trace.BadParameter("not implemented, use StreamKeepAlives instead")
}

// KeepAliveServer not implemented: can only be called locally.
func (c *Client) KeepAliveServer(ctx context.Context, keepAlive types.KeepAlive) error {
	return trace.BadParameter("not implemented, use StreamKeepAlives instead")
}

// UpsertReverseTunnel is used by admins to create a new reverse tunnel
// to the remote proxy to bypass firewall restrictions
func (c *Client) UpsertReverseTunnel(tunnel types.ReverseTunnel) error {
	data, err := services.MarshalReverseTunnel(tunnel)
	if err != nil {
		return trace.Wrap(err)
	}
	args := &upsertReverseTunnelRawReq{
		ReverseTunnel: data,
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("reversetunnels"), args)
	return trace.Wrap(err)
}

// GetReverseTunnel not implemented: can only be called locally.
func (c *Client) GetReverseTunnel(name string, opts ...services.MarshalOption) (types.ReverseTunnel, error) {
	return nil, trace.NotImplemented(notImplementedMessage)
}

// GetReverseTunnels returns the list of created reverse tunnels
func (c *Client) GetReverseTunnels(ctx context.Context, opts ...services.MarshalOption) ([]types.ReverseTunnel, error) {
	out, err := c.Get(ctx, c.Endpoint("reversetunnels"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	tunnels := make([]types.ReverseTunnel, len(items))
	for i, raw := range items {
		tunnel, err := services.UnmarshalReverseTunnel(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		tunnels[i] = tunnel
	}
	return tunnels, nil
}

// DeleteReverseTunnel deletes reverse tunnel by domain name
func (c *Client) DeleteReverseTunnel(domainName string) error {
	// this is to avoid confusing error in case if domain empty for example
	// HTTP route will fail producing generic not found error
	// instead we catch the error here
	if strings.TrimSpace(domainName) == "" {
		return trace.BadParameter("empty domain name")
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("reversetunnels", domainName))
	return trace.Wrap(err)
}

// UpsertTunnelConnection upserts tunnel connection
func (c *Client) UpsertTunnelConnection(conn types.TunnelConnection) error {
	data, err := services.MarshalTunnelConnection(conn)
	if err != nil {
		return trace.Wrap(err)
	}
	args := &upsertTunnelConnectionRawReq{
		TunnelConnection: data,
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("tunnelconnections"), args)
	return trace.Wrap(err)
}

// GetTunnelConnections returns tunnel connections for a given cluster
func (c *Client) GetTunnelConnections(clusterName string, opts ...services.MarshalOption) ([]types.TunnelConnection, error) {
	if clusterName == "" {
		return nil, trace.BadParameter("missing cluster name parameter")
	}
	out, err := c.Get(context.TODO(), c.Endpoint("tunnelconnections", clusterName), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	conns := make([]types.TunnelConnection, len(items))
	for i, raw := range items {
		conn, err := services.UnmarshalTunnelConnection(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		conns[i] = conn
	}
	return conns, nil
}

// GetAllTunnelConnections returns all tunnel connections
func (c *Client) GetAllTunnelConnections(opts ...services.MarshalOption) ([]types.TunnelConnection, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("tunnelconnections"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	conns := make([]types.TunnelConnection, len(items))
	for i, raw := range items {
		conn, err := services.UnmarshalTunnelConnection(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		conns[i] = conn
	}
	return conns, nil
}

// DeleteTunnelConnection deletes tunnel connection by name
func (c *Client) DeleteTunnelConnection(clusterName string, connName string) error {
	if clusterName == "" {
		return trace.BadParameter("missing parameter cluster name")
	}
	if connName == "" {
		return trace.BadParameter("missing parameter connection name")
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("tunnelconnections", clusterName, connName))
	return trace.Wrap(err)
}

// DeleteTunnelConnections deletes all tunnel connections for cluster
func (c *Client) DeleteTunnelConnections(clusterName string) error {
	if clusterName == "" {
		return trace.BadParameter("missing parameter cluster name")
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("tunnelconnections", clusterName))
	return trace.Wrap(err)
}

// DeleteAllTokens not implemented: can only be called locally.
func (c *Client) DeleteAllTokens() error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllTunnelConnections deletes all tunnel connections
func (c *Client) DeleteAllTunnelConnections() error {
	_, err := c.Delete(context.TODO(), c.Endpoint("tunnelconnections"))
	return trace.Wrap(err)
}

// AddUserLoginAttempt logs user login attempt
func (c *Client) AddUserLoginAttempt(user string, attempt services.LoginAttempt, ttl time.Duration) error {
	panic("not implemented")
}

// GetUserLoginAttempts returns user login attempts
func (c *Client) GetUserLoginAttempts(user string) ([]services.LoginAttempt, error) {
	panic("not implemented")
}

// GetRemoteClusters returns a list of remote clusters
func (c *Client) GetRemoteClusters(opts ...services.MarshalOption) ([]types.RemoteCluster, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("remoteclusters"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	conns := make([]types.RemoteCluster, len(items))
	for i, raw := range items {
		conn, err := services.UnmarshalRemoteCluster(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		conns[i] = conn
	}
	return conns, nil
}

// GetRemoteCluster returns a remote cluster by name
func (c *Client) GetRemoteCluster(clusterName string) (types.RemoteCluster, error) {
	if clusterName == "" {
		return nil, trace.BadParameter("missing cluster name")
	}
	out, err := c.Get(context.TODO(), c.Endpoint("remoteclusters", clusterName), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalRemoteCluster(out.Bytes())
}

// DeleteRemoteCluster deletes remote cluster by name
func (c *Client) DeleteRemoteCluster(clusterName string) error {
	if clusterName == "" {
		return trace.BadParameter("missing parameter cluster name")
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("remoteclusters", clusterName))
	return trace.Wrap(err)
}

// DeleteAllRemoteClusters deletes all remote clusters
func (c *Client) DeleteAllRemoteClusters() error {
	_, err := c.Delete(context.TODO(), c.Endpoint("remoteclusters"))
	return trace.Wrap(err)
}

// CreateRemoteCluster creates remote cluster resource
func (c *Client) CreateRemoteCluster(rc types.RemoteCluster) error {
	data, err := services.MarshalRemoteCluster(rc)
	if err != nil {
		return trace.Wrap(err)
	}
	args := &createRemoteClusterRawReq{
		RemoteCluster: data,
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("remoteclusters"), args)
	return trace.Wrap(err)
}

// UpsertAuthServer is used by auth servers to report their presence
// to other auth servers in form of hearbeat expiring after ttl period.
func (c *Client) UpsertAuthServer(s types.Server) error {
	data, err := services.MarshalServer(s)
	if err != nil {
		return trace.Wrap(err)
	}
	args := &upsertServerRawReq{
		Server: data,
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("authservers"), args)
	return trace.Wrap(err)
}

// GetAuthServers returns the list of auth servers registered in the cluster.
func (c *Client) GetAuthServers() ([]types.Server, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("authservers"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	re := make([]types.Server, len(items))
	for i, raw := range items {
		server, err := services.UnmarshalServer(raw, types.KindAuthServer)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		re[i] = server
	}
	return re, nil
}

// DeleteAllAuthServers not implemented: can only be called locally.
func (c *Client) DeleteAllAuthServers() error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAuthServer not implemented: can only be called locally.
func (c *Client) DeleteAuthServer(name string) error {
	return trace.NotImplemented(notImplementedMessage)
}

// UpsertProxy is used by proxies to report their presence
// to other auth servers in form of hearbeat expiring after ttl period.
func (c *Client) UpsertProxy(s types.Server) error {
	data, err := services.MarshalServer(s)
	if err != nil {
		return trace.Wrap(err)
	}
	args := &upsertServerRawReq{
		Server: data,
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("proxies"), args)
	return trace.Wrap(err)
}

// GetProxies returns the list of auth servers registered in the cluster.
func (c *Client) GetProxies() ([]types.Server, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("proxies"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, trace.Wrap(err)
	}
	re := make([]types.Server, len(items))
	for i, raw := range items {
		server, err := services.UnmarshalServer(raw, types.KindProxy)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		re[i] = server
	}
	return re, nil
}

// DeleteAllProxies deletes all proxies
func (c *Client) DeleteAllProxies() error {
	_, err := c.Delete(context.TODO(), c.Endpoint("proxies"))
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// DeleteProxy deletes proxy by name
func (c *Client) DeleteProxy(name string) error {
	if name == "" {
		return trace.BadParameter("missing parameter name")
	}
	_, err := c.Delete(context.TODO(), c.Endpoint("proxies", name))
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// UpsertUser user updates user entry.
func (c *Client) UpsertUser(user types.User) error {
	data, err := services.MarshalUser(user)
	if err != nil {
		return trace.Wrap(err)
	}
	_, err = c.PostJSON(context.TODO(), c.Endpoint("users"), &upsertUserRawReq{User: data})
	return trace.Wrap(err)
}

// CompareAndSwapUser not implemented: can only be called locally
func (c *Client) CompareAndSwapUser(ctx context.Context, new, expected types.User) error {
	return trace.NotImplemented(notImplementedMessage)
}

// ChangePassword updates users password based on the old password.
func (c *Client) ChangePassword(req services.ChangePasswordReq) error {
	_, err := c.PutJSON(context.TODO(), c.Endpoint("users", req.User, "web", "password"), req)
	return trace.Wrap(err)
}

// CheckPassword checks if the suplied web access password is valid.
func (c *Client) CheckPassword(user string, password []byte, otpToken string) error {
	_, err := c.PostJSON(
		context.TODO(),
		c.Endpoint("users", user, "web", "password", "check"),
		checkPasswordReq{
			Password: string(password),
			OTPToken: otpToken,
		})
	return trace.Wrap(err)
}

// ExtendWebSession creates a new web session for a user based on another
// valid web session
func (c *Client) ExtendWebSession(ctx context.Context, req WebSessionReq) (types.WebSession, error) {
	out, err := c.PostJSON(ctx, c.Endpoint("users", req.User, "web", "sessions"), req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalWebSession(out.Bytes())
}

// CreateWebSession creates a new web session for a user
func (c *Client) CreateWebSession(ctx context.Context, user string) (types.WebSession, error) {
	out, err := c.PostJSON(
		ctx,
		c.Endpoint("users", user, "web", "sessions"),
		WebSessionReq{User: user},
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalWebSession(out.Bytes())
}

// AuthenticateWebUser authenticates web user, creates and  returns web session
// in case if authentication is successful
func (c *Client) AuthenticateWebUser(ctx context.Context, req AuthenticateUserRequest) (types.WebSession, error) {
	out, err := c.PostJSON(
		ctx,
		c.Endpoint("users", req.Username, "web", "authenticate"),
		req,
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalWebSession(out.Bytes())
}

// AuthenticateSSHUser authenticates SSH console user, creates and  returns a pair of signed TLS and SSH
// short lived certificates as a result
func (c *Client) AuthenticateSSHUser(ctx context.Context, req AuthenticateSSHRequest) (*SSHLoginResponse, error) {
	out, err := c.PostJSON(
		ctx,
		c.Endpoint("users", req.Username, "ssh", "authenticate"),
		req,
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var re SSHLoginResponse
	if err := json.Unmarshal(out.Bytes(), &re); err != nil {
		return nil, trace.Wrap(err)
	}
	return &re, nil
}

// GetWebSessionInfo checks if a web sesion is valid, returns session id in case if
// it is valid, or error otherwise.
func (c *Client) GetWebSessionInfo(ctx context.Context, user, sessionID string) (types.WebSession, error) {
	out, err := c.Get(
		ctx,
		c.Endpoint("users", user, "web", "sessions", sessionID), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalWebSession(out.Bytes())
}

// DeleteWebSession deletes the web session specified with sid for the given user
func (c *Client) DeleteWebSession(ctx context.Context, user string, sid string) error {
	_, err := c.Delete(ctx, c.Endpoint("users", user, "web", "sessions", sid))
	return trace.Wrap(err)
}

// GenerateHostCert takes the public key in the Open SSH “authorized_keys“
// plain text format, signs it using Host Certificate Authority private key and returns the
// resulting certificate.
func (c *Client) GenerateHostCert(
	ctx context.Context, key []byte, hostID, nodeName string, principals []string, clusterName string, role types.SystemRole, ttl time.Duration,
) ([]byte, error) {
	out, err := c.PostJSON(ctx, c.Endpoint("ca", "host", "certs"),
		generateHostCertReq{
			Key:         key,
			HostID:      hostID,
			NodeName:    nodeName,
			Principals:  principals,
			ClusterName: clusterName,
			Roles:       types.SystemRoles{role},
			TTL:         ttl,
		})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var cert string
	if err := json.Unmarshal(out.Bytes(), &cert); err != nil {
		return nil, err
	}

	return []byte(cert), nil
}

// ValidateOIDCAuthCallback validates OIDC auth callback returned from redirect
func (c *Client) ValidateOIDCAuthCallback(ctx context.Context, q url.Values) (*OIDCAuthResponse, error) {
	out, err := c.PostJSON(ctx, c.Endpoint("oidc", "requests", "validate"), validateOIDCAuthCallbackReq{
		Query: q,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var rawResponse *oidcAuthRawResponse
	if err := json.Unmarshal(out.Bytes(), &rawResponse); err != nil {
		return nil, trace.Wrap(err)
	}
	response := OIDCAuthResponse{
		Username: rawResponse.Username,
		Identity: rawResponse.Identity,
		Cert:     rawResponse.Cert,
		Req:      rawResponse.Req,
		TLSCert:  rawResponse.TLSCert,
	}
	if len(rawResponse.Session) != 0 {
		session, err := services.UnmarshalWebSession(rawResponse.Session)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.Session = session
	}
	response.HostSigners = make([]types.CertAuthority, len(rawResponse.HostSigners))
	for i, raw := range rawResponse.HostSigners {
		ca, err := services.UnmarshalCertAuthority(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.HostSigners[i] = ca
	}
	return &response, nil
}

// ValidateSAMLResponse validates response returned by SAML identity provider
func (c *Client) ValidateSAMLResponse(ctx context.Context, re string, connectorID string) (*SAMLAuthResponse, error) {
	out, err := c.PostJSON(ctx, c.Endpoint("saml", "requests", "validate"), validateSAMLResponseReq{
		Response:    re,
		ConnectorID: connectorID,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var rawResponse *samlAuthRawResponse
	if err := json.Unmarshal(out.Bytes(), &rawResponse); err != nil {
		return nil, trace.Wrap(err)
	}
	response := SAMLAuthResponse{
		Username: rawResponse.Username,
		Identity: rawResponse.Identity,
		Cert:     rawResponse.Cert,
		Req:      rawResponse.Req,
		TLSCert:  rawResponse.TLSCert,
	}
	if len(rawResponse.Session) != 0 {
		session, err := services.UnmarshalWebSession(rawResponse.Session)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.Session = session
	}
	response.HostSigners = make([]types.CertAuthority, len(rawResponse.HostSigners))
	for i, raw := range rawResponse.HostSigners {
		ca, err := services.UnmarshalCertAuthority(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.HostSigners[i] = ca
	}
	return &response, nil
}

// ValidateGithubAuthCallback validates Github auth callback returned from redirect
func (c *Client) ValidateGithubAuthCallback(ctx context.Context, q url.Values) (*GithubAuthResponse, error) {
	out, err := c.PostJSON(ctx, c.Endpoint("github", "requests", "validate"),
		validateGithubAuthCallbackReq{Query: q})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var rawResponse githubAuthRawResponse
	if err := json.Unmarshal(out.Bytes(), &rawResponse); err != nil {
		return nil, trace.Wrap(err)
	}
	response := GithubAuthResponse{
		Username: rawResponse.Username,
		Identity: rawResponse.Identity,
		Cert:     rawResponse.Cert,
		Req:      rawResponse.Req,
		TLSCert:  rawResponse.TLSCert,
	}
	if len(rawResponse.Session) != 0 {
		session, err := services.UnmarshalWebSession(
			rawResponse.Session)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.Session = session
	}
	response.HostSigners = make([]types.CertAuthority, len(rawResponse.HostSigners))
	for i, raw := range rawResponse.HostSigners {
		ca, err := services.UnmarshalCertAuthority(raw)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.HostSigners[i] = ca
	}
	return &response, nil
}

// GetSessionChunk allows clients to receive a byte array (chunk) from a recorded
// session stream, starting from 'offset', up to 'max' in length. The upper bound
// of 'max' is set to events.MaxChunkBytes
func (c *Client) GetSessionChunk(namespace string, sid session.ID, offsetBytes, maxBytes int) ([]byte, error) {
	if namespace == "" {
		return nil, trace.BadParameter(MissingNamespaceError)
	}
	response, err := c.Get(context.TODO(), c.Endpoint("namespaces", namespace, "sessions", string(sid), "stream"), url.Values{
		"offset": []string{strconv.Itoa(offsetBytes)},
		"bytes":  []string{strconv.Itoa(maxBytes)},
	})
	if err != nil {
		log.Error(err)
		return nil, trace.Wrap(err)
	}
	return response.Bytes(), nil
}

// Returns events that happen during a session sorted by time
// (oldest first).
//
// afterN allows to filter by "newer than N" value where N is the cursor ID
// of previously returned bunch (good for polling for latest)
//
// This function is usually used in conjunction with GetSessionReader to
// replay recorded session streams.
func (c *Client) GetSessionEvents(namespace string, sid session.ID, afterN int, includePrintEvents bool) (retval []events.EventFields, err error) {
	if namespace == "" {
		return nil, trace.BadParameter(MissingNamespaceError)
	}
	query := make(url.Values)
	if afterN > 0 {
		query.Set("after", strconv.Itoa(afterN))
	}
	if includePrintEvents {
		query.Set("print", fmt.Sprintf("%v", includePrintEvents))
	}
	response, err := c.Get(context.TODO(), c.Endpoint("namespaces", namespace, "sessions", string(sid), "events"), query)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	retval = make([]events.EventFields, 0)
	if err := json.Unmarshal(response.Bytes(), &retval); err != nil {
		return nil, trace.Wrap(err)
	}
	return retval, nil
}

// StreamSessionEvents streams all events from a given session recording. An error is returned on the first
// channel if one is encountered. Otherwise the event channel is closed when the stream ends.
// The event channel is not closed on error to prevent race conditions in downstream select statements.
func (c *Client) StreamSessionEvents(ctx context.Context, sessionID session.ID, startIndex int64) (chan apievents.AuditEvent, chan error) {
	return c.APIClient.StreamSessionEvents(ctx, string(sessionID), startIndex)
}

// SearchEvents allows searching for audit events with pagination support.
func (c *Client) SearchEvents(fromUTC, toUTC time.Time, namespace string, eventTypes []string, limit int, order types.EventOrder, startKey string) ([]apievents.AuditEvent, string, error) {
	events, lastKey, err := c.APIClient.SearchEvents(context.TODO(), fromUTC, toUTC, namespace, eventTypes, limit, order, startKey)
	if err != nil {
		return nil, "", trace.Wrap(err)
	}

	return events, lastKey, nil
}

// SearchSessionEvents returns session related events to find completed sessions.
func (c *Client) SearchSessionEvents(fromUTC, toUTC time.Time, limit int, order types.EventOrder, startKey string, cond *types.WhereExpr, sessionID string) ([]apievents.AuditEvent, string, error) {
	events, lastKey, err := c.APIClient.SearchSessionEvents(context.TODO(), fromUTC, toUTC, limit, order, startKey)
	if err != nil {
		return nil, "", trace.Wrap(err)
	}

	return events, lastKey, nil
}

// GetNamespaces returns a list of namespaces
func (c *Client) GetNamespaces() ([]types.Namespace, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("namespaces"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var re []types.Namespace
	if err := utils.FastUnmarshal(out.Bytes(), &re); err != nil {
		return nil, trace.Wrap(err)
	}
	return re, nil
}

// GetNamespace returns namespace by name
func (c *Client) GetNamespace(name string) (*types.Namespace, error) {
	if name == "" {
		return nil, trace.BadParameter("missing namespace name")
	}
	out, err := c.Get(context.TODO(), c.Endpoint("namespaces", name), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.UnmarshalNamespace(out.Bytes())
}

// UpsertNamespace upserts namespace
func (c *Client) UpsertNamespace(ns types.Namespace) error {
	_, err := c.PostJSON(context.TODO(), c.Endpoint("namespaces"), upsertNamespaceReq{Namespace: ns})
	return trace.Wrap(err)
}

// DeleteNamespace deletes namespace by name
func (c *Client) DeleteNamespace(name string) error {
	_, err := c.Delete(context.TODO(), c.Endpoint("namespaces", name))
	return trace.Wrap(err)
}

// CreateRole not implemented: can only be called locally.
func (c *Client) CreateRole(ctx context.Context, role types.Role) error {
	return trace.NotImplemented(notImplementedMessage)
}

// GetClusterName returns a cluster name
func (c *Client) GetClusterName(opts ...services.MarshalOption) (types.ClusterName, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("configuration", "name"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cn, err := services.UnmarshalClusterName(out.Bytes())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return cn, err
}

// SetClusterName sets cluster name once, will
// return Already Exists error if the name is already set
func (c *Client) SetClusterName(cn types.ClusterName) error {
	data, err := services.MarshalClusterName(cn)
	if err != nil {
		return trace.Wrap(err)
	}

	_, err = c.PostJSON(context.TODO(), c.Endpoint("configuration", "name"), &setClusterNameReq{ClusterName: data})
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// UpsertClusterName not implemented: can only be called locally.
func (c *Client) UpsertClusterName(cn types.ClusterName) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteStaticTokens deletes static tokens
func (c *Client) DeleteStaticTokens() error {
	_, err := c.Delete(context.TODO(), c.Endpoint("configuration", "static_tokens"))
	return trace.Wrap(err)
}

// GetStaticTokens returns a list of static register tokens
func (c *Client) GetStaticTokens() (types.StaticTokens, error) {
	out, err := c.Get(context.TODO(), c.Endpoint("configuration", "static_tokens"), url.Values{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	st, err := services.UnmarshalStaticTokens(out.Bytes())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return st, err
}

// SetStaticTokens sets a list of static register tokens
func (c *Client) SetStaticTokens(st types.StaticTokens) error {
	data, err := services.MarshalStaticTokens(st)
	if err != nil {
		return trace.Wrap(err)
	}

	_, err = c.PostJSON(context.TODO(), c.Endpoint("configuration", "static_tokens"), &setStaticTokensReq{StaticTokens: data})
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// DeleteClusterName not implemented: can only be called locally.
func (c *Client) DeleteClusterName() error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllCertAuthorities not implemented: can only be called locally.
func (c *Client) DeleteAllCertAuthorities(caType types.CertAuthType) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllReverseTunnels not implemented: can only be called locally.
func (c *Client) DeleteAllReverseTunnels() error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllCertNamespaces not implemented: can only be called locally.
func (c *Client) DeleteAllNamespaces() error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllRoles not implemented: can only be called locally.
func (c *Client) DeleteAllRoles() error {
	return trace.NotImplemented(notImplementedMessage)
}

// ListWindowsDesktops not implemented: can only be called locally.
func (c *Client) ListWindowsDesktops(ctx context.Context, req types.ListWindowsDesktopsRequest) (*types.ListWindowsDesktopsResponse, error) {
	return nil, trace.NotImplemented(notImplementedMessage)
}

// ListWindowsDesktopServices not implemented: can only be called locally.
func (c *Client) ListWindowsDesktopServices(ctx context.Context, req types.ListWindowsDesktopServicesRequest) (*types.ListWindowsDesktopServicesResponse, error) {
	return nil, trace.NotImplemented(notImplementedMessage)
}

// DeleteAllUsers not implemented: can only be called locally.
func (c *Client) DeleteAllUsers() error {
	return trace.NotImplemented(notImplementedMessage)
}

func (c *Client) ValidateTrustedCluster(ctx context.Context, validateRequest *ValidateTrustedClusterRequest) (*ValidateTrustedClusterResponse, error) {
	validateRequestRaw, err := validateRequest.ToRaw()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	out, err := c.PostJSON(ctx, c.Endpoint("trustedclusters", "validate"), validateRequestRaw)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var validateResponseRaw ValidateTrustedClusterResponseRaw
	err = json.Unmarshal(out.Bytes(), &validateResponseRaw)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	validateResponse, err := validateResponseRaw.ToNative()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return validateResponse, nil
}

// CreateResetPasswordToken creates reset password token
func (c *Client) CreateResetPasswordToken(ctx context.Context, req CreateUserTokenRequest) (types.UserToken, error) {
	return c.APIClient.CreateResetPasswordToken(ctx, &proto.CreateResetPasswordTokenRequest{
		Name: req.Name,
		TTL:  proto.Duration(req.TTL),
		Type: req.Type,
	})
}

// CreateBot creates a bot and associated resources.
func (c *Client) CreateBot(ctx context.Context, req *proto.CreateBotRequest) (*proto.CreateBotResponse, error) {
	return c.APIClient.CreateBot(ctx, req)
}

// DeleteBot deletes a certificate renewal bot and associated resources.
func (c *Client) DeleteBot(ctx context.Context, botName string) error {
	return c.APIClient.DeleteBot(ctx, botName)
}

// GetBotUsers fetches all bot users.
func (c *Client) GetBotUsers(ctx context.Context) ([]types.User, error) {
	return c.APIClient.GetBotUsers(ctx)
}

// GetDatabaseServers returns all registered database proxy servers.
func (c *Client) GetDatabaseServers(ctx context.Context, namespace string, opts ...services.MarshalOption) ([]types.DatabaseServer, error) {
	return c.APIClient.GetDatabaseServers(ctx, namespace)
}

// UpsertAppSession not implemented: can only be called locally.
func (c *Client) UpsertAppSession(ctx context.Context, session types.WebSession) error {
	return trace.NotImplemented(notImplementedMessage)
}

// UpsertSnowflakeSession not implemented: can only be called locally.
func (c *Client) UpsertSnowflakeSession(_ context.Context, _ types.WebSession) error {
	return trace.NotImplemented(notImplementedMessage)
}

// ResumeAuditStream resumes existing audit stream.
// This is a wrapper on the grpc endpoint and is deprecated.
// DELETE IN 7.0.0
func (c *Client) ResumeAuditStream(ctx context.Context, sid session.ID, uploadID string) (apievents.Stream, error) {
	return c.APIClient.ResumeAuditStream(ctx, string(sid), uploadID)
}

// CreateAuditStream creates new audit stream.
// This is a wrapper on the grpc endpoint and is deprecated.
// DELETE IN 7.0.0
func (c *Client) CreateAuditStream(ctx context.Context, sid session.ID) (apievents.Stream, error) {
	return c.APIClient.CreateAuditStream(ctx, string(sid))
}

// GetClusterAuditConfig gets cluster audit configuration.
func (c *Client) GetClusterAuditConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterAuditConfig, error) {
	return c.APIClient.GetClusterAuditConfig(ctx)
}

// GetClusterNetworkingConfig gets cluster networking configuration.
func (c *Client) GetClusterNetworkingConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterNetworkingConfig, error) {
	return c.APIClient.GetClusterNetworkingConfig(ctx)
}

// GetSessionRecordingConfig gets session recording configuration.
func (c *Client) GetSessionRecordingConfig(ctx context.Context, opts ...services.MarshalOption) (types.SessionRecordingConfig, error) {
	return c.APIClient.GetSessionRecordingConfig(ctx)
}

// GenerateCertAuthorityCRL generates an empty CRL for a CA.
func (c *Client) GenerateCertAuthorityCRL(ctx context.Context, caType types.CertAuthType) ([]byte, error) {
	resp, err := c.APIClient.GenerateCertAuthorityCRL(ctx, &proto.CertAuthorityRequest{Type: caType})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return resp.CRL, nil
}

// DeleteClusterNetworkingConfig not implemented: can only be called locally.
func (c *Client) DeleteClusterNetworkingConfig(ctx context.Context) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteSessionRecordingConfig not implemented: can only be called locally.
func (c *Client) DeleteSessionRecordingConfig(ctx context.Context) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAuthPreference not implemented: can only be called locally.
func (c *Client) DeleteAuthPreference(context.Context) error {
	return trace.NotImplemented(notImplementedMessage)
}

// SetClusterAuditConfig not implemented: can only be called locally.
func (c *Client) SetClusterAuditConfig(ctx context.Context, auditConfig types.ClusterAuditConfig) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteClusterAuditConfig not implemented: can only be called locally.
func (c *Client) DeleteClusterAuditConfig(ctx context.Context) error {
	return trace.NotImplemented(notImplementedMessage)
}

// DeleteAllLocks not implemented: can only be called locally.
func (c *Client) DeleteAllLocks(context.Context) error {
	return trace.NotImplemented(notImplementedMessage)
}

func (c *Client) UpdatePresence(ctx context.Context, sessionID, user string) error {
	return trace.NotImplemented(notImplementedMessage)
}

// WebService implements features used by Web UI clients
type WebService interface {
	// GetWebSessionInfo checks if a web session is valid, returns session id in case if
	// it is valid, or error otherwise.
	GetWebSessionInfo(ctx context.Context, user, sessionID string) (types.WebSession, error)
	// ExtendWebSession creates a new web session for a user based on another
	// valid web session
	ExtendWebSession(ctx context.Context, req WebSessionReq) (types.WebSession, error)
	// CreateWebSession creates a new web session for a user
	CreateWebSession(ctx context.Context, user string) (types.WebSession, error)

	// AppSession defines application session features.
	services.AppSession
	// SnowflakeSession defines Snowflake session features.
	services.SnowflakeSession
}

// IdentityService manages identities and users
type IdentityService interface {
	// UpsertOIDCConnector updates or creates OIDC connector
	UpsertOIDCConnector(ctx context.Context, connector types.OIDCConnector) error
	// GetOIDCConnector returns OIDC connector information by id
	GetOIDCConnector(ctx context.Context, id string, withSecrets bool) (types.OIDCConnector, error)
	// GetOIDCConnectors gets OIDC connectors list
	GetOIDCConnectors(ctx context.Context, withSecrets bool) ([]types.OIDCConnector, error)
	// DeleteOIDCConnector deletes OIDC connector by ID
	DeleteOIDCConnector(ctx context.Context, connectorID string) error
	// CreateOIDCAuthRequest creates OIDCAuthRequest
	CreateOIDCAuthRequest(ctx context.Context, req types.OIDCAuthRequest) (*types.OIDCAuthRequest, error)
	// GetOIDCAuthRequest returns OIDC auth request if found
	GetOIDCAuthRequest(ctx context.Context, id string) (*types.OIDCAuthRequest, error)
	// ValidateOIDCAuthCallback validates OIDC auth callback returned from redirect
	ValidateOIDCAuthCallback(ctx context.Context, q url.Values) (*OIDCAuthResponse, error)

	// UpsertSAMLConnector updates or creates SAML connector
	UpsertSAMLConnector(ctx context.Context, connector types.SAMLConnector) error
	// GetSAMLConnector returns SAML connector information by id
	GetSAMLConnector(ctx context.Context, id string, withSecrets bool) (types.SAMLConnector, error)
	// GetSAMLConnectors gets SAML connectors list
	GetSAMLConnectors(ctx context.Context, withSecrets bool) ([]types.SAMLConnector, error)
	// DeleteSAMLConnector deletes SAML connector by ID
	DeleteSAMLConnector(ctx context.Context, connectorID string) error
	// CreateSAMLAuthRequest creates SAML AuthnRequest
	CreateSAMLAuthRequest(ctx context.Context, req types.SAMLAuthRequest) (*types.SAMLAuthRequest, error)
	// ValidateSAMLResponse validates SAML auth response
	ValidateSAMLResponse(ctx context.Context, re string, connectorID string) (*SAMLAuthResponse, error)
	// GetSAMLAuthRequest returns SAML auth request if found
	GetSAMLAuthRequest(ctx context.Context, authRequestID string) (*types.SAMLAuthRequest, error)

	// UpsertGithubConnector creates or updates a Github connector
	UpsertGithubConnector(ctx context.Context, connector types.GithubConnector) error
	// GetGithubConnectors returns all configured Github connectors
	GetGithubConnectors(ctx context.Context, withSecrets bool) ([]types.GithubConnector, error)
	// GetGithubConnector returns the specified Github connector
	GetGithubConnector(ctx context.Context, id string, withSecrets bool) (types.GithubConnector, error)
	// DeleteGithubConnector deletes the specified Github connector
	DeleteGithubConnector(ctx context.Context, id string) error
	// CreateGithubAuthRequest creates a new request for Github OAuth2 flow
	CreateGithubAuthRequest(ctx context.Context, req types.GithubAuthRequest) (*types.GithubAuthRequest, error)
	// GetGithubAuthRequest returns Github auth request if found
	GetGithubAuthRequest(ctx context.Context, id string) (*types.GithubAuthRequest, error)
	// ValidateGithubAuthCallback validates Github auth callback
	ValidateGithubAuthCallback(ctx context.Context, q url.Values) (*GithubAuthResponse, error)

	// GetSSODiagnosticInfo returns SSO diagnostic info records.
	GetSSODiagnosticInfo(ctx context.Context, authKind string, authRequestID string) (*types.SSODiagnosticInfo, error)

	// GetUser returns user by name
	GetUser(name string, withSecrets bool) (types.User, error)

	// GetCurrentUser returns current user as seen by the server.
	// Useful especially in the context of remote clusters which perform role and trait mapping.
	GetCurrentUser(ctx context.Context) (types.User, error)

	// GetCurrentUserRoles returns current user's roles.
	GetCurrentUserRoles(ctx context.Context) ([]types.Role, error)

	// CreateUser inserts a new entry in a backend.
	CreateUser(ctx context.Context, user types.User) error

	// UpdateUser updates an existing user in a backend.
	UpdateUser(ctx context.Context, user types.User) error

	// UpsertUser user updates or inserts user entry
	UpsertUser(user types.User) error

	// CompareAndSwapUser updates an existing user in a backend, but fails if
	// the user in the backend does not match the expected value.
	CompareAndSwapUser(ctx context.Context, new, expected types.User) error

	// DeleteUser deletes an existng user in a backend by username.
	DeleteUser(ctx context.Context, user string) error

	// GetUsers returns a list of usernames registered in the system
	GetUsers(withSecrets bool) ([]types.User, error)

	// ChangePassword changes user password
	ChangePassword(req services.ChangePasswordReq) error

	// CheckPassword checks if the suplied web access password is valid.
	CheckPassword(user string, password []byte, otpToken string) error

	// GenerateToken creates a special provisioning token for a new SSH server
	// that is valid for ttl period seconds.
	//
	// This token is used by SSH server to authenticate with Auth server
	// and get signed certificate and private key from the auth server.
	//
	// If token is not supplied, it will be auto generated and returned.
	// If TTL is not supplied, token will be valid until removed.
	GenerateToken(ctx context.Context, req *proto.GenerateTokenRequest) (string, error)

	// GenerateHostCert takes the public key in the Open SSH ``authorized_keys``
	// plain text format, signs it using Host Certificate Authority private key and returns the
	// resulting certificate.
	GenerateHostCert(ctx context.Context, key []byte, hostID, nodeName string, principals []string, clusterName string, role types.SystemRole, ttl time.Duration) ([]byte, error)

	// GenerateUserCerts takes the public key in the OpenSSH `authorized_keys` plain
	// text format, signs it using User Certificate Authority signing key and
	// returns the resulting certificates.
	GenerateUserCerts(ctx context.Context, req proto.UserCertsRequest) (*proto.Certs, error)

	// GenerateUserSingleUseCerts is like GenerateUserCerts but issues a
	// certificate for a single session
	// (https://github.com/gravitational/teleport/blob/3a1cf9111c2698aede2056513337f32bfc16f1f1/rfd/0014-session-2FA.md#sessions).
	GenerateUserSingleUseCerts(ctx context.Context) (proto.AuthService_GenerateUserSingleUseCertsClient, error)

	// IsMFARequired is a request to check whether MFA is required to
	// access the Target.
	IsMFARequired(ctx context.Context, req *proto.IsMFARequiredRequest) (*proto.IsMFARequiredResponse, error)

	// DeleteAllUsers deletes all users
	DeleteAllUsers() error

	// CreateResetPasswordToken creates a new user reset token
	CreateResetPasswordToken(ctx context.Context, req CreateUserTokenRequest) (types.UserToken, error)

	// CreateBot creates a new certificate renewal bot and associated resources.
	CreateBot(ctx context.Context, req *proto.CreateBotRequest) (*proto.CreateBotResponse, error)
	// DeleteBot removes a certificate renewal bot and associated resources.
	DeleteBot(ctx context.Context, botName string) error
	// GetBotUsers gets all bot users.
	GetBotUsers(ctx context.Context) ([]types.User, error)

	// ChangeUserAuthentication allows a user with a reset or invite token to change their password and if enabled also adds a new mfa device.
	// Upon success, creates new web session and creates new set of recovery codes (if user meets requirements).
	ChangeUserAuthentication(ctx context.Context, req *proto.ChangeUserAuthenticationRequest) (*proto.ChangeUserAuthenticationResponse, error)

	// GetResetPasswordToken returns a reset password token.
	GetResetPasswordToken(ctx context.Context, username string) (types.UserToken, error)

	// GetMFADevices fetches all MFA devices registered for the calling user.
	GetMFADevices(ctx context.Context, in *proto.GetMFADevicesRequest) (*proto.GetMFADevicesResponse, error)
	// AddMFADevice adds a new MFA device for the calling user.
	AddMFADevice(ctx context.Context) (proto.AuthService_AddMFADeviceClient, error)
	// DeleteMFADevice deletes a MFA device for the calling user.
	DeleteMFADevice(ctx context.Context) (proto.AuthService_DeleteMFADeviceClient, error)
	// AddMFADeviceSync adds a new MFA device (nonstream).
	AddMFADeviceSync(ctx context.Context, req *proto.AddMFADeviceSyncRequest) (*proto.AddMFADeviceSyncResponse, error)
	// DeleteMFADeviceSync deletes a users MFA device (nonstream).
	DeleteMFADeviceSync(ctx context.Context, req *proto.DeleteMFADeviceSyncRequest) error
	// CreateAuthenticateChallenge creates and returns MFA challenges for a users registered MFA devices.
	CreateAuthenticateChallenge(ctx context.Context, req *proto.CreateAuthenticateChallengeRequest) (*proto.MFAAuthenticateChallenge, error)
	// CreateRegisterChallenge creates and returns MFA register challenge for a new MFA device.
	CreateRegisterChallenge(ctx context.Context, req *proto.CreateRegisterChallengeRequest) (*proto.MFARegisterChallenge, error)

	// MaintainSessionPresence establishes a channel used to continuously verify the presence for a session.
	MaintainSessionPresence(ctx context.Context) (proto.AuthService_MaintainSessionPresenceClient, error)

	// StartAccountRecovery creates a recovery start token for a user who successfully verified their username and their recovery code.
	// This token is used as part of a URL that will be emailed to the user (not done in this request).
	// Represents step 1 of the account recovery process.
	StartAccountRecovery(ctx context.Context, req *proto.StartAccountRecoveryRequest) (types.UserToken, error)
	// VerifyAccountRecovery creates a recovery approved token after successful verification of users password or second factor
	// (authn depending on what user needed to recover). This token will allow users to perform protected actions while not logged in.
	// Represents step 2 of the account recovery process after RPC StartAccountRecovery.
	VerifyAccountRecovery(ctx context.Context, req *proto.VerifyAccountRecoveryRequest) (types.UserToken, error)
	// CompleteAccountRecovery sets a new password or adds a new mfa device,
	// allowing user to regain access to their account using the new credentials.
	// Represents the last step in the account recovery process after RPC's StartAccountRecovery and VerifyAccountRecovery.
	CompleteAccountRecovery(ctx context.Context, req *proto.CompleteAccountRecoveryRequest) error

	// CreateAccountRecoveryCodes creates new set of recovery codes for a user, replacing and invalidating any previously owned codes.
	CreateAccountRecoveryCodes(ctx context.Context, req *proto.CreateAccountRecoveryCodesRequest) (*proto.RecoveryCodes, error)
	// GetAccountRecoveryToken returns a user token resource after verifying the token in
	// request is not expired and is of the correct recovery type.
	GetAccountRecoveryToken(ctx context.Context, req *proto.GetAccountRecoveryTokenRequest) (types.UserToken, error)
	// GetAccountRecoveryCodes returns the user in context their recovery codes resource without any secrets.
	GetAccountRecoveryCodes(ctx context.Context, req *proto.GetAccountRecoveryCodesRequest) (*proto.RecoveryCodes, error)

	// CreatePrivilegeToken creates a privilege token for the logged in user who has successfully re-authenticated with their second factor.
	// A privilege token allows users to perform privileged action eg: add/delete their MFA device.
	CreatePrivilegeToken(ctx context.Context, req *proto.CreatePrivilegeTokenRequest) (*types.UserTokenV3, error)
}

// ProvisioningService is a service in control
// of adding new nodes, auth servers and proxies to the cluster
type ProvisioningService interface {
	// GetTokens returns a list of active invitation tokens for nodes and users
	GetTokens(ctx context.Context) (tokens []types.ProvisionToken, err error)

	// GetToken returns provisioning token
	GetToken(ctx context.Context, token string) (types.ProvisionToken, error)

	// DeleteToken deletes a given provisioning token on the auth server (CA). It
	// could be a reset password token or a machine token
	DeleteToken(ctx context.Context, token string) error

	// DeleteAllTokens deletes all provisioning tokens
	DeleteAllTokens() error

	// UpsertToken adds provisioning tokens for the auth server
	UpsertToken(ctx context.Context, token types.ProvisionToken) error

	// CreateToken creates a new provision token for the auth server
	CreateToken(ctx context.Context, token types.ProvisionToken) error

	// RegisterUsingToken calls the auth service API to register a new node via registration token
	// which has been previously issued via GenerateToken
	RegisterUsingToken(ctx context.Context, req *types.RegisterUsingTokenRequest) (*proto.Certs, error)
}

// ClientI is a client to Auth service
type ClientI interface {
	IdentityService
	ProvisioningService
	services.Trust
	events.IAuditLog
	events.Streamer
	apievents.Emitter
	services.Presence
	services.Access
	services.DynamicAccess
	services.DynamicAccessOracle
	services.Restrictions
	services.Apps
	services.Databases
	services.Kubernetes
	services.WindowsDesktops
	WebService
	services.Status
	session.Service
	services.ClusterConfiguration
	services.SessionTrackerService
	services.ConnectionsDiagnostic
	types.Events

	types.WebSessionsGetter
	types.WebTokensGetter

	// NewKeepAliver returns a new instance of keep aliver
	NewKeepAliver(ctx context.Context) (types.KeepAliver, error)

	// RotateCertAuthority starts or restarts certificate authority rotation process.
	RotateCertAuthority(ctx context.Context, req RotateRequest) error

	// RotateExternalCertAuthority rotates external certificate authority,
	// this method is used to update only public keys and certificates of the
	// the certificate authorities of trusted clusters.
	RotateExternalCertAuthority(ctx context.Context, ca types.CertAuthority) error

	// ValidateTrustedCluster validates trusted cluster token with
	// main cluster, in case if validation is successful, main cluster
	// adds remote cluster
	ValidateTrustedCluster(context.Context, *ValidateTrustedClusterRequest) (*ValidateTrustedClusterResponse, error)

	// GetDomainName returns auth server cluster name
	GetDomainName(ctx context.Context) (string, error)

	// GetClusterCACert returns the PEM-encoded TLS certs for the local cluster.
	// If the cluster has multiple TLS certs, they will all be concatenated.
	GetClusterCACert(ctx context.Context) (*proto.GetClusterCACertResponse, error)

	// GenerateHostCerts generates new host certificates (signed
	// by the host certificate authority) for a node
	GenerateHostCerts(context.Context, *proto.HostCertsRequest) (*proto.Certs, error)
	// AuthenticateWebUser authenticates web user, creates and  returns web session
	// in case if authentication is successful
	AuthenticateWebUser(ctx context.Context, req AuthenticateUserRequest) (types.WebSession, error)
	// AuthenticateSSHUser authenticates SSH console user, creates and  returns a pair of signed TLS and SSH
	// short-lived certificates as a result
	AuthenticateSSHUser(ctx context.Context, req AuthenticateSSHRequest) (*SSHLoginResponse, error)

	// ProcessKubeCSR processes CSR request against Kubernetes CA, returns
	// signed certificate if successful.
	ProcessKubeCSR(req KubeCSR) (*KubeCSRResponse, error)

	// Ping gets basic info about the auth server.
	Ping(ctx context.Context) (proto.PingResponse, error)

	// CreateAppSession creates an application web session. Application web
	// sessions represent a browser session the client holds.
	CreateAppSession(context.Context, types.CreateAppSessionRequest) (types.WebSession, error)

	// CreateSnowflakeSession creates a Snowflake web session. Snowflake web
	// sessions represent Database Access Snowflake session the client holds.
	CreateSnowflakeSession(context.Context, types.CreateSnowflakeSessionRequest) (types.WebSession, error)

	// GenerateDatabaseCert generates client certificate used by a database
	// service to authenticate with the database instance.
	GenerateDatabaseCert(context.Context, *proto.DatabaseCertRequest) (*proto.DatabaseCertResponse, error)

	// GetWebSession queries the existing web session described with req.
	// Implements ReadAccessPoint.
	GetWebSession(ctx context.Context, req types.GetWebSessionRequest) (types.WebSession, error)

	// GetWebToken queries the existing web token described with req.
	// Implements ReadAccessPoint.
	GetWebToken(ctx context.Context, req types.GetWebTokenRequest) (types.WebToken, error)

	// ResetAuthPreference resets cluster auth preference to defaults.
	ResetAuthPreference(ctx context.Context) error

	// ResetClusterNetworkingConfig resets cluster networking configuration to defaults.
	ResetClusterNetworkingConfig(ctx context.Context) error

	// ResetSessionRecordingConfig resets session recording configuration to defaults.
	ResetSessionRecordingConfig(ctx context.Context) error

	// GenerateWindowsDesktopCert generates client smartcard certificate used
	// by an RDP client to authenticate with Windows.
	GenerateWindowsDesktopCert(context.Context, *proto.WindowsDesktopCertRequest) (*proto.WindowsDesktopCertResponse, error)
	// GenerateCertAuthorityCRL generates an empty CRL for a CA.
	GenerateCertAuthorityCRL(context.Context, types.CertAuthType) ([]byte, error)

	// GetInventoryStatus gets basic status info about instance inventory.
	GetInventoryStatus(ctx context.Context, req proto.InventoryStatusRequest) (proto.InventoryStatusSummary, error)

	// PingInventory attempts to trigger a downstream ping against a connected instance.
	PingInventory(ctx context.Context, req proto.InventoryPingRequest) (proto.InventoryPingResponse, error)

	// SubmitUsageEvent submits an external usage event.
	SubmitUsageEvent(ctx context.Context, req *proto.SubmitUsageEventRequest) error
}
