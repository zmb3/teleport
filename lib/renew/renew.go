// Package renew contains tools for managing renewable certificates.
package renew

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"log"
	"strings"

	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/trace"
)

type DestinationType string

const (
	// DestinationDir is the destination for certificates stored in a
	// directory on the local filesystem.
	DestinationDir DestinationType = "dir"
)

const (
	// TLSCertKey is the name under which TLS certificates exist in a destination.
	TLSCertKey = "tlscert"

	// TLSCertKey is the name under which SSH certificates exist in a destination.
	SSHCertKey = "sshcert"

	// SSHCACertsKey is the name under which SSH CA certificates exist in a destination.
	SSHCACertsKey = "sshcacerts"

	// TLSCACertsKey is the name under which SSH CA certificates exist in a destination.
	TLSCACertsKey = "tlscacerts"

	// PrivateKeyKey is the name under which the private key exists in a destination.
	// The same private key is used for SSH and TLS certificates.
	PrivateKeyKey = "key"

	// MetadataKey is the name under which additional metadata exists in a destination.
	MetadataKey = "meta"
)

// DestinationSpec specifies where to place certificates acquired and renewed by tbot.
type DestinationSpec struct {
	Type     DestinationType
	Location string
}

// Destination can persist renewable certificates.
type Destination interface { // TODO: make this the store
	TLSConfig() (*tls.Config, error)

	HostID() (string, error)

	Write(name string, data []byte) error
	Read(name string) ([]byte, error)
}

func NewDestination(d *DestinationSpec) (Destination, error) {
	switch d.Type {
	case DestinationDir:
		return &destinationDir{dir: d.Location}, nil
	default:
		return nil, trace.BadParameter("invalid destination type %v", d.Type)
	}
}

func ParseDestinationSpec(s string) (*DestinationSpec, error) {
	i := strings.Index(s, ":")
	if i == -1 || i == len(s)-1 {
		return nil, fmt.Errorf("invalid destination %v, must be of the form type:location", s)
	}

	var typ DestinationType

	switch t := s[:i]; t {
	case string(DestinationDir):
		typ = DestinationType(t)
	default:
		return nil, fmt.Errorf("invalid destination type %v", t)
	}

	return &DestinationSpec{
		Type:     typ,
		Location: s[i+1:],
	}, nil
}

func SaveIdentity(id *auth.Identity, d Destination) error {
	for _, data := range []struct {
		name string
		data []byte
	}{
		{TLSCertKey, id.TLSCertBytes},
		{SSHCertKey, id.CertBytes},
		{TLSCACertsKey, bytes.Join(id.TLSCACertsBytes, []byte("$"))},
		{SSHCACertsKey, bytes.Join(id.SSHCACertBytes, []byte("\n"))},
		{PrivateKeyKey, id.KeyBytes},
		{MetadataKey, []byte(id.ID.HostUUID)},
	} {
		if err := d.Write(data.name, data.data); err != nil {
			return trace.Wrap(err, "could not write to %v", data.name)
		}
	}
	return nil
}

func LoadIdentity(d Destination) (*auth.Identity, error) {
	// TODO: encode the whole thing using the identityfile package?
	var key, tlsCA, sshCA []byte
	var certs proto.Certs
	var err error
	for _, item := range []struct {
		name string
		out  *[]byte
	}{
		{TLSCertKey, &certs.TLS},
		{SSHCertKey, &certs.SSH},
		{TLSCACertsKey, &tlsCA},
		{SSHCACertsKey, &sshCA},
		{PrivateKeyKey, &key},
	} {
		*item.out, err = d.Read(item.name)
		if err != nil {
			return nil, trace.Wrap(err, "could not read %v", item.name)
		}
	}

	certs.SSHCACerts = bytes.Split(sshCA, []byte("\n"))
	certs.TLSCACerts = bytes.Split(tlsCA, []byte("$"))

	log.Printf("got %d SSH CA certs", len(certs.SSHCACerts))
	log.Printf("got %d TLS CA certs", len(certs.TLSCACerts))

	return auth.ReadIdentityFromKeyPair(key, &certs)
}
