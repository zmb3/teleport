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

// Package identityfile handles formatting and parsing of identity files.
package identityfile

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gravitational/trace"
	"github.com/pavlo-v-chernykh/keystore-go/v4"

	"github.com/zmb3/teleport/api/identityfile"
	"github.com/zmb3/teleport/api/utils/keypaths"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/kube/kubeconfig"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/prompt"
)

// Format describes possible file formats how a user identity can be stored.
type Format string

const (
	// FormatFile is when a key + cert are stored concatenated into a single file
	FormatFile Format = "file"

	// FormatOpenSSH is OpenSSH-compatible format, when a key and a cert are stored in
	// two different files (in the same directory)
	FormatOpenSSH Format = "openssh"

	// FormatTLS is a standard TLS format used by common TLS clients (e.g. GRPC) where
	// certificate and key are stored in separate files.
	FormatTLS Format = "tls"

	// FormatKubernetes is a standard Kubernetes format, with all credentials
	// stored in a "kubeconfig" file.
	FormatKubernetes Format = "kubernetes"

	// FormatDatabase produces CA and key pair suitable for configuring a
	// database instance for mutual TLS.
	FormatDatabase Format = "db"

	// FormatMongo produces CA and key pair in the format suitable for
	// configuring a MongoDB database for mutual TLS authentication.
	FormatMongo Format = "mongodb"

	// FormatCockroach produces CA and key pair in the format suitable for
	// configuring a CockroachDB database for mutual TLS.
	FormatCockroach Format = "cockroachdb"

	// FormatRedis produces CA and key pair in the format suitable for
	// configuring a Redis database for mutual TLS.
	FormatRedis Format = "redis"

	// FormatSnowflake produces public key in the format suitable for
	// configuration Snowflake JWT access.
	FormatSnowflake Format = "snowflake"
	// FormatCassandra produces CA and key pair in the format suitable for
	// configuring a Cassandra database for mutual TLS.
	FormatCassandra Format = "cassandra"
	// FormatScylla produces CA and key pair in the format suitable for
	// configuring a Scylla database for mutual TLS.
	FormatScylla Format = "scylla"

	// FormatElasticsearch produces CA and key pair in the format suitable for
	// configuring Elasticsearch for mutual TLS authentication.
	FormatElasticsearch Format = "elasticsearch"

	// DefaultFormat is what Teleport uses by default
	DefaultFormat = FormatFile
)

// FormatList is a list of all possible FormatList.
type FormatList []Format

// KnownFileFormats is a list of all above formats.
var KnownFileFormats = FormatList{
	FormatFile, FormatOpenSSH, FormatTLS, FormatKubernetes, FormatDatabase, FormatMongo,
	FormatCockroach, FormatRedis, FormatSnowflake, FormatElasticsearch, FormatCassandra, FormatScylla,
}

// String returns human-readable version of FormatList, ex:
// file, openssh, tls, kubernetes
func (f FormatList) String() string {
	elems := make([]string, len(f))
	for i, format := range f {
		elems[i] = string(format)
	}
	return strings.Join(elems, ", ")
}

// ConfigWriter is a simple filesystem abstraction to allow alternative simple
// read/write for this package.
type ConfigWriter interface {
	// WriteFile writes the given data to path `name`, using the specified
	// permissions if the file is new.
	WriteFile(name string, data []byte, perm os.FileMode) error

	// Remove removes a file.
	Remove(name string) error

	// Stat fetches information about a file.
	Stat(name string) (fs.FileInfo, error)
}

// StandardConfigWriter is a trivial ConfigWriter that wraps the relevant `os` functions.
type StandardConfigWriter struct{}

// WriteFile writes data to the named file, creating it if necessary.
func (s *StandardConfigWriter) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// Remove removes the named file or (empty) directory.
// If there is an error, it will be of type *PathError.
func (s *StandardConfigWriter) Remove(name string) error {
	return os.Remove(name)
}

// Stat returns a FileInfo describing the named file.
// If there is an error, it will be of type *PathError.
func (s *StandardConfigWriter) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

