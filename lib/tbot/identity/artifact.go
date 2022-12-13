/*
Copyright 2021-2022 Gravitational, Inc.

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

package identity

import (
	"bytes"

	"github.com/zmb3/teleport/api/client/proto"
)

// Artifact is a component of a serialized identity.
type Artifact struct {
	// Key is the name that this artifact should be stored under within a
	// destination. For a file based destination, this will be the file name.
	Key       string
	Kind      ArtifactKind
	ToBytes   func(*Identity) []byte
	FromBytes func(*proto.Certs, *LoadIdentityParams, []byte)

	// Optional indicates whether or not an identity should fail to load if this
	// key is missing.
	Optional bool

	// OldKey allows an artifact to be migrated from an older key to a new key.
	// If this value is set, and we are unable to load from Key, we will try
	// and load from OldKey
	OldKey string
}

// Matches returns true if this artifact's Kind matches any one of the given
// kinds or if it's kind is KindAlways
func (a *Artifact) Matches(kinds ...ArtifactKind) bool {
	if a.Kind == KindAlways {
		return true
	}

	for _, kind := range kinds {
		if a.Kind == kind {
			return true
		}
	}

	return false
}

var artifacts = []Artifact{
	// SSH artifacts
	{
		Key: SSHCertKey,

		// DELETE IN: 12.0
		// Migrate from old key "sshcert".
		OldKey: "sshcert",
		Kind:   KindAlways,
		ToBytes: func(i *Identity) []byte {
			return i.CertBytes
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			c.SSH = b
		},
	},
	{
		Key: SSHCACertsKey,

		// SSH CAs in this format are only used for saving/loading of bot
		// identities and are not particularly useful to end users. We encode
		// the current SSH CAs inside the known_hosts file generated with the
		// `ssh_config` template, which is actually readable by OpenSSH.
		Kind: KindBotInternal,
		ToBytes: func(i *Identity) []byte {
			return bytes.Join(i.SSHCACertBytes, []byte("$"))
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			c.SSHCACerts = bytes.Split(b, []byte("$"))
		},
	},

	// TLS artifacts
	{
		Key:  TLSCertKey,
		Kind: KindAlways,
		ToBytes: func(i *Identity) []byte {
			return i.TLSCertBytes
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			c.TLS = b
		},
	},
	{
		Key: TLSCACertsKey,

		// TLS CA certs are useful to end users, but this artifact contains an
		// arbitrary number of CAs, including both Teleport's user and host CAs
		// and potentially multiple sets if they've been rotated.
		// Instead of exposing this mess of CAs to end users, we'll keep these
		// for internal use and just present single standard CAs in destination
		// dirs.
		Kind: KindBotInternal,
		ToBytes: func(i *Identity) []byte {
			return bytes.Join(i.TLSCACertsBytes, []byte("$"))
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			c.TLSCACerts = bytes.Split(b, []byte("$"))
		},
	},

	// Common artifacts
	{
		Key:  PrivateKeyKey,
		Kind: KindAlways,
		ToBytes: func(i *Identity) []byte {
			return i.PrivateKeyBytes
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			p.PrivateKeyBytes = b
		},
	},
	{
		Key:  PublicKeyKey,
		Kind: KindAlways,
		ToBytes: func(i *Identity) []byte {
			return i.PublicKeyBytes
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			p.PublicKeyBytes = b
		},
	},
	{
		// The token hash is used to detect changes to the token and
		// request a new identity when changes are detected.
		Key:  TokenHashKey,
		Kind: KindBotInternal,
		ToBytes: func(i *Identity) []byte {
			return i.TokenHashBytes
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			p.TokenHashBytes = b
		},
	},
	{
		// The write test is used to ensure the destination is writable before
		// attempting a renewal.
		Key:  WriteTestKey,
		Kind: KindAlways,
		ToBytes: func(i *Identity) []byte {
			// always empty
			return []byte{}
		},
		FromBytes: func(c *proto.Certs, p *LoadIdentityParams, b []byte) {
			// nothing to do
		},
		Optional: true,
	},
}

func GetArtifacts() []Artifact {
	return artifacts
}
