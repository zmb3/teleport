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
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"strings"

	"github.com/go-piv/piv-go/piv"
	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api"
	attestation "github.com/gravitational/teleport/api/gen/proto/go/attestation/v1"
)

var (
	// We use slot 9a for Teleport Clients which require `private_key_policy: hardware_key`.
	pivSlotNoTouch = piv.SlotAuthentication
	// We use slot 9c for Teleport Clients which require `private_key_policy: hardware_key_touch`.
	// Private keys generated on this slot will use TouchPolicy=Cached.
	pivSlotWithTouch = piv.SlotSignature
)

// YubiKeyPrivateKey is a YubiKey PIV private key. Cryptographical operations open
// a new temporary connection to the PIV card to perform the operation.
type YubiKeyPrivateKey struct {
	serialNumber uint32
	pivSlot      piv.Slot
	signer       crypto.Signer
	piv          *yubikeyPIV
}

// yubiKeyPrivateKeyData is marshalable data used to retrieve a specific yubiKey PIV private key.
type yubiKeyPrivateKeyData struct {
	SerialNumber uint32 `json:"serial_number"`
	SlotKey      uint32 `json:"slot_key"`
}

func newYubiKeyPrivateKey(ctx context.Context, ykPIV *yubikeyPIV, slot piv.Slot, pub crypto.PublicKey) (*YubiKeyPrivateKey, error) {
	privateKey, err := ykPIV.PrivateKey(slot, pub, piv.KeyAuth{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// privateKey should always be a valid crypto.Signer, but check for safety.
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, trace.BadParameter("yubikey private key does not implement crypto.Signer")
	}

	return &YubiKeyPrivateKey{
		signer:  signer,
		piv:     ykPIV,
		pivSlot: slot,
	}, nil
}

// Public returns the public key corresponding to this private key.
func (y *YubiKeyPrivateKey) Public() crypto.PublicKey {
	return y.signer.Public()
}

// Sign implements crypto.Signer.
func (y *YubiKeyPrivateKey) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) (signature []byte, err error) {
	return y.signer.Sign(rand, digest, opts)
}

func (y *YubiKeyPrivateKey) keyPEM() ([]byte, error) {
	keyDataBytes, err := json.Marshal(yubiKeyPrivateKeyData{
		SerialNumber: y.serialNumber,
		SlotKey:      y.pivSlot.Key,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:    pivYubiKeyPrivateKeyType,
		Headers: nil,
		Bytes:   keyDataBytes,
	}), nil
}

// GetAttestationStatement returns an AttestationStatement for this YubiKeyPrivateKey.
func (y *YubiKeyPrivateKey) GetAttestationStatement() (*AttestationStatement, error) {
	slotCert, err := y.piv.Attest(y.pivSlot)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	attCert, err := y.piv.AttestationCertificate()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &AttestationStatement{
		AttestationStatement: &attestation.AttestationStatement_YubikeyAttestationStatement{
			YubikeyAttestationStatement: &attestation.YubiKeyAttestationStatement{
				SlotCert:        slotCert.Raw,
				AttestationCert: attCert.Raw,
			},
		},
	}, nil
}

// GetPrivateKeyPolicy returns the PrivateKeyPolicy supported by this YubiKeyPrivateKey.
func (k *YubiKeyPrivateKey) GetPrivateKeyPolicy() PrivateKeyPolicy {
	switch k.pivSlot {
	case pivSlotNoTouch:
		return PrivateKeyPolicyHardwareKey
	case pivSlotWithTouch:
		return PrivateKeyPolicyHardwareKeyTouch
	default:
		return PrivateKeyPolicyNone
	}
}

func parsePIVSlot(slotKey uint32) (piv.Slot, error) {
	switch slotKey {
	case piv.SlotAuthentication.Key:
		return piv.SlotAuthentication, nil
	case piv.SlotSignature.Key:
		return piv.SlotSignature, nil
	case piv.SlotCardAuthentication.Key:
		return piv.SlotCardAuthentication, nil
	case piv.SlotKeyManagement.Key:
		return piv.SlotKeyManagement, nil
	default:
		retiredSlot, ok := piv.RetiredKeyManagementSlot(slotKey)
		if !ok {
			return piv.Slot{}, trace.BadParameter("slot %X does not exist", slotKey)
		}
		return retiredSlot, nil
	}
}

// certOrgName is used to identify Teleport Client self-signed certificates stored in yubiKey PIV slots.
const certOrgName = "teleport"

func selfSignedTeleportClientCertificate(priv crypto.PrivateKey, pub crypto.PublicKey) (*x509.Certificate, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit) // see crypto/tls/generate_cert.go
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cert := &x509.Certificate{
		SerialNumber: serialNumber,
		PublicKey:    pub,
		Subject: pkix.Name{
			Organization:       []string{certOrgName},
			OrganizationalUnit: []string{api.Version},
		},
	}
	if cert.Raw, err = x509.CreateCertificate(rand.Reader, cert, cert, pub, priv); err != nil {
		return nil, trace.Wrap(err)
	}
	return cert, nil
}

const (
	// PIVCardTypeYubiKey is the PIV card type assigned to yubiKeys.
	PIVCardTypeYubiKey = "yubikey"
)

// findYubiKey finds a yubiKey PIV card.
func findYubiKey(serialNumber uint32) (string, error) {
	yubiKeyCards, err := findYubiKeyCards()
	if err != nil {
		return "", trace.Wrap(err)
	}

	if len(yubiKeyCards) == 0 {
		return "", trace.NotFound("no yubiKey devices found")
	}

	var errs []error
	for _, card := range yubiKeyCards {
		yk, err := piv.Open(card)
		if err != nil {
			// If we can't open the PIV card, it may already be in use.
			// Append the error and check other available cards.
			errs = append(errs, trace.Wrap(err))
			continue
		}
		defer yk.Close()

		if serialNumber == 0 {
			return card, nil
		}

		ykSerialNumber, err := yk.Serial()
		if err != nil {
			return "", trace.Wrap(err)
		}

		if ykSerialNumber == serialNumber {
			return card, nil
		}
	}

	if len(errs) > 0 {
		return "", trace.Wrap(trace.NewAggregate(errs...), "encountered PIV connection errors while searching for YubiKey with serial number %d", serialNumber)
	}

	return "", trace.NotFound("no YubiKey found with serial number %q", serialNumber)
}

// findYubiKeyCards returns a list of connected yubiKey PIV card names.
func findYubiKeyCards() ([]string, error) {
	cards, err := piv.Cards()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var yubiKeyCards []string
	for _, card := range cards {
		if strings.Contains(strings.ToLower(card), PIVCardTypeYubiKey) {
			yubiKeyCards = append(yubiKeyCards, card)
		}
	}

	return yubiKeyCards, nil
}
