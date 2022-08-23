package keys

import (
	"crypto"
	"crypto/tls"
	"encoding/pem"
	"io"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
)

type Key interface {
	crypto.Signer
	crypto.PrivateKey
}

type PrivateKey struct {
	key           Key
	privateKeyDER []byte
	sshPub        ssh.PublicKey
}

func (p *PrivateKey) Private() crypto.PrivateKey {
	return p.key
}

func (p *PrivateKey) Public() crypto.PublicKey {
	return p.key.Public()
}

func (p *PrivateKey) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) (signature []byte, err error) {
	return p.key.Sign(rand, digest, opts)
}

// PrivateKeyPEM returns the PEM encoded ECDSA private key.
func (p *PrivateKey) PrivateKeyPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:    pkcs8PrivateKeyType,
		Headers: nil,
		Bytes:   p.privateKeyDER,
	})
}

// SSHPublicKey returns the ssh.PublicKey representation of the public key.
func (p *PrivateKey) SSHPublicKey() ssh.PublicKey {
	return p.sshPub
}

// TLSCertificate parses the given TLS certificate paired with the private key
// to return a tls.Certificate, ready to be used in a TLS handshake.
func (p *PrivateKey) TLSCertificate(certRaw []byte) (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certRaw, p.PrivateKeyPEM())
	return cert, trace.Wrap(err)
}
