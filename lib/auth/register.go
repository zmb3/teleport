/*
Copyright 2015 Gravitational, Inc.

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
	"crypto/x509"
	"os"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/metadata"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib"
	"github.com/zmb3/teleport/lib/auth/native"
	"github.com/zmb3/teleport/lib/circleci"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/githubactions"
	"github.com/zmb3/teleport/lib/srv/alpnproxy/common"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

// LocalRegister is used to generate host keys when a node or proxy is running
// within the same process as the Auth Server and as such, does not need to
// use provisioning tokens.
func LocalRegister(id IdentityID, authServer *Server, additionalPrincipals, dnsNames []string, remoteAddr string, systemRoles []types.SystemRole) (*Identity, error) {
	priv, pub, err := native.GenerateKeyPair()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	tlsPub, err := PrivateKeyToPublicKeyTLS(priv)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// If local registration is happening and no remote address was passed in
	// (which means no advertise IP was set), use localhost.
	if remoteAddr == "" {
		remoteAddr = defaults.Localhost
	}
	certs, err := authServer.GenerateHostCerts(context.Background(),
		&proto.HostCertsRequest{
			HostID:               id.HostUUID,
			NodeName:             id.NodeName,
			Role:                 id.Role,
			AdditionalPrincipals: additionalPrincipals,
			RemoteAddr:           remoteAddr,
			DNSNames:             dnsNames,
			NoCache:              true,
			PublicSSHKey:         pub,
			PublicTLSKey:         tlsPub,
			SystemRoles:          systemRoles,
		})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	identity, err := ReadIdentityFromKeyPair(priv, certs)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return identity, nil
}

// RegisterParams specifies parameters
// for first time register operation with auth server
type RegisterParams struct {
	// Token is a secure token to join the cluster
	Token string
	// ID is identity ID
	ID IdentityID
	// AuthServers is a list of auth servers to dial
	AuthServers []utils.NetAddr
	// ProxyServer is a proxy server to dial
	ProxyServer utils.NetAddr
	// AdditionalPrincipals is a list of additional principals to dial
	AdditionalPrincipals []string
	// DNSNames is a list of DNS names to add to x509 certificate
	DNSNames []string
	// PublicTLSKey is a server's public key to sign
	PublicTLSKey []byte
	// PublicSSHKey is a server's public SSH key to sign
	PublicSSHKey []byte
	// CipherSuites is a list of cipher suites to use for TLS client connection
	CipherSuites []uint16
	// CAPins are the SKPI hashes of the CAs used to verify the Auth Server.
	CAPins []string
	// CAPath is the path to the CA file.
	CAPath string
	// GetHostCredentials is a client that can fetch host credentials.
	GetHostCredentials HostCredentials
	// Clock specifies the time provider. Will be used to override the time anchor
	// for TLS certificate verification.
	// Defaults to real clock if unspecified
	Clock clockwork.Clock
	// JoinMethod is the joining method used for this register request.
	JoinMethod types.JoinMethod
	// ec2IdentityDocument is used for Simplified Node Joining to prove the
	// identity of a joining EC2 instance.
	ec2IdentityDocument []byte
	// CircuitBreakerConfig defines how the circuit breaker should behave.
	CircuitBreakerConfig breaker.Config
	// FIPS means FedRAMP/FIPS 140-2 compliant configuration was requested.
	FIPS bool
	// IDToken is a token retrieved from a workload identity provider for
	// certain join types e.g GitHub, Google.
	IDToken string
	// Expires is an optional field for bots that specifies a time that the
	// certificates that are returned by registering should expire at.
	// It should not be specified for non-bot registrations.
	Expires *time.Time
}

func (r *RegisterParams) checkAndSetDefaults() error {
	if r.Clock == nil {
		r.Clock = clockwork.NewRealClock()
	}

	if err := r.verifyAuthOrProxyAddress(); err != nil {
		return trace.BadParameter("no auth or proxy servers set")
	}

	return nil
}

func (r *RegisterParams) verifyAuthOrProxyAddress() error {
	haveAuthServers := len(r.AuthServers) > 0
	haveProxyServer := !r.ProxyServer.IsEmpty()

	if !haveAuthServers && !haveProxyServer {
		return trace.BadParameter("no auth or proxy servers set")
	}

	if haveAuthServers && haveProxyServer {
		return trace.BadParameter("only one of auth or proxy server should be set")
	}

	return nil
}

// CredGetter is an interface for a client that can be used to get host
// credentials. This interface is needed because lib/client can not be imported
// in lib/auth due to circular imports.
type HostCredentials func(context.Context, string, bool, types.RegisterUsingTokenRequest) (*proto.Certs, error)

// Register is used to generate host keys when a node or proxy are running on
// different hosts than the auth server. This method requires provisioning
// tokens to prove a valid auth server was used to issue the joining request
// as well as a method for the node to validate the auth server.
func Register(params RegisterParams) (*proto.Certs, error) {
	ctx := context.TODO()
	if err := params.checkAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	// Read in the token. The token can either be passed in or come from a file
	// on disk.
	token, err := utils.TryReadValueAsFile(params.Token)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// add EC2 Identity Document to params if required for given join method
	if params.JoinMethod == types.JoinMethodEC2 {
		if !utils.IsEC2NodeID(params.ID.HostUUID) {
			return nil, trace.BadParameter(
				`Host ID %q is not valid when using the EC2 join method, `+
					`try removing the "host_uuid" file in your teleport data dir `+
					`(e.g. /var/lib/teleport/host_uuid)`,
				params.ID.HostUUID)
		}
		params.ec2IdentityDocument, err = utils.GetEC2IdentityDocument()
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else if params.JoinMethod == types.JoinMethodGitHub {
		params.IDToken, err = githubactions.NewIDTokenSource().GetIDToken(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else if params.JoinMethod == types.JoinMethodCircleCI {
		params.IDToken, err = circleci.GetIDToken(os.Getenv)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	type registerMethod struct {
		call func(token string, params RegisterParams) (*proto.Certs, error)
		desc string
	}

	registerThroughAuth := registerMethod{registerThroughAuth, "with auth server"}
	registerThroughProxy := registerMethod{registerThroughProxy, "via proxy server"}

	registerMethods := []registerMethod{registerThroughAuth, registerThroughProxy}

	if !params.ProxyServer.IsEmpty() {
		log.WithField("proxy-server", params.ProxyServer).Debugf("Registering node to the cluster.")

		registerMethods = []registerMethod{registerThroughProxy}

		if proxyServerIsAuth(params.ProxyServer) {
			log.Debugf("The specified proxy server appears to be an auth server.")
		}
	} else {
		log.WithField("auth-servers", params.AuthServers).Debugf("Registering node to the cluster.")

		if params.GetHostCredentials == nil {
			log.Debugf("Missing client, it is not possible to register through proxy.")
			registerMethods = []registerMethod{registerThroughAuth}
		} else if authServerIsProxy(params.AuthServers) {
			log.Debugf("The first specified auth server appears to be a proxy.")
			registerMethods = []registerMethod{registerThroughProxy, registerThroughAuth}
		}
	}

	var collectedErrs []error
	for _, method := range registerMethods {
		log.Infof("Attempting registration %s.", method.desc)
		certs, err := method.call(token, params)
		if err != nil {
			collectedErrs = append(collectedErrs, err)
			log.WithError(err).Debugf("Registration %s failed.", method.desc)
			continue
		}
		log.Infof("Successfully registered %s.", method.desc)
		return certs, nil
	}
	return nil, trace.NewAggregate(collectedErrs...)
}

// authServerIsProxy returns true if the first specified auth server
// to register with appears to be a proxy.
func authServerIsProxy(servers []utils.NetAddr) bool {
	if len(servers) == 0 {
		return false
	}
	port := servers[0].Port(0)
	return port == defaults.HTTPListenPort || port == teleport.StandardHTTPSPort
}

// proxyServerIsAuth returns true if the address given to register with
// appears to be an auth server.
func proxyServerIsAuth(server utils.NetAddr) bool {
	port := server.Port(0)
	return port == defaults.AuthListenPort
}

// registerThroughProxy is used to register through the proxy server.
func registerThroughProxy(token string, params RegisterParams) (*proto.Certs, error) {
	var certs *proto.Certs
	if params.JoinMethod == types.JoinMethodIAM {
		// IAM join method requires gRPC client
		conn, err := proxyJoinServiceConn(params)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		defer conn.Close()

		joinServiceClient := client.NewJoinServiceClient(proto.NewJoinServiceClient(conn))
		certs, err = registerUsingIAMMethod(joinServiceClient, token, params)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		// non-IAM join methods use GetHostCredentials function passed through
		// params to call proxy HTTP endpoint
		var err error
		certs, err = params.GetHostCredentials(context.Background(),
			getHostAddresses(params)[0],
			lib.IsInsecureDevMode(),
			types.RegisterUsingTokenRequest{
				Token:                token,
				HostID:               params.ID.HostUUID,
				NodeName:             params.ID.NodeName,
				Role:                 params.ID.Role,
				AdditionalPrincipals: params.AdditionalPrincipals,
				DNSNames:             params.DNSNames,
				PublicTLSKey:         params.PublicTLSKey,
				PublicSSHKey:         params.PublicSSHKey,
				EC2IdentityDocument:  params.ec2IdentityDocument,
				IDToken:              params.IDToken,
				Expires:              params.Expires,
			})
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	return certs, nil
}

func getHostAddresses(params RegisterParams) []string {
	if !params.ProxyServer.IsEmpty() {
		return []string{params.ProxyServer.String()}
	}

	return utils.NetAddrsToStrings(params.AuthServers)
}

// registerThroughAuth is used to register through the auth server.
func registerThroughAuth(token string, params RegisterParams) (*proto.Certs, error) {
	var client *Client
	var err error

	// Build a client to the Auth Server. If a CA pin is specified require the
	// Auth Server is validated. Otherwise attempt to use the CA file on disk
	// but if it's not available connect without validating the Auth Server CA.
	switch {
	case len(params.CAPins) != 0:
		client, err = pinRegisterClient(params)
	default:
		client, err = insecureRegisterClient(params)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer client.Close()

	var certs *proto.Certs
	if params.JoinMethod == types.JoinMethodIAM {
		// IAM method uses unique gRPC endpoint
		certs, err = registerUsingIAMMethod(client, token, params)
	} else {
		// non-IAM join methods use HTTP endpoint
		// Get the SSH and X509 certificates for a node.
		certs, err = client.RegisterUsingToken(
			context.Background(),
			&types.RegisterUsingTokenRequest{
				Token:                token,
				HostID:               params.ID.HostUUID,
				NodeName:             params.ID.NodeName,
				Role:                 params.ID.Role,
				AdditionalPrincipals: params.AdditionalPrincipals,
				DNSNames:             params.DNSNames,
				PublicTLSKey:         params.PublicTLSKey,
				PublicSSHKey:         params.PublicSSHKey,
				EC2IdentityDocument:  params.ec2IdentityDocument,
				IDToken:              params.IDToken,
				Expires:              params.Expires,
			})
	}
	return certs, trace.Wrap(err)
}

// proxyJoinServiceConn attempts to connect to the join service running on the
// proxy. The Proxy's TLS cert will be verified using the host's root CA pool
// (PKI) unless the --insecure flag was passed.
func proxyJoinServiceConn(params RegisterParams) (*grpc.ClientConn, error) {
	tlsConfig := utils.TLSConfig(params.CipherSuites)
	tlsConfig.Time = params.Clock.Now
	// set NextProtos for TLS routing, the actual protocol will be h2
	tlsConfig.NextProtos = []string{string(common.ProtocolProxyGRPC), http2.NextProtoTLS}

	if lib.IsInsecureDevMode() {
		tlsConfig.InsecureSkipVerify = true
		log.Warnf("Joining cluster without validating the identity of the Proxy Server.")
	}

	conn, err := grpc.Dial(
		getHostAddresses(params)[0],
		grpc.WithUnaryInterceptor(metadata.UnaryClientInterceptor),
		grpc.WithStreamInterceptor(metadata.StreamClientInterceptor),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)
	return conn, trace.Wrap(err)
}

// insecureRegisterClient attempts to connects to the Auth Server using the
// CA on disk. If no CA is found on disk, Teleport will not verify the Auth
// Server it is connecting to.
func insecureRegisterClient(params RegisterParams) (*Client, error) {
	tlsConfig := utils.TLSConfig(params.CipherSuites)
	tlsConfig.Time = params.Clock.Now

	cert, err := readCA(params.CAPath)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}

	// If no CA was found, then create a insecure connection to the Auth Server,
	// otherwise use the CA on disk to validate the Auth Server.
	if trace.IsNotFound(err) {
		tlsConfig.InsecureSkipVerify = true

		log.Warnf("Joining cluster without validating the identity of the Auth " +
			"Server. This may open you up to a Man-In-The-Middle (MITM) attack if an " +
			"attacker can gain privileged network access. To remedy this, use the CA pin " +
			"value provided when join token was generated to validate the identity of " +
			"the Auth Server.")
	} else {
		certPool := x509.NewCertPool()
		certPool.AddCert(cert)
		tlsConfig.RootCAs = certPool

		log.Infof("Joining remote cluster %v, validating connection with certificate on disk.", cert.Subject.CommonName)
	}

	client, err := NewClient(client.Config{
		Addrs: getHostAddresses(params),
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
		CircuitBreakerConfig: params.CircuitBreakerConfig,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return client, nil
}

// readCA will read in CA that will be used to validate the certificate that
// the Auth Server presents.
func readCA(path string) (*x509.Certificate, error) {
	certBytes, err := utils.ReadPath(path)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cert, err := tlsca.ParseCertificatePEM(certBytes)
	if err != nil {
		return nil, trace.Wrap(err, "failed to parse certificate at %v", path)
	}
	return cert, nil
}

// pinRegisterClient first connects to the Auth Server using a insecure
// connection to fetch the root CA. If the root CA matches the provided CA
// pin, a connection will be re-established and the root CA will be used to
// validate the certificate presented. If both conditions hold true, then we
// know we are connecting to the expected Auth Server.
func pinRegisterClient(params RegisterParams) (*Client, error) {
	// Build a insecure client to the Auth Server. This is safe because even if
	// an attacker were to MITM this connection the CA pin will not match below.
	tlsConfig := utils.TLSConfig(params.CipherSuites)
	tlsConfig.InsecureSkipVerify = true
	tlsConfig.Time = params.Clock.Now
	authClient, err := NewClient(client.Config{
		Addrs: getHostAddresses(params),
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
		CircuitBreakerConfig: params.CircuitBreakerConfig,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer authClient.Close()

	// Fetch the root CA from the Auth Server. The NOP role has access to the
	// GetClusterCACert endpoint.
	localCA, err := authClient.GetClusterCACert(context.TODO())
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certs, err := tlsca.ParseCertificatePEMs(localCA.TLSCA)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Check that the SPKI pin matches the CA we fetched over a insecure
	// connection. This makes sure the CA fetched over a insecure connection is
	// in-fact the expected CA.
	err = utils.CheckSPKI(params.CAPins, certs)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	for _, cert := range certs {
		// Check that the fetched CA is valid at the current time.
		err = utils.VerifyCertificateExpiry(cert, params.Clock)
		if err != nil {
			return nil, trace.Wrap(err)
		}

	}
	log.Infof("Joining remote cluster %v with CA pin.", certs[0].Subject.CommonName)

	// Create another client, but this time with the CA provided to validate
	// that the Auth Server was issued a certificate by the same CA.
	tlsConfig = utils.TLSConfig(params.CipherSuites)
	tlsConfig.Time = params.Clock.Now
	certPool := x509.NewCertPool()
	for _, cert := range certs {
		certPool.AddCert(cert)
	}
	tlsConfig.RootCAs = certPool

	authClient, err = NewClient(client.Config{
		Addrs: getHostAddresses(params),
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
		CircuitBreakerConfig: params.CircuitBreakerConfig,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return authClient, nil
}

type joinServiceClient interface {
	RegisterUsingIAMMethod(ctx context.Context, challengeResponse client.RegisterChallengeResponseFunc) (*proto.Certs, error)
}

// registerUsingIAMMethod is used to register using the IAM join method. It is
// able to register through a proxy or through the auth server directly.
func registerUsingIAMMethod(joinServiceClient joinServiceClient, token string, params RegisterParams) (*proto.Certs, error) {
	ctx := context.Background()

	// Attempt to use the regional STS endpoint, fall back to using the global
	// endpoint. The regional endpoint may fail if Auth is on an older version
	// which does not support regional endpoints, the STS service is not
	// enabled in the current region, or an unknown AWS region is configured.
	var errs []error
	for _, s := range []struct {
		desc string
		opts []stsIdentityRequestOption
	}{
		{
			desc: "regional",
			opts: []stsIdentityRequestOption{
				withFIPSEndpoint(params.FIPS),
				withRegionalEndpoint(true),
			},
		},
		{
			// DELETE IN 12.0, global endpoint does not support China or
			// GovCloud or FIPS, is only a fallback for connecting to an auth
			// server on an older version which does not support regional
			// endpoints.
			desc: "global",
			opts: []stsIdentityRequestOption{
				withFIPSEndpoint(false),
				withRegionalEndpoint(false),
			},
		},
	} {
		log.Infof("Attempting to register %s with IAM method using %s STS endpoint", params.ID.Role, s.desc)
		// Call RegisterUsingIAMMethod and pass a callback to respond to the challenge with a signed join request.
		certs, err := joinServiceClient.RegisterUsingIAMMethod(ctx, func(challenge string) (*proto.RegisterUsingIAMMethodRequest, error) {
			// create the signed sts:GetCallerIdentity request and include the challenge
			signedRequest, err := createSignedSTSIdentityRequest(ctx, challenge, s.opts...)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			// send the register request including the challenge response
			return &proto.RegisterUsingIAMMethodRequest{
				RegisterUsingTokenRequest: &types.RegisterUsingTokenRequest{
					Token:                token,
					HostID:               params.ID.HostUUID,
					NodeName:             params.ID.NodeName,
					Role:                 params.ID.Role,
					AdditionalPrincipals: params.AdditionalPrincipals,
					DNSNames:             params.DNSNames,
					PublicTLSKey:         params.PublicTLSKey,
					PublicSSHKey:         params.PublicSSHKey,
				},
				StsIdentityRequest: signedRequest,
			}, nil
		})
		if err != nil {
			log.WithError(err).Infof("Failed to register %s using %s STS endpoint", params.ID.Role, s.desc)
			errs = append(errs, err)
		} else {
			log.Infof("Successfully registered %s with IAM method using %s STS endpoint", params.ID.Role, s.desc)
			return certs, nil
		}
	}

	return nil, trace.NewAggregate(errs...)
}

// ReRegisterParams specifies parameters for re-registering
// in the cluster (rotating certificates for existing members)
type ReRegisterParams struct {
	// Client is an authenticated client using old credentials
	Client ClientI
	// ID is identity ID
	ID IdentityID
	// AdditionalPrincipals is a list of additional principals to dial
	AdditionalPrincipals []string
	// DNSNames is a list of DNS Names to add to the x509 client certificate
	DNSNames []string
	// PrivateKey is a PEM encoded private key (not passed to auth servers)
	PrivateKey []byte
	// PublicTLSKey is a server's public key to sign
	PublicTLSKey []byte
	// PublicSSHKey is a server's public SSH key to sign
	PublicSSHKey []byte
	// Rotation is the rotation state of the certificate authority
	Rotation types.Rotation
	// SystemRoles is a set of additional system roles held by the instance.
	SystemRoles []types.SystemRole
	// Used by older instances to requisition a multi-role cert by individually
	// proving which system roles are held.
	UnstableSystemRoleAssertionID string
}

// ReRegister renews the certificates and private keys based on the client's existing identity.
func ReRegister(params ReRegisterParams) (*Identity, error) {
	var rotation *types.Rotation
	if !params.Rotation.IsZero() {
		// older auths didn't distinguish between empty and nil rotation
		// structs, so we go out of our way to only send non-nil rotation
		// if it is truly non-empty.
		rotation = &params.Rotation
	}
	certs, err := params.Client.GenerateHostCerts(context.Background(),
		&proto.HostCertsRequest{
			HostID:                        params.ID.HostID(),
			NodeName:                      params.ID.NodeName,
			Role:                          params.ID.Role,
			AdditionalPrincipals:          params.AdditionalPrincipals,
			DNSNames:                      params.DNSNames,
			PublicTLSKey:                  params.PublicTLSKey,
			PublicSSHKey:                  params.PublicSSHKey,
			Rotation:                      rotation,
			SystemRoles:                   params.SystemRoles,
			UnstableSystemRoleAssertionID: params.UnstableSystemRoleAssertionID,
		})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return ReadIdentityFromKeyPair(params.PrivateKey, certs)
}
