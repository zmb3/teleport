package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sync"

	"github.com/gravitational/roundtrip"
	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/trace"
	"google.golang.org/grpc"
)

// Client is HTTP Auth API client. It works by connecting to auth servers
// via HTTP.
//
// When Teleport servers connect to auth API, they usually establish an SSH
// tunnel first, and then do HTTP-over-SSH. This client is wrapped by auth.TunClient
// in lib/auth/tun.go
type Client struct {
	sync.Mutex
	ClientConfig
	roundtrip.Client
	transport  *http.Transport
	conn       *grpc.ClientConn
	grpcClient proto.AuthServiceClient
	// closedFlag is set to indicate that the services are closed
	closedFlag int32
}

// Make sure Client implements all the necessary methods.
var _ ClientI = &Client{}

// TLSConfig returns TLS config used by the client, could return nil
// if the client is not using TLS
func (c *Client) TLSConfig() *tls.Config {
	return c.ClientConfig.TLS
}

// New establishes a gRPC connection to an auth server.
func New() (*auth.Client, error) {
	tlsConfig, err := LoadTLSConfig("certs/api-admin.crt", "certs/api-admin.key", "certs/api-admin.cas")
	if err != nil {
		return nil, fmt.Errorf("Failed to setup TLS config: %v", err)
	}

	// replace 127.0.0.1:3025 (default) with your auth server address
	authServerAddr := utils.MustParseAddrList("127.0.0.1:3025")
	clientConfig := Config{Addrs: authServerAddr, TLS: tlsConfig}

	client, err := NewTLSClient(clientConfig)
	return client, nil
}

// Config contains configuration of the client
type Config struct {
	// Addrs is a list of addresses to dial
	Addrs []utils.NetAddr
	// Dialer is a custom dialer, if provided
	// is used instead of the list of addresses
	Dialer ContextDialer
	// TLS is a TLS config
	TLS *tls.Config
}

// CheckAndSetDefaults checks and sets default config values
func (c *Config) CheckAndSetDefaults() error {
	if len(c.Addrs) == 0 && c.Dialer == nil {
		return trace.BadParameter("set parameter Addrs or DialContext")
	}
	if c.TLS == nil {
		return trace.BadParameter("missing parameter TLS")
	}
	if c.KeepAlivePeriod == 0 {
		c.KeepAlivePeriod = defaults.ServerKeepAliveTTL
	}
	if c.KeepAliveCount == 0 {
		c.KeepAliveCount = defaults.KeepAliveCountMax
	}
	if c.Dialer == nil {
		c.Dialer = NewAddrDialer(c.Addrs, c.KeepAlivePeriod)
	}
	if c.TLS.ServerName == "" {
		c.TLS.ServerName = teleport.APIDomain
	}
	// this logic is necessary to force client to always send certificate
	// regardless of the server setting, otherwise client may pick
	// not to send the client certificate by looking at certificate request
	if len(c.TLS.Certificates) != 0 {
		cert := c.TLS.Certificates[0]
		c.TLS.Certificates = nil
		c.TLS.GetClientCertificate = func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}

	return nil
}

// NewTLSClient returns a new TLS client that uses mutual TLS authentication
// and dials the remote server using dialer
func NewTLSClient(cfg Config, params ...roundtrip.ClientParam) (*Client, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	transport := &http.Transport{
		// notice that below roundtrip.Client is passed
		// teleport.APIEndpoint as an address for the API server, this is
		// to make sure client verifies the DNS name of the API server
		// custom DialContext overrides this DNS name to the real address
		// in addition this dialer tries multiple adresses if provided
		DialContext:           cfg.Dialer.DialContext,
		ResponseHeaderTimeout: defaults.DefaultDialTimeout,
		TLSClientConfig:       cfg.TLS,

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

	clientParams := append(
		[]roundtrip.ClientParam{
			roundtrip.HTTPClient(&http.Client{Transport: transport}),
			roundtrip.SanitizerEnabled(true),
		},
		params...,
	)
	roundtripClient, err := roundtrip.NewClient("https://"+teleport.APIDomain, CurrentVersion, clientParams...)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &Client{
		ClientConfig: cfg,
		Client:       *roundtripClient,
		transport:    transport,
	}, nil
}

// TLSCreds loads creds from TLS config
func TLSCreds(cfg *tls.Config) {
	// TODO
}

// PathCreds loads mounted creds from path, detects reloads and updates the grpc transport
func PathCreds() {
	// TODO
}

// LoadTSHConfig loads creds from TSH config file
// $ tsh login
// # try client
// $ go run main.go
func LoadTSHConfig() {
	// TODO
}

// LoadTLSConfig loads and sets up client TLS config for authentication
func LoadTLSConfig(certPath, keyPath, rootCAsPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caPool, err := LoadTLSCertPool(rootCAsPath)
	if err != nil {
		return nil, err
	}
	conf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
	}
	return conf, nil
}

// LoadTLSCertPool is used to load root CA certs from file path.
func LoadTLSCertPool(path string) (*x509.CertPool, error) {
	caFile, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	caCerts, err := ioutil.ReadAll(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caCerts); !ok {
		return nil, fmt.Errorf("invalid CA cert PEM")
	}
	return pool, nil
}
