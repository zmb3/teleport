//go:build libpcsclite
// +build libpcsclite

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
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-piv/piv-go/piv"
	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api/utils/retryutils"
)

var (
	yubikeyPIVManager            *yubiKeyPIVManager
	yubikeyPIVManagerInitialized = new(int32)
)

func initYubiKeyPIVManager(ctx context.Context) error {
	if init := atomic.CompareAndSwapInt32(yubikeyPIVManagerInitialized, 0, 1); !init {
		return trace.AlreadyExists("YubiKey PIV manager already initialized")
	}
	yubikeyPIVManager = newYubiKeyPIVManager(ctx)
	return nil
}

func checkInitialized() error {
	if init := atomic.LoadInt32(yubikeyPIVManagerInitialized); init != 1 {
		return trace.BadParameter("YubiKey PIV manager must be initialized")
	}
	return nil
}

func getOrGenerateYubiKeyPrivateKey(ctx context.Context, touchRequired bool) (*PrivateKey, error) {
	if err := checkInitialized(); err != nil {
		return nil, trace.Wrap(err)
	}
	priv, err := yubikeyPIVManager.getOrGenerateYubiKeyPrivateKey(ctx, touchRequired)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}

func parseYubiKeyPrivateKeyData(keyDataBytes []byte) (crypto.Signer, error) {
	if err := checkInitialized(); err != nil {
		return nil, trace.Wrap(err)
	}

	signer, err := yubikeyPIVManager.parseYubiKeyPrivateKeyData(keyDataBytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return signer, nil
}

func closeYubiKeyPIVManager() error {
	if err := checkInitialized(); err != nil {
		return trace.Wrap(err)
	}

	err := yubikeyPIVManager.close()
	return trace.Wrap(err)
}

type yubiKeyPIVManager struct {
	ctx      context.Context
	closed   bool
	cache    map[uint32]*yubikeyPIV
	cacheMux sync.Mutex
}

func newYubiKeyPIVManager(ctx context.Context) *yubiKeyPIVManager {
	return &yubiKeyPIVManager{
		ctx:   ctx,
		cache: make(map[uint32]*yubikeyPIV),
	}
}

func (m *yubiKeyPIVManager) getOrGenerateYubiKeyPrivateKey(ctx context.Context, touchRequired bool) (*PrivateKey, error) {
	ykPIV, err := yubikeyPIVManager.getYubiKeyPIV(0)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	priv, err := ykPIV.getOrGenerateYubiKeyPrivateKey(ctx, touchRequired)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}

func (m *yubiKeyPIVManager) parseYubiKeyPrivateKeyData(keyDER []byte) (crypto.Signer, error) {
	var keyData yubiKeyPrivateKeyData
	if err := json.Unmarshal(keyDER, &keyData); err != nil {
		return nil, trace.Wrap(err)
	}

	pivSlot, err := parsePIVSlot(keyData.SlotKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ykPIV, err := m.getYubiKeyPIV(keyData.SerialNumber)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ykPIV.keysMux.Lock()
	defer ykPIV.keysMux.Unlock()

	// When parsing a yubikey private key, we don't have a context available,
	// so we default to the yubikey PIV manager ctx.
	priv, err := ykPIV.getPrivateKey(m.ctx, pivSlot)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}

func (m *yubiKeyPIVManager) getYubiKeyPIV(serialNumber uint32) (*yubikeyPIV, error) {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()

	if m.closed {
		return nil, trace.Errorf("closed")
	}

	if ykConn, ok := m.cache[serialNumber]; ok {
		return ykConn, nil
	}

	card, err := findYubiKey(serialNumber)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ykPIV, err := openYubiKeyPIV(m.ctx, card)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	m.cache[serialNumber] = ykPIV
	return ykPIV, nil
}

func (m *yubiKeyPIVManager) close() error {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()
	m.closed = true

	// Close any cached connections.
	var errs []error
	for _, ykPIV := range m.cache {
		errs = append(errs, ykPIV.Close())
	}
	return trace.NewAggregate(errs...)
}

// yubikeyPIV is an open connection to a yubikey's PIV module.
type yubikeyPIV struct {
	// conn is an open connection to the PIV module.
	conn *piv.YubiKey
	// piv connections can only handle a single request at a time,
	// so we must use a mux to handle synchronous requests.
	connMux sync.Mutex
	keys    map[piv.Slot]*YubiKeyPrivateKey
	keysMux sync.Mutex
}

// connectYubiKeyPIV connects to YubiKey PIV module. The returned connection should be closed once
// it's been used. The YubiKey PIV module itself takes some additional time to handle closed
// connections, so we use a retry loop to give the PIV module time to close prior connections.
func openYubiKeyPIV(ctx context.Context, card string) (*yubikeyPIV, error) {
	retry, err := retryutils.NewConstant(time.Millisecond * 10)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	isRetryError := func(error) bool {
		const retryError = "connecting to smart card: the smart card cannot be accessed because of other connections outstanding"
		return strings.Contains(err.Error(), retryError)
	}

	retryCtx, cancel := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancel()

	var yk *piv.YubiKey
	err = retry.For(retryCtx, func() error {
		yk, err = piv.Open(card)
		if err != nil && !isRetryError(err) {
			return retryutils.PermanentRetryError(err)
		}
		return trace.Wrap(err)
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &yubikeyPIV{
		conn: yk,
		keys: make(map[piv.Slot]*YubiKeyPrivateKey),
	}, nil
}

func (y *yubikeyPIV) getOrGenerateYubiKeyPrivateKey(ctx context.Context, touchRequired bool) (*PrivateKey, error) {
	y.keysMux.Lock()
	defer y.keysMux.Unlock()

	// Get the correct PIV slot and Touch policy for the given touch requirement.
	pivSlot := pivSlotNoTouch
	touchPolicy := piv.TouchPolicyNever
	if touchRequired {
		pivSlot = pivSlotWithTouch
		touchPolicy = piv.TouchPolicyCached
	}

	// First, check if there is already a private key
	// on the PIV slot set up by a Teleport Client.
	priv, err := y.getPrivateKey(ctx, pivSlot)
	if err != nil {
		// Generate a new private key on the PIV slot.
		if priv, err = y.generatePrivateKey(ctx, pivSlot, touchPolicy); err != nil {
			return nil, trace.Wrap(err)
		}
	}

	keyPEM, err := priv.keyPEM()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return NewPrivateKey(priv, keyPEM)
}

// generatePrivateKey generates a new private key from the given PIV slot with the given PIV policies.
func (y *yubikeyPIV) generatePrivateKey(ctx context.Context, slot piv.Slot, touchPolicy piv.TouchPolicy) (*YubiKeyPrivateKey, error) {
	opts := piv.Key{
		Algorithm:   piv.AlgorithmEC256,
		PINPolicy:   piv.PINPolicyNever,
		TouchPolicy: touchPolicy,
	}

	pub, err := y.GenerateKey(piv.DefaultManagementKey, slot, opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Create a self signed certificate and store it in the PIV slot so that other
	// Teleport Clients know to reuse the stored key instead of genearting a new one.
	cryptoPriv, err := y.PrivateKey(slot, pub, piv.KeyAuth{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cert, err := selfSignedTeleportClientCertificate(cryptoPriv, pub)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Store a self-signed certificate to mark this slot as used by tsh.
	if err = y.SetCertificate(piv.DefaultManagementKey, slot, cert); err != nil {
		return nil, trace.Wrap(err)
	}

	priv, err := newYubiKeyPrivateKey(ctx, y, slot, pub)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	y.keys[slot] = priv
	return priv, nil
}

// getPrivateKey gets an existing private key from the given PIV slot.
func (y *yubikeyPIV) getPrivateKey(ctx context.Context, slot piv.Slot) (*YubiKeyPrivateKey, error) {
	if key, ok := y.keys[slot]; ok {
		return key, nil
	}

	// Check the slot's certificate to see if it contains a self signed Teleport Client cert.
	cert, err := y.Certificate(slot)
	if err != nil || cert == nil {
		return nil, trace.NotFound("YubiKey certificate slot is empty, expected a Teleport Client cert")
	} else if len(cert.Subject.Organization) == 0 || cert.Subject.Organization[0] != certOrgName {
		return nil, trace.NotFound("YubiKey certificate slot contained unknown certificate:\n%+v", cert)
	}

	// Verify that the slot's certs have the same public key, otherwise the key
	// may have been generated by a non-teleport client.
	if pubComparer, ok := cert.PublicKey.(CryptoPublicKeyI); !ok {
		return nil, trace.BadParameter("certificate's public key of type %T is not a supported public key", cert.PublicKey)
	} else if !pubComparer.Equal(cert.PublicKey) {
		return nil, trace.NotFound("YubiKey slot contains mismatched certificates and must be regenerated")
	}

	priv, err := newYubiKeyPrivateKey(ctx, y, slot, cert.PublicKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	y.keys[slot] = priv
	return priv, nil
}

func (y *yubikeyPIV) Serial() (uint32, error) {
	return y.conn.Serial()
}

func (y *yubikeyPIV) GenerateKey(key [24]byte, slot piv.Slot, opts piv.Key) (crypto.PublicKey, error) {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.GenerateKey(key, slot, opts)
}

func (y *yubikeyPIV) PrivateKey(slot piv.Slot, public crypto.PublicKey, auth piv.KeyAuth) (crypto.PrivateKey, error) {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.PrivateKey(slot, public, auth)
}

func (y *yubikeyPIV) SetCertificate(key [24]byte, slot piv.Slot, cert *x509.Certificate) error {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.SetCertificate(key, slot, cert)
}

func (y *yubikeyPIV) Certificate(slot piv.Slot) (*x509.Certificate, error) {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.Certificate(slot)
}

func (y *yubikeyPIV) Attest(slot piv.Slot) (*x509.Certificate, error) {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.Attest(slot)
}

func (y *yubikeyPIV) AttestationCertificate() (*x509.Certificate, error) {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	return y.conn.AttestationCertificate()
}

func (y *yubikeyPIV) Close() error {
	y.connMux.Lock()
	defer y.connMux.Unlock()
	err := y.conn.Close()
	return trace.Wrap(err)
}