// WriteConfig holds the necessary information to write an identity file.
type WriteConfig struct {
	// OutputPath is the output path for the identity file. Note that some
	// formats (like FormatOpenSSH and FormatTLS) write multiple output files
	// and use OutputPath as a prefix.
	OutputPath string
	// Key contains the credentials to write to the identity file.
	Key *client.Key
	// Format is the output format for the identity file.
	Format Format
	// KubeProxyAddr is the public address of the proxy with its kubernetes
	// port. KubeProxyAddr is only used when Format is FormatKubernetes.
	KubeProxyAddr string
	// KubeClusterName is the Kubernetes Cluster name.
	// KubeClusterName is only used when Format is FormatKubernetes.
	KubeClusterName string
	// KubeTLSServerName is the SNI host value passed to the server.
	KubeTLSServerName string
	// KubeStoreAllCAs stores the CAs of all clusters in kubeconfig, instead
	// of just the root cluster's CA.
	KubeStoreAllCAs bool
	// OverwriteDestination forces all existing destination files to be
	// overwritten. When false, user will be prompted for confirmation of
	// overwrite first.
	OverwriteDestination bool
	// Writer is the filesystem implementation.
	Writer ConfigWriter
	// JKSPassword is the password for the JKS keystore used by Cassandra format.
	JKSPassword string
}

