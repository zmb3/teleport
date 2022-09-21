/*
Copyright 2022 Gravitational, Inc.
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

package keys

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"sync"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
)

const (
	PKCS1PrivateKeyType = "RSA PRIVATE KEY"
	PKCS8PrivateKeyType = "PRIVATE KEY"
	ECPrivateKeyType    = "EC PRIVATE KEY"
)

// PrivateKeyParsers is a function which can parse a specific type
// of key from its ASN.1 DER into a usable crypto.Signer.
type PrivateKeyParser func(keyDER []byte) (crypto.Signer, error)

var parsers map[string]PrivateKeyParser
var parsersMux sync.Mutex

func init() {
	parsersMux.Lock()
	defer parsersMux.Unlock()
	parsers = map[string]PrivateKeyParser{
		PKCS1PrivateKeyType: func(keyDER []byte) (crypto.Signer, error) {
			cryptoSigner, err := x509.ParsePKCS1PrivateKey(keyDER)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return cryptoSigner, nil
		},
		PKCS8PrivateKeyType: func(keyDER []byte) (crypto.Signer, error) {
			priv, err := x509.ParsePKCS8PrivateKey(keyDER)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			cryptoSigner, ok := priv.(crypto.Signer)
			if !ok {
				return nil, trace.BadParameter("x509.ParsePKCS8PrivateKey returned an invalid private key of type %T", priv)
			}
			return cryptoSigner, nil
		},
		ECPrivateKeyType: func(keyDER []byte) (crypto.Signer, error) {
			cryptoSigner, err := x509.ParseECPrivateKey(keyDER)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return cryptoSigner, nil
		},
	}
}

// AddPrivateKeyParser adds a new key type and parser which can be used
// by ParsePrivateKey. Some key types, such as YubiKey PIV private keys,
// must be added dynamically due to build constraints.
func AddPrivateKeyParser(keyType string, parser PrivateKeyParser) error {
	parsersMux.Lock()
	defer parsersMux.Unlock()
	if _, ok := parsers[keyType]; ok {
		return trace.AlreadyExists("parser for key type %q already exists", keyType)
	}
	parsers[keyType] = parser
	return nil
}

func getParser(keyType string) (PrivateKeyParser, error) {
	parsersMux.Lock()
	defer parsersMux.Unlock()
	parser, ok := parsers[keyType]
	if !ok {
		return nil, trace.BadParameter("unexpected private key PEM type %q", keyType)
	}
	return parser, nil
}

// ParsePrivateKey returns the PrivateKey for the given key PEM block.
func ParsePrivateKey(keyPEM []byte) (*PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, trace.BadParameter("expected PEM encoded private key")
	}

	parser, err := getParser(block.Type)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	signer, err := parser(block.Bytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return NewPrivateKey(signer, keyPEM)
}

// LoadPrivateKey returns the PrivateKey for the given key file.
func LoadPrivateKey(keyFile string) (*PrivateKey, error) {
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}

	priv, err := ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}

// LoadKeyPair returns the PrivateKey for the given private and public key files.
func LoadKeyPair(privFile, sshPubFile string) (*PrivateKey, error) {
	privPEM, err := os.ReadFile(privFile)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}

	marshalledSSHPub, err := os.ReadFile(sshPubFile)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}

	priv, err := ParseKeyPair(privPEM, marshalledSSHPub)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}

// ParseKeyPair returns the PrivateKey for the given private and public key PEM blocks.
func ParseKeyPair(privPEM, marshalledSSHPub []byte) (*PrivateKey, error) {
	priv, err := ParsePrivateKey(privPEM)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Verify that the private key's public key matches the expected public key.
	if !bytes.Equal(ssh.MarshalAuthorizedKey(priv.SSHPublicKey()), marshalledSSHPub) {
		return nil, trace.CompareFailed("the given private and public keys do not form a valid keypair")
	}

	return priv, nil
}

// LoadX509KeyPair parse a tls.Certificate from a private key file and certificate file.
// This should be used instead of tls.LoadX509KeyPair to support non-raw private keys, like PIV keys.
func LoadX509KeyPair(certFile, keyFile string) (tls.Certificate, error) {
	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, trace.ConvertSystemError(err)
	}

	certPEMBlock, err := os.ReadFile(certFile)
	if err != nil {
		return tls.Certificate{}, trace.ConvertSystemError(err)
	}

	tlsCert, err := X509KeyPair(certPEMBlock, keyPEMBlock)
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}

	return tlsCert, nil
}

// X509KeyPair parse a tls.Certificate from a private key PEM and certificate PEM.
// This should be used instead of tls.X509KeyPair to support non-raw private keys, like PIV keys.
func X509KeyPair(certPEMBlock, keyPEMBlock []byte) (tls.Certificate, error) {
	priv, err := ParsePrivateKey(keyPEMBlock)
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}

	tlsCert, err := priv.TLSCertificate(certPEMBlock)
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}

	return tlsCert, nil
}
