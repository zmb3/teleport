package client

import (
	"crypto"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/go-piv/piv-go/piv"
	"github.com/gravitational/teleport/api/utils/sshutils"
	"github.com/gravitational/teleport/api/utils/sshutils/ppk"
	"github.com/gravitational/teleport/lib/auth/native"
	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type KeyPair interface {
	PrivateKey() crypto.PrivateKey
	PrivateKeyRaw() []byte
	PublicKeyRaw() []byte
	TLSCertificate(certRaw []byte) (tls.Certificate, error)
	SSHSigner() (ssh.Signer, error)
	AsAgentKeys(cert *ssh.Certificate) ([]agent.AddedKey, error)
}

// PlainKeyPair is a keypair generated and held in memory
type PlainKeyPair struct {
	privateKey crypto.PrivateKey
	// Priv is a PEM encoded private key
	privateKeyRaw []byte `json:"Priv,omitempty"`
	// Pub is a public key
	publicKeyRaw []byte `json:"Pub,omitempty"`
	// PPK is a PuTTY PPK-formatted keypair
	PPK []byte `json:"PPK,omitempty"`
}

// NewPlainKeyPair generates a new unsigned key. Such key must be signed by a
// Teleport CA (auth server) before it becomes useful.
func NewPlainKeyPair() (*PlainKeyPair, error) {
	priv, pub, err := native.GenerateKeyPair()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return GetPlainKeyPair(priv, pub)
}

func GetPlainKeyPair(priv, pub []byte) (*PlainKeyPair, error) {
	// unmarshal private key bytes into a *rsa.PrivateKey
	sshPrivateKey, err := ssh.ParseRawPrivateKey(priv)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	privateKey, ok := sshPrivateKey.(crypto.PrivateKey)
	if !ok {
		return nil, trace.BadParameter("Expected crypto.PrivateKey, got %T", privateKey)
	}

	ppkFile, err := ppk.ConvertToPPK(priv, pub)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &PlainKeyPair{
		privateKey:    privateKey,
		privateKeyRaw: priv,
		publicKeyRaw:  pub,
		PPK:           ppkFile,
	}, nil
}

func (kp *PlainKeyPair) PrivateKey() crypto.PrivateKey {
	return kp.privateKey
}

func (kp *PlainKeyPair) SSHSigner() (ssh.Signer, error) {
	signer, err := ssh.NewSignerFromKey(kp.privateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return signer, nil
}

func (kp *PlainKeyPair) PublicKeyRaw() []byte {
	return kp.publicKeyRaw
}

func (kp *PlainKeyPair) PrivateKeyRaw() []byte {
	return kp.privateKeyRaw
}

func (kp *PlainKeyPair) TLSCertificate(certRaw []byte) (tls.Certificate, error) {
	tlsCert, err := tls.X509KeyPair(certRaw, kp.privateKeyRaw)
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}
	return tlsCert, nil
}

// AsAgentKeys converts client.Key struct to a []*agent.AddedKey. All elements
// of the []*agent.AddedKey slice need to be loaded into the agent!
func (kp *PlainKeyPair) AsAgentKeys(cert *ssh.Certificate) ([]agent.AddedKey, error) {
	return sshutils.AsAgentKeys(cert, kp.privateKeyRaw)
}

// YkKeyPair is a keypair generated and held on a yubikey
type YkKeyPair struct {
	name       string
	privateKey crypto.PrivateKey
	// Pub is a public key
	publicKeyRaw []byte `json:"Pub,omitempty"`
}

func NewYkKeyPair() (*YkKeyPair, error) {
	cards, err := piv.Cards()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	for _, card := range cards {
		if !strings.Contains(strings.ToLower(card), "yubikey") {
			continue
		}
		return GetYkKeyPair(card)
	}

	return nil, trace.NotFound("no yubikey devices available")
}

func GetYkKeyPair(cardName string) (*YkKeyPair, error) {
	yk, err := piv.Open(cardName)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer yk.Close()

	key := piv.Key{
		// TODO is RSA2048 the best choice?
		Algorithm:   piv.AlgorithmRSA2048,
		PINPolicy:   piv.PINPolicyAlways,
		TouchPolicy: piv.TouchPolicyAlways,
	}

	// TODO which slot should we choose? Does it need to be user configurable?
	pub, err := yk.GenerateKey(piv.DefaultManagementKey, piv.SlotAuthentication, key)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)

	auth := piv.KeyAuth{PIN: piv.DefaultPIN}
	priv, err := yk.PrivateKey(piv.SlotAuthentication, pub, auth)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &YkKeyPair{
		name:         cardName,
		privateKey:   priv,
		publicKeyRaw: pubBytes,
	}, nil
}

func (kp *YkKeyPair) PrivateKey() crypto.PrivateKey {
	return kp.PrivateKey
}

func (kp *YkKeyPair) SSHSigner() (ssh.Signer, error) {
	signer, err := ssh.NewSignerFromKey(kp.privateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return signer, nil
}

func (kp *YkKeyPair) PublicKeyRaw() []byte {
	return kp.publicKeyRaw
}

func (kp *YkKeyPair) PrivateKeyRaw() []byte {
	return []byte(fmt.Sprintf("yubikey-card %s", kp.name))
}

func (kp *YkKeyPair) TLSCertificate(certRaw []byte) (tls.Certificate, error) {
	return tls.Certificate{
		Certificate: [][]byte{certRaw},
		PrivateKey:  kp.privateKey,
	}, nil
}

// AsAgentKeys converts client.Key struct to a []*agent.AddedKey. All elements
// of the []*agent.AddedKey slice need to be loaded into the agent!
func (kp *YkKeyPair) AsAgentKeys(cert *ssh.Certificate) ([]agent.AddedKey, error) {
	// TODO Can we use yubikey-agent to still forward agent?
	return []agent.AddedKey{}, nil
}
