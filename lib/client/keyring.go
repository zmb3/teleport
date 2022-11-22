// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package client

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type privKey struct {
	signer       ssh.Signer
	cryptoSigner crypto.Signer
	comment      string
	expire       *time.Time
}

type keyring struct {
	mu   sync.Mutex
	keys []privKey

	locked     bool
	passphrase []byte
}

var (
	errLocked   = trace.AccessDenied("agent: locked")
	errNotFound = trace.NotFound("agent: key not found")
)

// NewKeyring returns an Agent that holds keys in memory.  It is safe
// for concurrent use by multiple goroutines.
func NewKeyring() agent.Agent {
	return &keyring{}
}

// RemoveAll removes all identities.
func (r *keyring) RemoveAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return errLocked
	}

	r.keys = nil
	return nil
}

// removeLocked does the actual key removal. The caller must already be holding the
// keyring mutex.
func (r *keyring) removeLocked(want []byte) error {
	found := false
	for i := 0; i < len(r.keys); {
		if bytes.Equal(r.keys[i].signer.PublicKey().Marshal(), want) {
			found = true
			r.keys[i] = r.keys[len(r.keys)-1]
			r.keys = r.keys[:len(r.keys)-1]
			continue
		} else {
			i++
		}
	}

	if !found {
		return errNotFound
	}
	return nil
}

// Remove removes all identities with the given public key.
func (r *keyring) Remove(key ssh.PublicKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return errLocked
	}

	return r.removeLocked(key.Marshal())
}

// Lock locks the agent. Sign and Remove will fail, and List will return an empty list.
func (r *keyring) Lock(passphrase []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return errLocked
	}

	r.locked = true
	r.passphrase = passphrase
	return nil
}

// Unlock undoes the effect of Lock
func (r *keyring) Unlock(passphrase []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.locked {
		return errors.New("agent: not locked")
	}
	if subtle.ConstantTimeCompare(passphrase, r.passphrase) != 1 {
		return fmt.Errorf("agent: incorrect passphrase")
	}

	r.locked = false
	r.passphrase = nil
	return nil
}

// expireKeysLocked removes expired keys from the keyring. If a key was added
// with a lifetimesecs contraint and seconds >= lifetimesecs seconds have
// elapsed, it is removed. The caller *must* be holding the keyring mutex.
func (r *keyring) expireKeysLocked() {
	for _, k := range r.keys {
		if k.expire != nil && time.Now().After(*k.expire) {
			r.removeLocked(k.signer.PublicKey().Marshal())
		}
	}
}

// List returns the identities known to the agent.
func (r *keyring) List() ([]*agent.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		// section 2.7: locked agents return empty.
		return nil, nil
	}

	r.expireKeysLocked()
	var ids []*agent.Key
	for _, k := range r.keys {
		pub := k.signer.PublicKey()
		ids = append(ids, &agent.Key{
			Format:  pub.Type(),
			Blob:    pub.Marshal(),
			Comment: k.comment})
	}
	return ids, nil
}

// Insert adds a private key to the keyring. If a certificate
// is given, that certificate is added as public key. Note that
// any constraints given are ignored.
func (r *keyring) Add(key agent.AddedKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return errLocked
	}

	cryptoSigner, ok := key.PrivateKey.(crypto.Signer)
	if !ok {
		return trace.BadParameter("invalid agent key: signer of type %T does not implement crypto.Signer", cryptoSigner)
	}

	signer, err := ssh.NewSignerFromKey(key.PrivateKey)
	if err != nil {
		return err
	}

	if cert := key.Certificate; cert != nil {
		signer, err = ssh.NewCertSigner(cert, signer)
		if err != nil {
			return err
		}
	}

	p := privKey{
		signer:       signer,
		cryptoSigner: cryptoSigner,
		comment:      key.Comment,
	}

	if key.LifetimeSecs > 0 {
		t := time.Now().Add(time.Duration(key.LifetimeSecs) * time.Second)
		p.expire = &t
	}

	r.keys = append(r.keys, p)

	return nil
}

// Sign returns a signature for the data.
func (r *keyring) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return r.SignWithFlags(key, data, 0)
}

func (r *keyring) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return nil, errLocked
	}

	r.expireKeysLocked()
	wanted := key.Marshal()
	for _, k := range r.keys {
		if bytes.Equal(k.signer.PublicKey().Marshal(), wanted) {
			if flags == 0 {
				return k.signer.Sign(rand.Reader, data)
			} else {
				if algorithmSigner, ok := k.signer.(ssh.AlgorithmSigner); !ok {
					return nil, fmt.Errorf("agent: signature does not support non-default signature algorithm: %T", k.signer)
				} else {
					var algorithm string
					switch flags {
					case agent.SignatureFlagRsaSha256:
						algorithm = ssh.KeyAlgoRSASHA256
					case agent.SignatureFlagRsaSha512:
						algorithm = ssh.KeyAlgoRSASHA512
					default:
						return nil, fmt.Errorf("agent: unsupported signature flags: %d", flags)
					}
					return algorithmSigner.SignWithAlgorithm(rand.Reader, data, algorithm)
				}
			}
		}
	}
	return nil, errNotFound
}

