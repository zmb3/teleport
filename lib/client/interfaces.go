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

package client

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/identityfile"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/native"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

// KeyIndex helps to identify a key in the store.
type KeyIndex struct {
	// ProxyHost is the root proxy hostname that a key is associated with.
	ProxyHost string
	// Username is the username that a key is associated with.
	Username string
	// ClusterName is the cluster name that a key is associated with.
	ClusterName string
}

// Check verifies the KeyIndex is fully specified.
func (idx KeyIndex) Check() error {
	missingField := "key index field %s is not set"
	if idx.ProxyHost == "" {
		return trace.BadParameter(missingField, "ProxyHost")
	}
	if idx.Username == "" {
		return trace.BadParameter(missingField, "Username")
	}
	if idx.ClusterName == "" {
		return trace.BadParameter(missingField, "ClusterName")
	}
	return nil
}

// Key describes a complete (signed) client key
type Key struct {
	KeyIndex

	// PrivateKey is a private key used for cryptographical operations.
	*keys.PrivateKey

	// Cert is an SSH client certificate
	Cert []byte `json:"Cert,omitempty"`
	// TLSCert is a PEM encoded client TLS x509 certificate.
	// It's used to authenticate to the Teleport APIs.
	TLSCert []byte `json:"TLSCert,omitempty"`
	// KubeTLSCerts are TLS certificates (PEM-encoded) for individual
	// kubernetes clusters. Map key is a kubernetes cluster name.
	KubeTLSCerts map[string][]byte `json:"KubeCerts,omitempty"`
	// DBTLSCerts are PEM-encoded TLS certificates for database access.
	// Map key is the database service name.
	DBTLSCerts map[string][]byte `json:"DBCerts,omitempty"`
	// AppTLSCerts are TLS certificates for application access.
	// Map key is the application name.
	AppTLSCerts map[string][]byte `json:"AppCerts,omitempty"`
	// WindowsDesktopCerts are TLS certificates for Windows Desktop access.
	// Map key is the desktop server name.
	WindowsDesktopCerts map[string][]byte `json:"WindowsDesktopCerts,omitempty"`
	// TrustedCA is a list of trusted certificate authorities
	TrustedCA []auth.TrustedCerts
}

// GenerateRSAKey generates a new unsigned key.
func GenerateRSAKey() (*Key, error) {
	priv, err := native.GeneratePrivateKey()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return NewKey(priv), nil
}

// NewKey creates a new Key for the given private key.
func NewKey(priv *keys.PrivateKey) *Key {
	return &Key{
		PrivateKey:          priv,
		KubeTLSCerts:        make(map[string][]byte),
		DBTLSCerts:          make(map[string][]byte),
		AppTLSCerts:         make(map[string][]byte),
		WindowsDesktopCerts: make(map[string][]byte),
	}
}

// extractIdentityFromCert parses a tlsca.Identity from raw PEM cert bytes.
func extractIdentityFromCert(certBytes []byte) (*tlsca.Identity, error) {
	cert, err := tlsca.ParseCertificatePEM(certBytes)
	if err != nil {
		return nil, trace.Wrap(err, "failed to parse TLS certificate")
	}

	parsed, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return parsed, nil
}

