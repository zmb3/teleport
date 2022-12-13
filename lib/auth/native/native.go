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

package native

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/wrappers"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/lib/modules"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/utils"
)

var log = logrus.WithFields(logrus.Fields{
	trace.Component: teleport.ComponentKeyGen,
})

// precomputedKeys is a queue of cached keys ready for usage.
var precomputedKeys = make(chan *rsa.PrivateKey, 25)

// startPrecomputeOnce is used to start the background task that precomputes key pairs.
var startPrecomputeOnce sync.Once

// GenerateKeyPair generates a new RSA key pair.
func GenerateKeyPair() ([]byte, []byte, error) {
	priv, err := GeneratePrivateKey()
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	return priv.PrivateKeyPEM(), priv.MarshalSSHPublicKey(), nil
}

// GeneratePrivateKey generates a new RSA private key.
func GeneratePrivateKey() (*keys.PrivateKey, error) {
	rsaKey, err := getOrGenerateRSAPrivateKey()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// We encode the private key in PKCS #1, ASN.1 DER form
	// instead of PKCS #8 to maintain compatibility with some
	// third party clients.
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:    keys.PKCS1PrivateKeyType,
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(rsaKey),
	})

	return keys.NewPrivateKey(rsaKey, keyPEM)
}

func getOrGenerateRSAPrivateKey() (*rsa.PrivateKey, error) {
	select {
	case k := <-precomputedKeys:
		return k, nil
	default:
		rsaKeyPair, err := rsa.GenerateKey(rand.Reader, constants.RSAKeySize)
		if err != nil {
			return nil, err
		}
		return rsaKeyPair, nil
	}
}

func generateRSAPrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, constants.RSAKeySize)
}

func precomputeKeys() {
	const backoff = time.Second * 30
	for {
		rsaPrivateKey, err := generateRSAPrivateKey()
		if err != nil {
			log.WithError(err).Errorf("Failed to precompute key pair, retrying in %s (this might be a bug).", backoff)
			time.Sleep(backoff)
		}

		precomputedKeys <- rsaPrivateKey
	}
}

// PrecomputeKeys sets this package into a mode where a small backlog of keys are
// computed in advance.  This should only be enabled if large spikes in key computation
// are expected (e.g. in auth/proxy services).  Safe to double-call.
func PrecomputeKeys() {
	startPrecomputeOnce.Do(func() {
		go precomputeKeys()
	})
}

// keygen is a key generator that precomputes keys to provide quick access to
// public/private key pairs.
type Keygen struct {
	ctx    context.Context
	cancel context.CancelFunc

	// clock is used to control time.
	clock clockwork.Clock
}

// KeygenOption is a functional optional argument for key generator
type KeygenOption func(k *Keygen)

// SetClock sets the clock to use for key generation.
func SetClock(clock clockwork.Clock) KeygenOption {
	return func(k *Keygen) {
		k.clock = clock
	}
}

// New returns a new key generator.
func New(ctx context.Context, opts ...KeygenOption) *Keygen {
	ctx, cancel := context.WithCancel(ctx)
	k := &Keygen{
		ctx:    ctx,
		cancel: cancel,
		clock:  clockwork.NewRealClock(),
	}
	for _, opt := range opts {
		opt(k)
	}

	return k
}

// Close stops the precomputation of keys (if enabled) and releases all resources.
func (k *Keygen) Close() {
	k.cancel()
}

// GenerateKeyPair returns fresh priv/pub keypair, takes about 300ms to
// execute.
func (k *Keygen) GenerateKeyPair() ([]byte, []byte, error) {
	return GenerateKeyPair()
}

// GenerateHostCert generates a host certificate with the passed in parameters.
// The private key of the CA to sign the certificate must be provided.
func (k *Keygen) GenerateHostCert(c services.HostCertParams) ([]byte, error) {
	if err := c.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	return k.GenerateHostCertWithoutValidation(c)
}