// Signers returns signers for all the known keys.
func (r *keyring) Signers() ([]ssh.Signer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return nil, errLocked
	}

	r.expireKeysLocked()
	s := make([]ssh.Signer, 0, len(r.keys))
	for _, k := range r.keys {
		s = append(s, k.signer)
	}
	return s, nil
}

// Sign returns a signature for the digest.
func (r *keyring) SignExtension(key ssh.PublicKey, digest []byte, opts crypto.SignerOpts) (signature []byte, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locked {
		return nil, errLocked
	}

	r.expireKeysLocked()
	wanted := key.Marshal()
	for _, k := range r.keys {
		if bytes.Equal(k.signer.PublicKey().Marshal(), wanted) {
			return k.cryptoSigner.Sign(rand.Reader, digest, opts)
		}
	}
	return nil, errNotFound
}

func (r *keyring) Extension(extensionType string, contents []byte) ([]byte, error) {
	switch extensionType {
	case agentExtensionSign:
		var req signExtensionRequest
		if err := ssh.Unmarshal(contents, &req); err != nil {
			return nil, trace.Wrap(err)
		}

		sshPub, err := ssh.ParsePublicKey(req.KeyBlob)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		hash := cryptoHashFromHashName(req.HashName)
		var signerOpts crypto.SignerOpts = hash
		if req.SaltLength != "" {
			pssOpts := &rsa.PSSOptions{Hash: hash}
			if req.SaltLength == saltLengthAuto {
				pssOpts.SaltLength = rsa.PSSSaltLengthAuto
			} else {
				pssOpts.SaltLength, err = strconv.Atoi(req.SaltLength)
				if err != nil {
					return nil, trace.Wrap(err)
				}
			}
			signerOpts = pssOpts
		}

		signature, err := r.SignExtension(sshPub, req.Digest, signerOpts)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		return ssh.Marshal(ssh.Signature{
			Format: sshPub.Type(),
			Blob:   signature,
		}), nil
	default:
		return nil, agent.ErrExtensionUnsupported
	}
}

const agentExtensionSign = "sign@goteleport.com"

type signExtensionRequest struct {
	KeyBlob    []byte
	Digest     []byte
	HashName   string
	SaltLength string
}

func SignExtension(agent agent.ExtendedAgent, pub ssh.PublicKey, digest []byte, opts crypto.SignerOpts) (signature []byte, err error) {
	req := signExtensionRequest{
		KeyBlob:  pub.Marshal(),
		Digest:   digest,
		HashName: opts.HashFunc().String(),
	}
	if pssOpts, ok := opts.(*rsa.PSSOptions); ok {
		switch pssOpts.SaltLength {
		case rsa.PSSSaltLengthEqualsHash:
			req.SaltLength = strconv.Itoa(opts.HashFunc().Size())
		case rsa.PSSSaltLengthAuto:
			req.SaltLength = saltLengthAuto
		default:
			req.SaltLength = strconv.Itoa(pssOpts.SaltLength)
		}
	}
	respBlob, err := agent.Extension(agentExtensionSign, ssh.Marshal(req))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var resp ssh.Signature
	if err := ssh.Unmarshal(respBlob, &resp); err != nil {
		return nil, trace.Wrap(err)
	}
	return resp.Blob, nil
}

// Used to indicate that the salt length will be set during signing to the largest
// value possible. This salt length can then be auto-detected during verification.
const saltLengthAuto = "auto"

// Returns the crypto.Hash for the given hash name, matching the
// value returned by the hash's String method. Unknown hashes will
// return the zero hash, which will skip pre-hashing. This is used
// in some signing algorithms.
func cryptoHashFromHashName(name string) crypto.Hash {
	switch name {
	case "MD4":
		return crypto.MD4
	case "MD5":
		return crypto.MD5
	case "SHA-1":
		return crypto.SHA1
	case "SHA-224":
		return crypto.SHA224
	case "SHA-256":
		return crypto.SHA256
	case "SHA-384":
		return crypto.SHA384
	case "SHA-512":
		return crypto.SHA512
	case "MD5+SHA1":
		return crypto.MD5SHA1
	case "RIPEMD-160":
		return crypto.RIPEMD160
	case "SHA3-224":
		return crypto.SHA3_224
	case "SHA3-256":
		return crypto.SHA3_256
	case "SHA3-384":
		return crypto.SHA3_384
	case "SHA3-512":
		return crypto.SHA3_512
	case "SHA-512/224":
		return crypto.SHA512_224
	case "SHA-512/256":
		return crypto.SHA512_256
	case "BLAKE2s-256":
		return crypto.BLAKE2s_256
	case "BLAKE2b-256":
		return crypto.BLAKE2b_256
	case "BLAKE2b-384":
		return crypto.BLAKE2b_384
	case "BLAKE2b-512":
		return crypto.BLAKE2b_512
	default:
		return crypto.Hash(0)
	}
}