// KeyFromIdentityFile loads the private key + certificate
// from an identity file into a Key.
func KeyFromIdentityFile(path string) (*Key, error) {
	ident, err := identityfile.ReadFile(path)
	if err != nil {
		return nil, trace.Wrap(err, "failed to parse identity file")
	}

	priv, err := keys.ParsePrivateKey(ident.PrivateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	dbTLSCerts := make(map[string][]byte)
	appCerts := make(map[string][]byte)

	// validate TLS Cert (if present):
	if len(ident.Certs.TLS) > 0 {
		if _, err := priv.TLSCertificate(ident.Certs.TLS); err != nil {
			return nil, trace.Wrap(err)
		}

		parsedIdent, err := extractIdentityFromCert(ident.Certs.TLS)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		// If this identity file has any database certs, copy it into the DBTLSCerts map.
		if parsedIdent.RouteToDatabase.ServiceName != "" {
			dbTLSCerts[parsedIdent.RouteToDatabase.ServiceName] = ident.Certs.TLS
		}

		// Similarly, if this identity has any app certs, copy them in.
		if parsedIdent.RouteToApp.Name != "" {
			appCerts[parsedIdent.RouteToApp.Name] = ident.Certs.TLS
		}

	}

	// Validate TLS CA certs (if present).
	var trustedCA []auth.TrustedCerts
	if len(ident.CACerts.TLS) > 0 || len(ident.CACerts.SSH) > 0 {
		trustedCA = []auth.TrustedCerts{{
			TLSCertificates:  ident.CACerts.TLS,
			HostCertificates: ident.CACerts.SSH,
		}}

		pool := x509.NewCertPool()
		for i, certPEM := range ident.CACerts.TLS {
			if !pool.AppendCertsFromPEM(certPEM) {
				return nil, trace.BadParameter("identity file contains invalid TLS CA cert (#%v)", i+1)
			}
		}

		for _, caCert := range ident.CACerts.SSH {
			if _, _, _, _, _, err := ssh.ParseKnownHosts(caCert); err != nil {
				return nil, trace.BadParameter("CA cert parsing error: %v; make sure this identity file was generated by 'tsh login -o' or 'tctl auth sign --format=file' or try generating it again", err.Error())
			}
		}
	}

	return &Key{
		PrivateKey:  priv,
		Cert:        ident.Certs.SSH,
		TLSCert:     ident.Certs.TLS,
		TrustedCA:   trustedCA,
		DBTLSCerts:  dbTLSCerts,
		AppTLSCerts: appCerts,
	}, nil
}

// RootClusterCAs returns root cluster CAs.
func (k *Key) RootClusterCAs() ([][]byte, error) {
	rootClusterName, err := k.RootClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var out [][]byte
	for _, cas := range k.TrustedCA {
		for _, v := range cas.TLSCertificates {
			cert, err := tlsca.ParseCertificatePEM(v)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			if cert.Subject.CommonName == rootClusterName {
				out = append(out, v)
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	return nil, trace.NotFound("failed to find TLS CA for %q root cluster", rootClusterName)
}

// TLSCAs returns all TLS CA certificates from this key
func (k *Key) TLSCAs() (result [][]byte) {
	for _, ca := range k.TrustedCA {
		result = append(result, ca.TLSCertificates...)
	}
	return result
}

func (k *Key) KubeClientTLSConfig(cipherSuites []uint16, kubeClusterName string) (*tls.Config, error) {
	rootCluster, err := k.RootClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsCert, ok := k.KubeTLSCerts[kubeClusterName]
	if !ok {
		return nil, trace.NotFound("TLS certificate for kubernetes cluster %q not found", kubeClusterName)
	}

	tlsConfig, err := k.clientTLSConfig(cipherSuites, tlsCert, []string{rootCluster})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsConfig.ServerName = fmt.Sprintf("%s%s", constants.KubeSNIPrefix, constants.APIDomain)
	return tlsConfig, nil
}

// SSHCAs returns all SSH CA certificates from this key
func (k *Key) SSHCAs() (result [][]byte) {
	for _, ca := range k.TrustedCA {
		result = append(result, ca.HostCertificates...)
	}
	return result
}

// SSHCAsForClusters returns SSH CA for particular clusters.
func (k *Key) SSHCAsForClusters(clusters []string) (result [][]byte, err error) {
	for _, ca := range k.TrustedCA {
		for _, hc := range ca.HostCertificates {
			_, hosts, _, _, _, err := ssh.ParseKnownHosts(hc)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			for _, h := range hosts {
				for _, c := range clusters {
					if h == c {
						result = append(result, hc)
					}
				}
			}
		}
	}
	return result, nil
}

// GetClusterNames gets the names of clusters this key has CAs for.
func (k *Key) GetClusterNames() ([]string, error) {
	var clusters []string
	for _, ca := range k.TrustedCA {
		for _, hc := range ca.HostCertificates {
			_, hosts, _, _, _, err := ssh.ParseKnownHosts(hc)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			clusters = append(clusters, hosts...)
		}
	}
	return apiutils.Deduplicate(clusters), nil
}

// TeleportClientTLSConfig returns client TLS configuration used
// to authenticate against API servers.
func (k *Key) TeleportClientTLSConfig(cipherSuites []uint16, clusters []string) (*tls.Config, error) {
	return k.clientTLSConfig(cipherSuites, k.TLSCert, clusters)
}

func (k *Key) clientTLSConfig(cipherSuites []uint16, tlsCertRaw []byte, clusters []string) (*tls.Config, error) {
	tlsCert, err := k.TLSCertificate(tlsCertRaw)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	pool, err := k.clientCertPool(clusters...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	tlsConfig := utils.TLSConfig(cipherSuites)
	tlsConfig.RootCAs = pool
	tlsConfig.Certificates = append(tlsConfig.Certificates, tlsCert)
	// Use Issuer CN from the certificate to populate the correct SNI in
	// requests.
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, trace.Wrap(err, "failed to parse TLS cert")
	}
	tlsConfig.ServerName = apiutils.EncodeClusterName(leaf.Issuer.CommonName)
	return tlsConfig, nil
}

// ClientCertPool returns x509.CertPool containing trusted CA.
func (k *Key) clientCertPool(clusters ...string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, caPEM := range k.TLSCAs() {
		cert, err := tlsca.ParseCertificatePEM(caPEM)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		for _, k := range clusters {
			if cert.Subject.CommonName == k {
				if !pool.AppendCertsFromPEM(caPEM) {
					return nil, trace.BadParameter("failed to parse TLS CA certificate")
				}
			}
		}
	}
	return pool, nil
}

// ProxyClientSSHConfig returns an ssh.ClientConfig with SSH credentials from this
// Key and HostKeyCallback matching SSH CAs in the Key.
//
// The config is set up to authenticate to proxy with the first available principal
// and ( if keyStore != nil ) trust local SSH CAs without asking for public keys.
func (k *Key) ProxyClientSSHConfig(keyStore sshKnowHostGetter, host string) (*ssh.ClientConfig, error) {
	sshCert, err := k.SSHCert()
	if err != nil {
		return nil, trace.Wrap(err, "failed to extract username from SSH certificate")
	}

	sshConfig, err := sshutils.ProxyClientSSHConfig(sshCert, k)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if keyStore != nil {
		sshConfig.HostKeyCallback = NewKeyStoreCertChecker(keyStore, host)
	}

	return sshConfig, nil
}

// CertUsername returns the name of the Teleport user encoded in the SSH certificate.
func (k *Key) CertUsername() (string, error) {
	cert, err := k.SSHCert()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return cert.KeyId, nil
}

// CertPrincipals returns the principals listed on the SSH certificate.
func (k *Key) CertPrincipals() ([]string, error) {
	cert, err := k.SSHCert()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return cert.ValidPrincipals, nil
}

func (k *Key) CertRoles() ([]string, error) {
	cert, err := k.SSHCert()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Extract roles from certificate. Note, if the certificate is in old format,
	// this will be empty.
	var roles []string
	rawRoles, ok := cert.Extensions[teleport.CertExtensionTeleportRoles]
	if ok {
		roles, err = services.UnmarshalCertRoles(rawRoles)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	return roles, nil
}

// AsAgentKeys converts client.Key struct to a []*agent.AddedKey. All elements
// of the []*agent.AddedKey slice need to be loaded into the agent!
func (k *Key) AsAgentKey() (agent.AddedKey, error) {
	sshCert, err := k.SSHCert()
	if err != nil {
		return agent.AddedKey{}, trace.Wrap(err)
	}
	return k.PrivateKey.AsAgentKey(sshCert)
}

// TeleportTLSCertificate returns the parsed x509 certificate for
// authentication against Teleport APIs.
func (k *Key) TeleportTLSCertificate() (*x509.Certificate, error) {
	return tlsca.ParseCertificatePEM(k.TLSCert)
}

// KubeTLSCertificate returns the parsed x509 certificate for
// authentication against a named kubernetes cluster.
func (k *Key) KubeTLSCertificate(kubeClusterName string) (*x509.Certificate, error) {
	tlsCert, ok := k.KubeTLSCerts[kubeClusterName]
	if !ok {
		return nil, trace.NotFound("TLS certificate for kubernetes cluster %q not found", kubeClusterName)
	}
	return tlsca.ParseCertificatePEM(tlsCert)
}

// DBTLSCertificates returns all parsed x509 database access certificates.
func (k *Key) DBTLSCertificates() (certs []x509.Certificate, err error) {
	for _, bytes := range k.DBTLSCerts {
		cert, err := tlsca.ParseCertificatePEM(bytes)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		certs = append(certs, *cert)
	}
	return certs, nil
}

// AppTLSCertificates returns all parsed x509 app access certificates.
func (k *Key) AppTLSCertificates() (certs []x509.Certificate, err error) {
	for _, bytes := range k.AppTLSCerts {
		cert, err := tlsca.ParseCertificatePEM(bytes)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		certs = append(certs, *cert)
	}
	return certs, nil
}

// TeleportTLSCertValidBefore returns the time of the TLS cert expiration
func (k *Key) TeleportTLSCertValidBefore() (t time.Time, err error) {
	cert, err := k.TeleportTLSCertificate()
	if err != nil {
		return t, trace.Wrap(err)
	}
	return cert.NotAfter, nil
}

// CertValidBefore returns the time of the cert expiration
func (k *Key) CertValidBefore() (t time.Time, err error) {
	cert, err := k.SSHCert()
	if err != nil {
		return t, trace.Wrap(err)
	}
	return time.Unix(int64(cert.ValidBefore), 0), nil
}

// AsAuthMethod returns an "auth method" interface, a common abstraction
// used by Golang SSH library. This is how you actually use a Key to feed
// it into the SSH lib.
func (k *Key) AsAuthMethod() (ssh.AuthMethod, error) {
	cert, err := k.SSHCert()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sshutils.AsAuthMethod(cert, k)
}

// SSHSigner returns an ssh.Signer using the SSH certificate in this key.
func (k *Key) SSHSigner() (ssh.Signer, error) {
	cert, err := k.SSHCert()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sshutils.SSHSigner(cert, k)
}

// SSHCert returns parsed SSH certificate
func (k *Key) SSHCert() (*ssh.Certificate, error) {
	if k.Cert == nil {
		return nil, trace.NotFound("SSH cert not available")
	}
	return sshutils.ParseCertificate(k.Cert)
}

// ActiveRequests gets the active requests associated with this key.
func (k *Key) ActiveRequests() (services.RequestIDs, error) {
	var activeRequests services.RequestIDs
	sshCert, err := k.SSHCert()
	if err != nil {
		return activeRequests, trace.Wrap(err)
	}
	rawRequests, ok := sshCert.Extensions[teleport.CertExtensionTeleportActiveRequests]
	if ok {
		if err := activeRequests.Unmarshal([]byte(rawRequests)); err != nil {
			return activeRequests, trace.Wrap(err)
		}
	}
	return activeRequests, nil
}

// CheckCert makes sure the key's SSH certificate is valid.
func (k *Key) CheckCert() error {
	cert, err := k.SSHCert()
	if err != nil {
		return trace.Wrap(err)
	}

	if err := k.checkCert(cert); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// checkCert makes sure the given SSH certificate is valid.
func (k *Key) checkCert(sshCert *ssh.Certificate) error {
	// Check that the certificate was for the current public key. If not, the
	// public/private key pair may have been rotated.
	if !sshutils.KeysEqual(sshCert.Key, k.SSHPublicKey()) {
		return trace.CompareFailed("public key in profile does not match the public key in SSH certificate")
	}

	// A valid principal is always passed in because the principals are not being
	// checked here, but rather the validity period, signature, and algorithms.
	certChecker := sshutils.CertChecker{
		FIPS: isFIPS(),
	}
	if err := certChecker.CheckCert(sshCert.ValidPrincipals[0], sshCert); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// HostKeyCallback returns an ssh.HostKeyCallback that validates host
// keys/certs against SSH CAs in the Key.
//
// If not CAs are present in the Key, the returned ssh.HostKeyCallback is nil.
// This causes golang.org/x/crypto/ssh to prompt the user to verify host key
// fingerprint (same as OpenSSH does for an unknown host).
func (k *Key) HostKeyCallback(withHostKeyFallback bool) (ssh.HostKeyCallback, error) {
	return sshutils.HostKeyCallback(k.SSHCAs(), withHostKeyFallback)
}

// HostKeyCallbackForClusters returns an ssh.HostKeyCallback that validates host
// keys/certs against SSH clusters CAs.
//
// If not CAs are present in the Key, the returned ssh.HostKeyCallback is nil.
// This causes golang.org/x/crypto/ssh to prompt the user to verify host key
// fingerprint (same as OpenSSH does for an unknown host).
func (k *Key) HostKeyCallbackForClusters(withHostKeyFallback bool, clusters []string) (ssh.HostKeyCallback, error) {
	sshCA, err := k.SSHCAsForClusters(clusters)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sshutils.HostKeyCallback(sshCA, withHostKeyFallback)
}

// RootClusterName extracts the root cluster name from the issuer
// of the Teleport TLS certificate.
func (k *Key) RootClusterName() (string, error) {
	cert, err := k.TeleportTLSCertificate()
	if err != nil {
		return "", trace.Wrap(err)
	}
	clusterName := cert.Issuer.CommonName
	if clusterName == "" {
		return "", trace.NotFound("failed to extract root cluster name from Teleport TLS cert")
	}
	return clusterName, nil
}

// EqualPrivateKey returns whether this key and the given key have the same PrivateKey.
func (k *Key) EqualPrivateKey(other *Key) bool {
	// Compare both private and public key PEM, since hardware keys
	// may not be uniquely identifiable by their private key PEM alone.
	// For example, for PIV keys, the private key PEM only uniquely
	// identifies a PIV slot, so we can use the public key to verify
	// that the private key on the slot hasn't changed.
	return subtle.ConstantTimeCompare(k.PrivateKeyPEM(), other.PrivateKeyPEM()) == 1 &&
		bytes.Equal(k.MarshalSSHPublicKey(), other.MarshalSSHPublicKey())
}
