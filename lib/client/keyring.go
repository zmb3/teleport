/*
Copyright 2017 Gravitational, Inc.

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

// This file is based off of golang.org/x/crypto/ssh/agent/keyring.go.
//
// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/lib/utils/prompt"
)

const (
	constraintConfirmBeforeUse = "confirm-before-use"
	constraintPerSessionMFA    = "per-session-mfa@goteleport.com"
)

// agentKey implements ssh.Signer.
type agentKey struct {
	signer      ssh.Signer
	cert        *ssh.Certificate
	comment     string
	expire      *time.Time
	constraints []constraint
}

// constraint applies a constraint to the given signer. Some constraints
// will return a modified signer, while others will return it as is.
type constraint func(ssh.Signer) (ssh.Signer, error)

func newPrivKey(key agent.AddedKey, constraints ...constraint) (*agentKey, error) {
	signer, err := ssh.NewSignerFromKey(key.PrivateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if key.Certificate != nil {
		signer, err = ssh.NewCertSigner(key.Certificate, signer)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	agentKey := &agentKey{
		signer:      signer,
		comment:     key.Comment,
		constraints: constraints,
	}

	if key.LifetimeSecs > 0 {
		t := time.Now().Add(time.Duration(key.LifetimeSecs) * time.Second)
		agentKey.expire = &t
	}

	return agentKey, nil
}

// PublicKey returns the associated PublicKey.
func (k *agentKey) PublicKey() ssh.PublicKey {
	return k.signer.PublicKey()
}

// Sign returns a signature for the given data. This method will hash the
// data appropriately first. The signature algorithm is expected to match
// the key format returned by the PublicKey.Type method (and not to be any
// alternative algorithm supported by the key format).
func (k *agentKey) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	var err error
	signer := k.signer

	// Add cert to signer if it exists
	if k.cert != nil {
		signer, err = ssh.NewCertSigner(k.cert, signer)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// Apply constraints
	for _, constraint := range k.constraints {
		signer, err = constraint(signer)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	return k.signer.Sign(rand, data)
}

type keyring struct {
	mu   sync.Mutex
	keys []*agentKey

	locked     bool
	passphrase []byte

	extensionHandlers     map[string]extensionHandler
	keyConstraintHandlers map[string]constraintHandler
}

var errLocked = errors.New("agent: locked")

// NewKeyring returns an Agent that holds keys in memory. It is safe
// for concurrent use by multiple goroutines.
func NewKeyring(opts ...KeyringOpt) agent.ExtendedAgent {
	keyring := &keyring{}
	for _, opt := range opts {
		opt(keyring)
	}
	return keyring
}

type KeyringOpt func(*keyring)

type extensionHandler func(req []byte) ([]byte, error)

func WithExtensionHandler(extensionName string, extensionHandler extensionHandler) KeyringOpt {
	return func(k *keyring) {
		k.extensionHandlers[extensionName] = extensionHandler
	}
}

type constraintHandler func(details []byte) (constraint, error)

func WithConfirmBeforeUseConstraintHandler(ctx context.Context, out io.Writer, in io.Reader) KeyringOpt {
	return func(k *keyring) {
		k.keyConstraintHandlers[constraintConfirmBeforeUse] = confirmBeforeUseHandler(ctx, out, in)
	}
}

func confirmBeforeUseHandler(ctx context.Context, out io.Writer, in io.Reader) constraintHandler {
	return func(_ []byte) (constraint, error) {
		return func(signer ssh.Signer) (ssh.Signer, error) {
			cr := prompt.NewContextReader(in)
			defer cr.Close()

			confirmed, err := prompt.Confirmation(ctx, out, prompt.Stdin(), fmt.Sprintf("Confirm signature with private key %q", signer.PublicKey().Marshal()))
			if err != nil {
				return nil, trace.Wrap(err)
			} else if !confirmed {
				return nil, trace.BadParameter("denied")
			}

			return signer, nil
		}, nil
	}
}

type perSessionMFAConstraintDetails struct {
	cluster string
}

func WithPerSessionMFAConstraintHandler(ctx context.Context, tc *TeleportClient) KeyringOpt {
	return func(k *keyring) {
		k.keyConstraintHandlers[constraintPerSessionMFA] = perSessionMFAExtension(ctx, tc)
	}
}

func perSessionMFAExtension(ctx context.Context, tc *TeleportClient) constraintHandler {
	return func(details []byte) (constraint, error) {
		var perSessionMFAConstraintDetails perSessionMFAConstraintDetails
		if err := ssh.Unmarshal(details, perSessionMFAConstraintDetails); err != nil {
			return nil, trace.Wrap(err)
		}

		return func(signer ssh.Signer) (ssh.Signer, error) {
			params := ReissueParams{
				RouteToCluster: tc.SiteName,
				MFACheck:       &proto.IsMFARequiredResponse{Required: true},
			}

			key, err := tc.IssueUserCertsWithMFA(ctx, params, nil /* applyOpts */)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			sshCert, err := key.SSHCert()
			if err != nil {
				return nil, trace.Wrap(err)
			}

			signer, err = ssh.NewCertSigner(sshCert, signer)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			return signer, nil
		}, nil
	}
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
		return errors.New("agent: key not found")
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

	var constraints []constraint
	if key.ConfirmBeforeUse {
		constraintHandler, ok := r.keyConstraintHandlers[constraintConfirmBeforeUse]
		if !ok {
			return trace.BadParameter("constraint extension %q is not supported", constraintConfirmBeforeUse)
		}
		constraint, err := constraintHandler(nil)
		if err != nil {
			return trace.Wrap(err)
		}
		constraints = append(constraints, constraint)
	}

	for _, constraintExtension := range key.ConstraintExtensions {
		constraintHandler, ok := r.keyConstraintHandlers[constraintExtension.ExtensionName]
		if !ok {
			return trace.BadParameter("constraint extension %q is not supported", constraintExtension.ExtensionName)
		}
		constraint, err := constraintHandler(constraintExtension.ExtensionDetails)
		if err != nil {
			return trace.Wrap(err)
		}
		constraints = append(constraints, constraint)
	}

	k, err := newPrivKey(key, constraints...)
	if err != nil {
		return trace.Wrap(err)
	}

	r.keys = append(r.keys, k)
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
		if bytes.Equal(k.PublicKey().Marshal(), wanted) {
			if flags == 0 {
				return k.Sign(rand.Reader, data)
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
	return nil, errors.New("not found")
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
		s = append(s, k)
	}
	return s, nil
}

// The keyring may support extensions provided through KeyringOpts on creation.
func (r *keyring) Extension(extensionType string, contents []byte) ([]byte, error) {
	if handler, ok := r.extensionHandlers[extensionType]; ok {
		return handler(contents)
	}
	return nil, agent.ErrExtensionUnsupported
}
