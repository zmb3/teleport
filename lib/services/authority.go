/*
Copyright 2017-2019 Gravitational, Inc.

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

package services

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"golang.org/x/crypto/ssh"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/wrappers"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/keys"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/jwt"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

// CertAuthoritiesEquivalent checks if a pair of certificate authority resources are equivalent.
// This differs from normal equality only in that resource IDs are ignored.
func CertAuthoritiesEquivalent(lhs, rhs types.CertAuthority) bool {
	return cmp.Equal(lhs, rhs, cmpopts.IgnoreFields(types.Metadata{}, "ID"))
}

// ValidateCertAuthority validates the CertAuthority
func ValidateCertAuthority(ca types.CertAuthority) (err error) {
	if err = ca.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}
	switch ca.GetType() {
	case types.UserCA, types.HostCA:
		err = checkUserOrHostCA(ca)
	case types.DatabaseCA:
		err = checkDatabaseCA(ca)
	case types.JWTSigner:
		err = checkJWTKeys(ca)
	default:
		return trace.BadParameter("invalid CA type %q", ca.GetType())
	}
	return trace.Wrap(err)
}

func checkUserOrHostCA(cai types.CertAuthority) error {
	ca, ok := cai.(*types.CertAuthorityV2)
	if !ok {
		return trace.BadParameter("unknown CA type %T", cai)
	}
	if len(ca.Spec.ActiveKeys.SSH) == 0 {
		return trace.BadParameter("certificate authority missing SSH key pairs")
	}
	if len(ca.Spec.ActiveKeys.TLS) == 0 {
		return trace.BadParameter("certificate authority missing TLS key pairs")
	}
	if _, err := sshutils.GetCheckers(ca); err != nil {
		return trace.Wrap(err)
	}
	if err := sshutils.ValidateSigners(ca); err != nil {
		return trace.Wrap(err)
	}
	// This is to force users to migrate
	if len(ca.GetRoles()) != 0 && len(ca.GetRoleMap()) != 0 {
		return trace.BadParameter("should set either 'roles' or 'role_map', not both")
	}
	_, err := parseRoleMap(ca.GetRoleMap())
	return trace.Wrap(err)
}

// checkDatabaseCA checks if provided certificate authority contains a valid TLS key pair.
// This function is used to verify Database CA.
func checkDatabaseCA(cai types.CertAuthority) error {
	ca, ok := cai.(*types.CertAuthorityV2)
	if !ok {
		return trace.BadParameter("unknown CA type %T", cai)
	}

	if len(ca.Spec.ActiveKeys.TLS) == 0 {
		return trace.BadParameter("DB certificate authority missing TLS key pairs")
	}

	for _, pair := range ca.GetTrustedTLSKeyPairs() {
		if len(pair.Key) > 0 && pair.KeyType == types.PrivateKeyType_RAW {
			var err error
			if len(pair.Cert) > 0 {
				_, err = tls.X509KeyPair(pair.Cert, pair.Key)
			} else {
				_, err = utils.ParsePrivateKey(pair.Key)
			}
			if err != nil {
				return trace.Wrap(err)
			}
		}
		_, err := tlsca.ParseCertificatePEM(pair.Cert)
		if err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

func checkJWTKeys(cai types.CertAuthority) error {
	ca, ok := cai.(*types.CertAuthorityV2)
	if !ok {
		return trace.BadParameter("unknown CA type %T", cai)
	}
	// Check that some JWT keys have been set on the CA.
	if len(ca.Spec.ActiveKeys.JWT) == 0 {
		return trace.BadParameter("missing JWT CA")
	}

	var err error
	var privateKey crypto.Signer

	// Check that the JWT keys set are valid.
	for _, pair := range ca.GetTrustedJWTKeyPairs() {
		// TODO(nic): validate PKCS11 private keys
		if len(pair.PrivateKey) > 0 && pair.PrivateKeyType == types.PrivateKeyType_RAW {
			privateKey, err = utils.ParsePrivateKey(pair.PrivateKey)
			if err != nil {
				return trace.Wrap(err)
			}
		}
		publicKey, err := utils.ParsePublicKey(pair.PublicKey)
		if err != nil {
			return trace.Wrap(err)
		}
		cfg := &jwt.Config{
			Algorithm:   defaults.ApplicationTokenAlgorithm,
			ClusterName: ca.GetClusterName(),
			PrivateKey:  privateKey,
			PublicKey:   publicKey,
		}
		if _, err = jwt.New(cfg); err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

// GetJWTSigner returns the active JWT key used to sign tokens.
func GetJWTSigner(signer crypto.Signer, clusterName string, clock clockwork.Clock) (*jwt.Key, error) {
	key, err := jwt.New(&jwt.Config{
		Clock:       clock,
		Algorithm:   defaults.ApplicationTokenAlgorithm,
		ClusterName: clusterName,
		PrivateKey:  signer,
	})
	return key, trace.Wrap(err)
}

// GetTLSCerts returns TLS certificates from CA
func GetTLSCerts(ca types.CertAuthority) [][]byte {
	pairs := ca.GetTrustedTLSKeyPairs()
	out := make([][]byte, len(pairs))
	for i, pair := range pairs {
		out[i] = append([]byte{}, pair.Cert...)
	}
	return out
}

// GetSSHCheckingKeys returns SSH public keys from CA
func GetSSHCheckingKeys(ca types.CertAuthority) [][]byte {
	pairs := ca.GetTrustedSSHKeyPairs()
	out := make([][]byte, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, append([]byte{}, pair.PublicKey...))
	}
	return out
}

// HostCertParams defines all parameters needed to generate a host certificate
type HostCertParams struct {
	// CASigner is the signer that will sign the public key of the host with the CA private key.
	CASigner ssh.Signer
	// PublicHostKey is the public key of the host
	PublicHostKey []byte
	// HostID is used by Teleport to uniquely identify a node within a cluster
	HostID string
	// Principals is a list of additional principals to add to the certificate.
	Principals []string
	// NodeName is the DNS name of the node
	NodeName string
	// ClusterName is the name of the cluster within which a node lives
	ClusterName string
	// Role identifies the role of a Teleport instance
	Role types.SystemRole
	// TTL defines how long a certificate is valid for
	TTL time.Duration
}

// Check checks parameters for errors
func (c HostCertParams) Check() error {
	if c.CASigner == nil {
		return trace.BadParameter("CASigner is required")
	}
	if c.HostID == "" && len(c.Principals) == 0 {
		return trace.BadParameter("HostID [%q] or Principals [%q] are required",
			c.HostID, c.Principals)
	}
	if c.ClusterName == "" {
		return trace.BadParameter("ClusterName [%q] is required", c.ClusterName)
	}

	if err := c.Role.Check(); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// ChangePasswordReq defines a request to change user password
type ChangePasswordReq struct {
	// User is user ID
	User string
	// OldPassword is user current password
	OldPassword []byte `json:"old_password"`
	// NewPassword is user new password
	NewPassword []byte `json:"new_password"`
	// SecondFactorToken is user 2nd factor token
	SecondFactorToken string `json:"second_factor_token"`
	// WebauthnResponse is Webauthn sign response
	WebauthnResponse *wanlib.CredentialAssertionResponse `json:"webauthn_response"`
}

// UserCertParams defines OpenSSH user certificate parameters
type UserCertParams struct {
	// CASigner is the signer that will sign the public key of the user with the CA private key
	CASigner ssh.Signer
	// PublicUserKey is the public key of the user
	PublicUserKey []byte
	// TTL defines how long a certificate is valid for
	TTL time.Duration
	// Username is teleport username
	Username string
	// Impersonator is set when a user requests certificate for another user
	Impersonator string
	// AllowedLogins is a list of SSH principals
	AllowedLogins []string
	// PermitX11Forwarding permits X11 forwarding for this cert
	PermitX11Forwarding bool
	// PermitAgentForwarding permits agent forwarding for this cert
	PermitAgentForwarding bool
	// PermitPortForwarding permits port forwarding.
	PermitPortForwarding bool
	// PermitFileCopying permits the use of SCP/SFTP.
	PermitFileCopying bool
	// Roles is a list of roles assigned to this user
	Roles []string
	// CertificateFormat is the format of the SSH certificate.
	CertificateFormat string
	// RouteToCluster specifies the target cluster
	// if present in the certificate, will be used
	// to route the requests to
	RouteToCluster string
	// Traits hold claim data used to populate a role at runtime.
	Traits wrappers.Traits
	// ActiveRequests tracks privilege escalation requests applied during
	// certificate construction.
	ActiveRequests RequestIDs
	// MFAVerified is the UUID of an MFA device when this Identity was
	// confirmed immediately after an MFA check.
	MFAVerified string
	// PreviousIdentityExpires is the expiry time of the identity/cert that this
	// identity/cert was derived from. It is used to determine a session's hard
	// deadline in cases where both require_session_mfa and disconnect_expired_cert
	// are enabled. See https://github.com/gravitational/teleport/issues/18544.
	PreviousIdentityExpires time.Time
	// ClientIP is an IP of the client to embed in the certificate.
	ClientIP string
	// SourceIP is an IP that certificate should be pinned to.
	SourceIP string
	// DisallowReissue flags that any attempt to request new certificates while
	// authenticated with this cert should be denied.
	DisallowReissue bool
	// CertificateExtensions are user configured ssh key extensions
	CertificateExtensions []*types.CertExtension
	// Renewable indicates this certificate is renewable
	Renewable bool
	// Generation counts the number of times a certificate has been renewed.
	Generation uint64
	// AllowedResourceIDs lists the resources the user should be able to access.
	AllowedResourceIDs string
	// ConnectionDiagnosticID references the ConnectionDiagnostic that we should use to append traces when testing a Connection.
	ConnectionDiagnosticID string
	// PrivateKeyPolicy is the private key policy supported by this certificate.
	PrivateKeyPolicy keys.PrivateKeyPolicy
}

// CheckAndSetDefaults checks the user certificate parameters
func (c *UserCertParams) CheckAndSetDefaults() error {
	if c.CASigner == nil {
		return trace.BadParameter("CASigner is required")
	}
	if c.TTL < apidefaults.MinCertDuration {
		c.TTL = apidefaults.MinCertDuration
	}
	if len(c.AllowedLogins) == 0 {
		return trace.BadParameter("AllowedLogins are required")
	}
	return nil
}

// CertPoolFromCertAuthorities returns a certificate pool from the TLS certificates
// set up in the certificate authorities list, as well as the number of certificates
// that were added to the pool.
func CertPoolFromCertAuthorities(cas []types.CertAuthority) (*x509.CertPool, int, error) {
	certPool := x509.NewCertPool()
	count := 0
	for _, ca := range cas {
		keyPairs := ca.GetTrustedTLSKeyPairs()
		if len(keyPairs) == 0 {
			continue
		}
		for _, keyPair := range keyPairs {
			cert, err := tlsca.ParseCertificatePEM(keyPair.Cert)
			if err != nil {
				return nil, 0, trace.Wrap(err)
			}
			certPool.AddCert(cert)
			count++
		}
	}
	return certPool, count, nil
}

// CertPool returns certificate pools from TLS certificates
// set up in the certificate authority
func CertPool(ca types.CertAuthority) (*x509.CertPool, error) {
	keyPairs := ca.GetTrustedTLSKeyPairs()
	if len(keyPairs) == 0 {
		return nil, trace.BadParameter("certificate authority has no TLS certificates")
	}
	certPool := x509.NewCertPool()
	for _, keyPair := range keyPairs {
		cert, err := tlsca.ParseCertificatePEM(keyPair.Cert)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		certPool.AddCert(cert)
	}
	return certPool, nil
}

// MarshalCertRoles marshal roles list to OpenSSH
func MarshalCertRoles(roles []string) (string, error) {
	out, err := json.Marshal(types.CertRoles{Version: types.V1, Roles: roles})
	if err != nil {
		return "", trace.Wrap(err)
	}
	return string(out), err
}

// UnmarshalCertRoles marshals roles list to OpenSSH format
func UnmarshalCertRoles(data string) ([]string, error) {
	var certRoles types.CertRoles
	if err := utils.FastUnmarshal([]byte(data), &certRoles); err != nil {
		return nil, trace.BadParameter(err.Error())
	}
	return certRoles.Roles, nil
}

// UnmarshalCertAuthority unmarshals the CertAuthority resource to JSON.
func UnmarshalCertAuthority(bytes []byte, opts ...MarshalOption) (types.CertAuthority, error) {
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var h types.ResourceHeader
	err = utils.FastUnmarshal(bytes, &h)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	switch h.Version {
	case types.V2:
		var ca types.CertAuthorityV2
		if err := utils.FastUnmarshal(bytes, &ca); err != nil {
			return nil, trace.BadParameter(err.Error())
		}

		if err := ValidateCertAuthority(&ca); err != nil {
			return nil, trace.Wrap(err)
		}
		if cfg.ID != 0 {
			ca.SetResourceID(cfg.ID)
		}
		// Correct problems with existing CAs that contain non-UTC times, which
		// causes panics when doing a gogoproto Clone; should only ever be
		// possible with LastRotated, but we enforce it on all the times anyway.
		// See https://github.com/gogo/protobuf/issues/519 .
		if ca.Spec.Rotation != nil {
			apiutils.UTC(&ca.Spec.Rotation.Started)
			apiutils.UTC(&ca.Spec.Rotation.LastRotated)
			apiutils.UTC(&ca.Spec.Rotation.Schedule.UpdateClients)
			apiutils.UTC(&ca.Spec.Rotation.Schedule.UpdateServers)
			apiutils.UTC(&ca.Spec.Rotation.Schedule.Standby)
		}

		return &ca, nil
	}

	return nil, trace.BadParameter("cert authority resource version %v is not supported", h.Version)
}

// MarshalCertAuthority marshals the CertAuthority resource to JSON.
func MarshalCertAuthority(certAuthority types.CertAuthority, opts ...MarshalOption) ([]byte, error) {
	if err := ValidateCertAuthority(certAuthority); err != nil {
		return nil, trace.Wrap(err)
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch certAuthority := certAuthority.(type) {
	case *types.CertAuthorityV2:
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			copy := *certAuthority
			copy.SetResourceID(0)
			certAuthority = &copy
		}
		return utils.FastMarshal(certAuthority)
	default:
		return nil, trace.BadParameter("unrecognized certificate authority version %T", certAuthority)
	}
}