// GenerateHostCertWithoutValidation generates a host certificate with the
// passed in parameters without validating them. For use in tests only.
func (k *Keygen) GenerateHostCertWithoutValidation(c services.HostCertParams) ([]byte, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(c.PublicHostKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Build a valid list of principals from the HostID and NodeName and then
	// add in any additional principals passed in.
	principals := BuildPrincipals(c.HostID, c.NodeName, c.ClusterName, types.SystemRoles{c.Role})
	principals = append(principals, c.Principals...)
	if len(principals) == 0 {
		return nil, trace.BadParameter("no principals provided: %v, %v, %v",
			c.HostID, c.NodeName, c.Principals)
	}
	principals = apiutils.Deduplicate(principals)

	// create certificate
	validBefore := uint64(ssh.CertTimeInfinity)
	if c.TTL != 0 {
		b := k.clock.Now().UTC().Add(c.TTL)
		validBefore = uint64(b.Unix())
	}
	cert := &ssh.Certificate{
		ValidPrincipals: principals,
		Key:             pubKey,
		ValidAfter:      uint64(k.clock.Now().UTC().Add(-1 * time.Minute).Unix()),
		ValidBefore:     validBefore,
		CertType:        ssh.HostCert,
	}
	cert.Permissions.Extensions = make(map[string]string)
	cert.Permissions.Extensions[utils.CertExtensionRole] = c.Role.String()
	cert.Permissions.Extensions[utils.CertExtensionAuthority] = c.ClusterName

	// sign host certificate with private signing key of certificate authority
	if err := cert.SignCert(rand.Reader, c.CASigner); err != nil {
		return nil, trace.Wrap(err)
	}

	log.Debugf("Generated SSH host certificate for role %v with principals: %v.",
		c.Role, principals)
	return ssh.MarshalAuthorizedKey(cert), nil
}

// GenerateUserCert generates a user ssh certificate with the passed in parameters.
// The private key of the CA to sign the certificate must be provided.
func (k *Keygen) GenerateUserCert(c services.UserCertParams) ([]byte, error) {
	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err, "error validating UserCertParams")
	}
	return k.GenerateUserCertWithoutValidation(c)
}

// sourceAddress is a critical option that defines IP addresses (in CIDR notation)
// from which this certificate is accepted for authentication.
// See: https://cvsweb.openbsd.org/src/usr.bin/ssh/PROTOCOL.certkeys?annotate=HEAD.
const sourceAddress = "source-address"

