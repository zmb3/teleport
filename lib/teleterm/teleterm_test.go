// Copyright 2021 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package teleterm

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/utils"
)

const (
	// timeout used for most operations in tests.
	timeout = 5 * time.Second
)

type createClientTLSConfigFunc func(t *testing.T, certsDir string) *tls.Config
type connReadExpectationFunc func(t *testing.T, connReadErr error)

func TestStart(t *testing.T) {
	t.Parallel()

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "teleterm.sock")

	tests := []struct {
		name                    string
		addr                    string
		connReadExpectationFunc connReadExpectationFunc
		// createClientTLSConfigFunc needs to be executed after the server is started. Starting the
		// server saves the public key of the server to disk. Without this key we wouldn't be able to
		// create a valid TLS config for the client.
		//
		// Called only when the server listens on a TCP address.
		createClientTLSConfigFunc createClientTLSConfigFunc
	}{
		{
			// No mTLS.
			name: "unix",
			addr: fmt.Sprintf("unix://%v", sockPath),
			connReadExpectationFunc: func(t *testing.T, connReadErr error) {
				require.NoError(t, connReadErr)
			},
		},
		{
			name: "tcp with valid client cert",
			addr: "tcp://localhost:0",
			createClientTLSConfigFunc: func(t *testing.T, certsDir string) *tls.Config {
				return createValidClientTLSConfig(t, certsDir)
			},
			connReadExpectationFunc: func(t *testing.T, connReadErr error) {
				require.NoError(t, connReadErr)
			},
		},
		{
			// The server reads the client cert from a predetermined path on disk and fall backs to a
			// default config if the cert is not present.
			name: "tcp with client cert not saved to disk",
			addr: "tcp://localhost:0",
			createClientTLSConfigFunc: func(t *testing.T, certsDir string) *tls.Config {
				return &tls.Config{InsecureSkipVerify: true}
			},
			connReadExpectationFunc: func(t *testing.T, connReadErr error) {
				require.ErrorContains(t, connReadErr, "tls: bad certificate")
			},
		},
		{
			name: "tcp with client cert saved to disk but not provided to server",
			addr: "tcp://localhost:0",
			createClientTLSConfigFunc: func(t *testing.T, certsDir string) *tls.Config {
				createValidClientTLSConfig(t, certsDir)
				return &tls.Config{InsecureSkipVerify: true}
			},
			connReadExpectationFunc: func(t *testing.T, connReadErr error) {
				require.ErrorContains(t, connReadErr, "tls: bad certificate")
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			certsDir := t.TempDir()
			listeningC := make(chan utils.NetAddr)

			cfg := Config{
				Addr:       test.addr,
				HomeDir:    homeDir,
				CertsDir:   certsDir,
				ListeningC: listeningC,
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			serveErr := make(chan error)
			go func() {
				err := Serve(ctx, cfg)
				serveErr <- err
			}()

			select {
			case addr := <-listeningC:
				// Verify that the server accepts connections on the advertised address.
				blockUntilServerAcceptsConnections(t, addr, certsDir,
					test.createClientTLSConfigFunc, test.connReadExpectationFunc)
			case <-time.After(timeout):
				t.Fatal("listeningC didn't advertise the address within the timeout")
			}

			// Stop the server.
			cancel()
			require.NoError(t, <-serveErr)
		})
	}

}

// blockUntilServerAcceptsConnections dials the addr and then reads from the connection.
// In case of a unix addr, it waits for the socket file to be created before attempting to dial.
// In case of a tcp addr, it sets up an mTLS config for the dialer.
func blockUntilServerAcceptsConnections(t *testing.T, addr utils.NetAddr, certsDir string,
	createClientTLSConfigFunc createClientTLSConfigFunc, connReadExpectation connReadExpectationFunc) {
	var conn net.Conn
	switch addr.AddrNetwork {
	case "unix":
		conn = dialUnix(t, addr)
	case "tcp":
		conn = dialTCP(t, addr, certsDir, createClientTLSConfigFunc)
	default:
		t.Fatalf("Unknown addr network %v", addr.AddrNetwork)
	}

	t.Cleanup(func() { conn.Close() })

	err := conn.SetReadDeadline(time.Now().Add(timeout))
	require.NoError(t, err)

	out := make([]byte, 1024)
	_, err = conn.Read(out)
	connReadExpectation(t, err)

	err = conn.Close()
	require.NoError(t, err)
}

func dialUnix(t *testing.T, addr utils.NetAddr) net.Conn {
	sockPath := addr.Addr

	// Wait for the socket to be created.
	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPath)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		require.NoError(t, err)
		return true
	}, time.Millisecond*500, time.Millisecond*50)

	conn, err := net.DialTimeout("unix", sockPath, timeout)
	require.NoError(t, err)
	return conn
}

func dialTCP(t *testing.T, addr utils.NetAddr, certsDir string, createClientTLSConfigFunc createClientTLSConfigFunc) net.Conn {
	dialer := tls.Dialer{
		Config: createClientTLSConfigFunc(t, certsDir),
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(func() { cancel() })

	conn, err := dialer.DialContext(ctx, addr.AddrNetwork, addr.Addr)
	require.NoError(t, err)
	return conn
}

func createValidClientTLSConfig(t *testing.T, certsDir string) *tls.Config {
	// Hardcoded filenames under which Connect expects certs. In this test suite, we're trying to
	// reach the tsh gRPC server, so we need to use the renderer cert as the client cert.
	clientCertPath := filepath.Join(certsDir, rendererCertFileName)
	serverCertPath := filepath.Join(certsDir, tshdCertFileName)
	clientCert, err := generateAndSaveCert(clientCertPath)
	require.NoError(t, err)

	tlsConfig, err := createClientTLSConfig(clientCert, serverCertPath)
	require.NoError(t, err)

	return tlsConfig
}
