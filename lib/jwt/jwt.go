/*
Copyright 2020 Gravitational, Inc.

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

// Package jwt is used to sign and verify JWT tokens used by application access.
package jwt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"gopkg.in/square/go-jose.v2"
	"gopkg.in/square/go-jose.v2/cryptosigner"
	"gopkg.in/square/go-jose.v2/jwt"

	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types/wrappers"
	"github.com/zmb3/teleport/lib/utils"
)

// Config defines the clock and PEM encoded bytes of a public and private
// key that form a *jwt.Key.
type Config struct {
	// Clock is used to control expiry time.
	Clock clockwork.Clock

	// PublicKey is used to verify a signed token.
	PublicKey crypto.PublicKey

	// PrivateKey is used to sign and verify tokens.
	PrivateKey crypto.Signer

	// Algorithm is algorithm used to sign JWT tokens.
	Algorithm jose.SignatureAlgorithm

	// ClusterName is the name of the cluster that will be signing the JWT tokens.
	ClusterName string
}

// CheckAndSetDefaults validates the values of a *Config.
func (c *Config) CheckAndSetDefaults() error {
	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}
	if c.PrivateKey != nil {
		c.PublicKey = c.PrivateKey.Public()
	}

	if c.PrivateKey == nil && c.PublicKey == nil {
		return trace.BadParameter("public or private key is required")
	}
	if c.Algorithm == "" {
		return trace.BadParameter("algorithm is required")
	}
	if c.ClusterName == "" {
		return trace.BadParameter("cluster name is required")
	}

	return nil
}

// Key is a JWT key that can be used to sign and/or verify a token.
type Key struct {
	config *Config
}

// New creates a JWT key that can be used to sign and verify tokens.
func New(config *Config) (*Key, error) {
	if err := config.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Key{
		config: config,
	}, nil
}

// SignParams are the claims to be embedded within the JWT token.
type SignParams struct {
	// Username is the Teleport identity.
	Username string

	// Roles are the roles assigned to the user within Teleport.
	Roles []string

	// Traits are the traits assigned to the user within Teleport.
	Traits wrappers.Traits

	// Expiry is time to live for the token.
	Expires time.Time

	// URI is the URI of the recipient application.
	URI string
}

// Check verifies all the values are valid.
func (p *SignParams) Check() error {
	if p.Username == "" {
		return trace.BadParameter("username missing")
	}
	if len(p.Roles) == 0 {
		return trace.BadParameter("roles missing")
	}
	if p.Expires.IsZero() {
		return trace.BadParameter("expires missing")
	}
	if p.URI == "" {
		return trace.BadParameter("uri missing")
	}

	return nil
}

// Sign will return a signed JWT with the passed in claims embedded within.
func (k *Key) sign(claims Claims) (string, error) {
	if k.config.PrivateKey == nil {
		return "", trace.BadParameter("can not sign token with non-signing key")
	}

	// Create a signer with configured private key and algorithm.
	var signer interface{}
	switch k.config.PrivateKey.(type) {
	case *rsa.PrivateKey:
		signer = k.config.PrivateKey
	default:
		signer = cryptosigner.Opaque(k.config.PrivateKey)
	}
	signingKey := jose.SigningKey{
		Algorithm: k.config.Algorithm,
		Key:       signer,
	}
	sig, err := jose.NewSigner(signingKey, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", trace.Wrap(err)
	}

	token, err := jwt.Signed(sig).Claims(claims).CompactSerialize()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return token, nil
}

func (k *Key) Sign(p SignParams) (string, error) {
	if err := p.Check(); err != nil {
		return "", trace.Wrap(err)
	}

	// Sign the claims and create a JWT token.
	claims := Claims{
		Claims: jwt.Claims{
			Subject:   p.Username,
			Issuer:    k.config.ClusterName,
			Audience:  jwt.Audience{p.URI},
			NotBefore: jwt.NewNumericDate(k.config.Clock.Now().Add(-10 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(k.config.Clock.Now()),
			Expiry:    jwt.NewNumericDate(p.Expires),
		},
		Username: p.Username,
		Roles:    p.Roles,
		Traits:   p.Traits,
	}

	return k.sign(claims)
}

func (k *Key) SignSnowflake(p SignParams, issuer string) (string, error) {
	// Sign the claims and create a JWT token.
	claims := Claims{
		Claims: jwt.Claims{
			Subject:   p.Username,
			Issuer:    issuer,
			NotBefore: jwt.NewNumericDate(k.config.Clock.Now().Add(-10 * time.Second)),
			Expiry:    jwt.NewNumericDate(p.Expires),
			IssuedAt:  jwt.NewNumericDate(k.config.Clock.Now().Add(-10 * time.Second)),
		},
	}

	return k.sign(claims)
}

// VerifyParams are the parameters needed to pass the token and data needed to verify.
type VerifyParams struct {
	// Username is the Teleport identity.
	Username string

	// RawToken is the JWT token.
	RawToken string

	// URI is the URI of the recipient application.
	URI string
}

// Check verifies all the values are valid.
func (p *VerifyParams) Check() error {
	if p.Username == "" {
		return trace.BadParameter("username missing")
	}
	if p.RawToken == "" {
		return trace.BadParameter("raw token missing")
	}
	if p.URI == "" {
		return trace.BadParameter("uri missing")
	}

	return nil
}

type SnowflakeVerifyParams struct {
	AccountName string
	LoginName   string
	RawToken    string
}

func (p *SnowflakeVerifyParams) Check() error {
	if p.AccountName == "" {
		return trace.BadParameter("account name missing")
	}

	if p.LoginName == "" {
		return trace.BadParameter("login name is missing")
	}

	if p.RawToken == "" {
		return trace.BadParameter("raw token missing")
	}

	return nil
}

func (k *Key) verify(rawToken string, expectedClaims jwt.Expected) (*Claims, error) {
	if k.config.PublicKey == nil {
		return nil, trace.BadParameter("can not verify token without public key")
	}
	// Parse the token.
	tok, err := jwt.ParseSigned(rawToken)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Validate the signature on the JWT token.
	var out Claims
	if err := tok.Claims(k.config.PublicKey, &out); err != nil {
		return nil, trace.Wrap(err)
	}

	// Validate the claims on the JWT token.
	if err = out.Validate(expectedClaims); err != nil {
		return nil, trace.Wrap(err)
	}

	return &out, nil
}

// Verify will validate the passed in JWT token.
func (k *Key) Verify(p VerifyParams) (*Claims, error) {
	if err := p.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	expectedClaims := jwt.Expected{
		Issuer:   k.config.ClusterName,
		Subject:  p.Username,
		Audience: jwt.Audience{p.URI},
		Time:     k.config.Clock.Now(),
	}

	return k.verify(p.RawToken, expectedClaims)
}

// VerifySnowflake will validate the passed in JWT token.
func (k *Key) VerifySnowflake(p SnowflakeVerifyParams) (*Claims, error) {
	if err := p.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	pubKey, err := x509.MarshalPKIXPublicKey(k.config.PublicKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	keyFp := sha256.Sum256(pubKey)
	keyFpStr := base64.StdEncoding.EncodeToString(keyFp[:])

	accName := strings.ToUpper(p.AccountName)
	loginName := strings.ToUpper(p.LoginName)

	// Generate issuer name in the Snowflake required format.
	issuer := fmt.Sprintf("%s.%s.SHA256:%s", accName, loginName, keyFpStr)

	// Validate the claims on the JWT token.
	expectedClaims := jwt.Expected{
		Issuer:  issuer,
		Subject: fmt.Sprintf("%s.%s", accName, loginName),
		Time:    k.config.Clock.Now(),
	}
	return k.verify(p.RawToken, expectedClaims)
}

// Claims represents public and private claims for a JWT token.
type Claims struct {
	// Claims represents public claim values (as specified in RFC 7519).
	jwt.Claims

	// Username returns the Teleport identity of the user.
	Username string `json:"username"`

	// Roles returns the list of roles assigned to the user within Teleport.
	Roles []string `json:"roles"`

	// Traits returns the traits assigned to the user within Teleport.
	Traits wrappers.Traits `json:"traits"`
}

// GenerateKeyPair generates and return a PEM encoded private and public
// key in the format used by this package.
func GenerateKeyPair() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, constants.RSAKeySize)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	public, private, err := utils.MarshalPrivateKey(privateKey)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	return public, private, nil
}