// GenerateUserCertWithoutValidation generates a user ssh certificate with the
// passed in parameters without validating them.
func (k *Keygen) GenerateUserCertWithoutValidation(c services.UserCertParams) ([]byte, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(c.PublicUserKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	validBefore := uint64(ssh.CertTimeInfinity)
	if c.TTL != 0 {
		b := k.clock.Now().UTC().Add(c.TTL)
		validBefore = uint64(b.Unix())
		log.Debugf("generated user key for %v with expiry on (%v) %v", c.AllowedLogins, validBefore, b)
	}
	cert := &ssh.Certificate{
		// we have to use key id to identify teleport user
		KeyId:           c.Username,
		ValidPrincipals: c.AllowedLogins,
		Key:             pubKey,
		ValidAfter:      uint64(k.clock.Now().UTC().Add(-1 * time.Minute).Unix()),
		ValidBefore:     validBefore,
		CertType:        ssh.UserCert,
	}
	cert.Permissions.Extensions = map[string]string{
		teleport.CertExtensionPermitPTY: "",
	}
	if c.PermitX11Forwarding {
		cert.Permissions.Extensions[teleport.CertExtensionPermitX11Forwarding] = ""
	}
	if c.PermitAgentForwarding {
		cert.Permissions.Extensions[teleport.CertExtensionPermitAgentForwarding] = ""
	}
	if c.PermitPortForwarding {
		cert.Permissions.Extensions[teleport.CertExtensionPermitPortForwarding] = ""
	}
	if c.MFAVerified != "" {
		cert.Permissions.Extensions[teleport.CertExtensionMFAVerified] = c.MFAVerified
	}
	if !c.PreviousIdentityExpires.IsZero() {
		cert.Permissions.Extensions[teleport.CertExtensionPreviousIdentityExpires] = c.PreviousIdentityExpires.Format(time.RFC3339)
	}
	if c.ClientIP != "" {
		cert.Permissions.Extensions[teleport.CertExtensionClientIP] = c.ClientIP
	}
	if c.Impersonator != "" {
		cert.Permissions.Extensions[teleport.CertExtensionImpersonator] = c.Impersonator
	}
	if c.DisallowReissue {
		cert.Permissions.Extensions[teleport.CertExtensionDisallowReissue] = ""
	}
	if c.Renewable {
		cert.Permissions.Extensions[teleport.CertExtensionRenewable] = ""
	}
	if c.Generation > 0 {
		cert.Permissions.Extensions[teleport.CertExtensionGeneration] = fmt.Sprint(c.Generation)
	}
	if c.AllowedResourceIDs != "" {
		cert.Permissions.Extensions[teleport.CertExtensionAllowedResources] = c.AllowedResourceIDs
	}
	if c.ConnectionDiagnosticID != "" {
		cert.Permissions.Extensions[teleport.CertExtensionConnectionDiagnosticID] = c.ConnectionDiagnosticID
	}
	if c.PrivateKeyPolicy != "" {
		cert.Permissions.Extensions[teleport.CertExtensionPrivateKeyPolicy] = string(c.PrivateKeyPolicy)
	}

	if c.SourceIP != "" {
		if modules.GetModules().BuildType() != modules.BuildEnterprise {
			return nil, trace.AccessDenied("source IP pinning is only supported in Teleport Enterprise")
		}
		if cert.CriticalOptions == nil {
			cert.CriticalOptions = make(map[string]string)
		}
		//IPv4, all bits matter
		ip := c.SourceIP + "/32"
		if strings.Contains(c.SourceIP, ":") {
			//IPv6
			ip = c.SourceIP + "/128"
		}
		cert.CriticalOptions[sourceAddress] = ip
	}

	for _, extension := range c.CertificateExtensions {
		// TODO(lxea): update behavior when non ssh, non extensions are supported.
		if extension.Mode != types.CertExtensionMode_EXTENSION ||
			extension.Type != types.CertExtensionType_SSH {
			continue
		}
		cert.Extensions[extension.Name] = extension.Value
	}

	// Add roles, traits, and route to cluster in the certificate extensions if
	// the standard format was requested. Certificate extensions are not included
	// legacy SSH certificates due to a bug in OpenSSH <= OpenSSH 7.1:
	// https://bugzilla.mindrot.org/show_bug.cgi?id=2387
	if c.CertificateFormat == constants.CertificateFormatStandard {
		traits, err := wrappers.MarshalTraits(&c.Traits)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if len(traits) > 0 {
			cert.Permissions.Extensions[teleport.CertExtensionTeleportTraits] = string(traits)
		}
		if len(c.Roles) != 0 {
			roles, err := services.MarshalCertRoles(c.Roles)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			cert.Permissions.Extensions[teleport.CertExtensionTeleportRoles] = roles
		}
		if c.RouteToCluster != "" {
			cert.Permissions.Extensions[teleport.CertExtensionTeleportRouteToCluster] = c.RouteToCluster
		}
		if !c.ActiveRequests.IsEmpty() {
			requests, err := c.ActiveRequests.Marshal()
			if err != nil {
				return nil, trace.Wrap(err)
			}
			cert.Permissions.Extensions[teleport.CertExtensionTeleportActiveRequests] = string(requests)
		}
	}

	if err := cert.SignCert(rand.Reader, c.CASigner); err != nil {
		return nil, trace.Wrap(err)
	}
	return ssh.MarshalAuthorizedKey(cert), nil
}

// BuildPrincipals takes a hostID, nodeName, clusterName, and role and builds a list of
// principals to insert into a certificate. This function is backward compatible with
// older clients which means:
//   - If RoleAdmin is in the list of roles, only a single principal is returned: hostID
//   - If nodename is empty, it is not included in the list of principals.
func BuildPrincipals(hostID string, nodeName string, clusterName string, roles types.SystemRoles) []string {
	// TODO(russjones): This should probably be clusterName, but we need to
	// verify changing this won't break older clients.
	if roles.Include(types.RoleAdmin) {
		return []string{hostID}
	}

	// if no hostID was passed it, the user might be specifying an exact list of principals
	if hostID == "" {
		return []string{}
	}

	// always include the hostID, this is what teleport uses internally to find nodes
	principals := []string{
		fmt.Sprintf("%v.%v", hostID, clusterName),
		hostID,
	}

	// nodeName is the DNS name, this is for OpenSSH interoperability
	if nodeName != "" {
		principals = append(principals, fmt.Sprintf("%s.%s", nodeName, clusterName))
		principals = append(principals, nodeName)
	}

	// Add localhost and loopback addresses to allow connecting to proxy/host
	// on the local machine. This should only matter for quickstart and local
	// development.
	principals = append(principals,
		string(teleport.PrincipalLocalhost),
		string(teleport.PrincipalLoopbackV4),
		string(teleport.PrincipalLoopbackV6),
	)

	// deduplicate (in-case hostID and nodeName are the same) and return
	return apiutils.Deduplicate(principals)
}