// Write writes user credentials to disk in a specified format.
// It returns the names of the files successfully written.
func Write(cfg WriteConfig) (filesWritten []string, err error) {
	// If no writer was set, use the standard implementation.
	writer := cfg.Writer
	if writer == nil {
		writer = &StandardConfigWriter{}
	}

	if cfg.OutputPath == "" {
		return nil, trace.BadParameter("identity output path is not specified")
	}

	switch cfg.Format {
	// dump user identity into a single file:
	case FormatFile:
		filesWritten = append(filesWritten, cfg.OutputPath)
		if err := checkOverwrite(writer, cfg.OverwriteDestination, filesWritten...); err != nil {
			return nil, trace.Wrap(err)
		}

		idFile := &identityfile.IdentityFile{
			PrivateKey: cfg.Key.PrivateKeyPEM(),
			Certs: identityfile.Certs{
				SSH: cfg.Key.Cert,
				TLS: cfg.Key.TLSCert,
			},
		}
		// append trusted host certificate authorities
		for _, ca := range cfg.Key.TrustedCA {
			// append ssh ca certificates
			for _, publicKey := range ca.HostCertificates {
				data, err := sshutils.MarshalAuthorizedHostsFormat(ca.ClusterName, publicKey, nil)
				if err != nil {
					return nil, trace.Wrap(err)
				}
				idFile.CACerts.SSH = append(idFile.CACerts.SSH, []byte(data))
			}
			// append tls ca certificates
			idFile.CACerts.TLS = append(idFile.CACerts.TLS, ca.TLSCertificates...)
		}

		idBytes, err := identityfile.Encode(idFile)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if err := writer.WriteFile(cfg.OutputPath, idBytes, identityfile.FilePermissions); err != nil {
			return nil, trace.Wrap(err)
		}

	// dump user identity into separate files:
	case FormatOpenSSH:
		keyPath := cfg.OutputPath
		certPath := keypaths.IdentitySSHCertPath(keyPath)
		filesWritten = append(filesWritten, keyPath, certPath)
		if err := checkOverwrite(writer, cfg.OverwriteDestination, filesWritten...); err != nil {
			return nil, trace.Wrap(err)
		}

		err = writer.WriteFile(certPath, cfg.Key.Cert, identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		err = writer.WriteFile(keyPath, cfg.Key.PrivateKeyPEM(), identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}

	case FormatTLS, FormatDatabase, FormatCockroach, FormatRedis, FormatElasticsearch, FormatScylla:
		keyPath := cfg.OutputPath + ".key"
		certPath := cfg.OutputPath + ".crt"
		casPath := cfg.OutputPath + ".cas"

		// CockroachDB expects files to be named ca.crt, node.crt and node.key.
		if cfg.Format == FormatCockroach {
			keyPath = filepath.Join(cfg.OutputPath, "node.key")
			certPath = filepath.Join(cfg.OutputPath, "node.crt")
			casPath = filepath.Join(cfg.OutputPath, "ca.crt")
		}

		filesWritten = append(filesWritten, keyPath, certPath, casPath)
		if err := checkOverwrite(writer, cfg.OverwriteDestination, filesWritten...); err != nil {
			return nil, trace.Wrap(err)
		}

		err = writer.WriteFile(certPath, cfg.Key.TLSCert, identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		err = writer.WriteFile(keyPath, cfg.Key.PrivateKeyPEM(), identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		var caCerts []byte
		for _, ca := range cfg.Key.TrustedCA {
			for _, cert := range ca.TLSCertificates {
				caCerts = append(caCerts, cert...)
			}
		}
		err = writer.WriteFile(casPath, caCerts, identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}

	// FormatMongo is the same as FormatTLS or FormatDatabase certificate and
	// key are concatenated in the same .crt file which is what Mongo expects.
	case FormatMongo:
		certPath := cfg.OutputPath + ".crt"
		casPath := cfg.OutputPath + ".cas"
		filesWritten = append(filesWritten, certPath, casPath)
		if err := checkOverwrite(writer, cfg.OverwriteDestination, filesWritten...); err != nil {
			return nil, trace.Wrap(err)
		}
		err = writer.WriteFile(certPath, append(cfg.Key.TLSCert, cfg.Key.PrivateKeyPEM()...), identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		var caCerts []byte
		for _, ca := range cfg.Key.TrustedCA {
			for _, cert := range ca.TLSCertificates {
				caCerts = append(caCerts, cert...)
			}
		}
		err = writer.WriteFile(casPath, caCerts, identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	case FormatSnowflake:
		pubPath := cfg.OutputPath + ".pub"
		filesWritten = append(filesWritten, pubPath)

		if err := checkOverwrite(writer, cfg.OverwriteDestination, pubPath); err != nil {
			return nil, trace.Wrap(err)
		}

		var caCerts []byte
		for _, ca := range cfg.Key.TrustedCA {
			for _, cert := range ca.TLSCertificates {
				block, _ := pem.Decode(cert)
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					return nil, trace.Wrap(err)
				}
				pubKey, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
				if err != nil {
					return nil, trace.Wrap(err)
				}
				pubPem := pem.EncodeToMemory(&pem.Block{
					Type:  "PUBLIC KEY",
					Bytes: pubKey,
				})
				caCerts = append(caCerts, pubPem...)
			}
		}

		err = os.WriteFile(pubPath, caCerts, identityfile.FilePermissions)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	case FormatCassandra:
		out, err := writeCassandraFormat(cfg, writer)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		filesWritten = append(filesWritten, out...)

	case FormatKubernetes:
		filesWritten = append(filesWritten, cfg.OutputPath)
		// If the user does not want to override,  it will merge the previous kubeconfig
		// with the new entry.
		if err := checkOverwrite(writer, cfg.OverwriteDestination, filesWritten...); err != nil && !trace.IsAlreadyExists(err) {
			return nil, trace.Wrap(err)
		} else if err == nil {
			// Clean up the existing file, if it exists.
			// This is used when the user wants to overwrite an existing kubeconfig.
			// Without it, kubeconfig.Update would try to parse it and merge in new
			// credentials.
			if err := writer.Remove(cfg.OutputPath); err != nil && !os.IsNotExist(err) {
				return nil, trace.Wrap(err)
			}
		}

		var kubeCluster []string
		if len(cfg.KubeClusterName) > 0 {
			kubeCluster = []string{cfg.KubeClusterName}
		}

		if err := kubeconfig.Update(cfg.OutputPath, kubeconfig.Values{
			TeleportClusterName: cfg.Key.ClusterName,
			ClusterAddr:         cfg.KubeProxyAddr,
			Credentials:         cfg.Key,
			TLSServerName:       cfg.KubeTLSServerName,
			KubeClusters:        kubeCluster,
		}, cfg.KubeStoreAllCAs); err != nil {
			return nil, trace.Wrap(err)
		}

	default:
		return nil, trace.BadParameter("unsupported identity format: %q, use one of %s", cfg.Format, KnownFileFormats)
	}
	return filesWritten, nil
}

func writeCassandraFormat(cfg WriteConfig, writer ConfigWriter) ([]string, error) {
	if cfg.JKSPassword == "" {
		pass, err := utils.CryptoRandomHex(16)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		cfg.JKSPassword = pass
	}
	// Cassandra expects a JKS keystore file with the private key and certificate
	// in it. The keystore file is password protected.
	keystoreBuf, err := prepareCassandraKeystore(cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Cassandra expects a JKS truststore file with the CA certificate in it.
	// The truststore file is password protected.
	truststoreBuf, err := prepareCassandraTruststore(cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	certPath := cfg.OutputPath + ".keystore"
	casPath := cfg.OutputPath + ".truststore"
	err = writer.WriteFile(certPath, keystoreBuf.Bytes(), identityfile.FilePermissions)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	err = writer.WriteFile(casPath, truststoreBuf.Bytes(), identityfile.FilePermissions)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return []string{certPath, casPath}, nil
}

func prepareCassandraTruststore(cfg WriteConfig) (*bytes.Buffer, error) {
	var caCerts []byte
	for _, ca := range cfg.Key.TrustedCA {
		for _, cert := range ca.TLSCertificates {
			block, _ := pem.Decode(cert)
			caCerts = append(caCerts, block.Bytes...)
		}
	}

	ks := keystore.New()
	trustIn := keystore.TrustedCertificateEntry{
		CreationTime: time.Now(),
		Certificate: keystore.Certificate{
			Type:    "x509",
			Content: caCerts,
		},
	}
	if err := ks.SetTrustedCertificateEntry("cassandra", trustIn); err != nil {
		return nil, trace.Wrap(err)
	}
	var buff bytes.Buffer
	if err := ks.Store(&buff, []byte(cfg.JKSPassword)); err != nil {
		return nil, trace.Wrap(err)
	}
	return &buff, nil
}

func prepareCassandraKeystore(cfg WriteConfig) (*bytes.Buffer, error) {
	certBlock, _ := pem.Decode(cfg.Key.TLSCert)
	privBlock, _ := pem.Decode(cfg.Key.PrivateKeyPEM())

	privKey, err := x509.ParsePKCS1PrivateKey(privBlock.Bytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	privKeyPkcs8, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ks := keystore.New()
	pkeIn := keystore.PrivateKeyEntry{
		CreationTime: time.Now(),
		PrivateKey:   privKeyPkcs8,
		CertificateChain: []keystore.Certificate{
			{
				Type:    "x509",
				Content: certBlock.Bytes,
			},
		},
	}
	if err := ks.SetPrivateKeyEntry("cassandra", pkeIn, []byte(cfg.JKSPassword)); err != nil {
		return nil, trace.Wrap(err)
	}
	var buff bytes.Buffer
	if err := ks.Store(&buff, []byte(cfg.JKSPassword)); err != nil {
		return nil, trace.Wrap(err)
	}
	return &buff, nil
}

func checkOverwrite(writer ConfigWriter, force bool, paths ...string) error {
	var existingFiles []string
	// Check if the destination file exists.
	for _, path := range paths {
		_, err := writer.Stat(path)
		if os.IsNotExist(err) {
			// File doesn't exist, proceed.
			continue
		}
		if err != nil {
			// Something else went wrong, fail.
			return trace.ConvertSystemError(err)
		}
		existingFiles = append(existingFiles, path)
	}
	if len(existingFiles) == 0 || force {
		// Files don't exist or we're asked not to prompt, proceed.
		return nil
	}

	// Some files exist, prompt user whether to overwrite.
	overwrite, err := prompt.Confirmation(context.Background(), os.Stderr, prompt.Stdin(), fmt.Sprintf("Destination file(s) %s exist. Overwrite?", strings.Join(existingFiles, ", ")))
	if err != nil {
		return trace.Wrap(err)
	}
	if !overwrite {
		return trace.AlreadyExists("not overwriting destination files %s", strings.Join(existingFiles, ", "))
	}
	return nil
}
