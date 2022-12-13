/*
Copyright 2015 Gravitational, Inc.

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

package sshutils

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

func TestStartStop(t *testing.T) {
	t.Parallel()

	_, signer, err := utils.CreateCertificate("foo", ssh.HostCert)
	require.NoError(t, err)

	called := false
	fn := NewChanHandlerFunc(func(_ context.Context, _ *ConnectionContext, nch ssh.NewChannel) {
		called = true

		err := nch.Reject(ssh.Prohibited, "nothing to see here")
		assert.NoError(t, err)
	})

	srv, err := NewServer(
		"test",
		utils.NetAddr{AddrNetwork: "tcp", Addr: "localhost:0"},
		fn,
		[]ssh.Signer{signer},
		AuthMethods{Password: pass("abc123")},
	)
	require.NoError(t, err)
	require.NoError(t, srv.Start())

	// Wait for SSH server to successfully shutdown, fail if it does not within
	// the timeout period.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		srv.Wait(ctx)
		require.NoError(t, ctx.Err())
	})

	clientConfig := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.Password("abc123")},
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	}
	clt, err := ssh.Dial("tcp", srv.Addr(), clientConfig)
	require.NoError(t, err)
	defer clt.Close()

	// Call new session to initiate opening new channel. This should get
	// rejected and fail.
	_, err = clt.NewSession()
	require.Error(t, err)
	require.ErrorContains(t, err, "nothing to see here")
	require.True(t, called)

	require.NoError(t, srv.Close())
}

// TestShutdown tests graceul shutdown feature
func TestShutdown(t *testing.T) {
	t.Parallel()

	_, signer, err := utils.CreateCertificate("foo", ssh.HostCert)
	require.NoError(t, err)

	closeContext, cancel := context.WithCancel(context.TODO())
	fn := NewChanHandlerFunc(func(_ context.Context, ccx *ConnectionContext, nch ssh.NewChannel) {
		ch, _, err := nch.Accept()
		require.NoError(t, err)
		defer ch.Close()

		<-closeContext.Done()
		ccx.ServerConn.Close()
	})

	srv, err := NewServer(
		"test",
		utils.NetAddr{AddrNetwork: "tcp", Addr: "localhost:0"},
		fn,
		[]ssh.Signer{signer},
		AuthMethods{Password: pass("abc123")},
		SetShutdownPollPeriod(10*time.Millisecond),
	)
	require.NoError(t, err)
	require.NoError(t, srv.Start())

	clientConfig := &ssh.ClientConfig{
		Auth:            []ssh.AuthMethod{ssh.Password("abc123")},
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	}
	clt, err := ssh.Dial("tcp", srv.Addr(), clientConfig)
	require.NoError(t, err)
	defer clt.Close()

	// call new session to initiate opening new channel
	_, err = clt.NewSession()
	require.NoError(t, err)

	// context will timeout because there is a connection around
	ctx, ctxc := context.WithTimeout(context.TODO(), 50*time.Millisecond)
	defer ctxc()
	require.True(t, trace.IsConnectionProblem(srv.Shutdown(ctx)))

	// now shutdown will return
	cancel()
	ctx2, ctxc2 := context.WithTimeout(context.TODO(), time.Second)
	defer ctxc2()
	require.NoError(t, srv.Shutdown(ctx2))

	// shutdown is re-entrable
	ctx3, ctxc3 := context.WithTimeout(context.TODO(), time.Second)
	defer ctxc3()
	require.NoError(t, srv.Shutdown(ctx3))
}

func TestConfigureCiphers(t *testing.T) {
	t.Parallel()

	_, signer, err := utils.CreateCertificate("foo", ssh.HostCert)
	require.NoError(t, err)

	fn := NewChanHandlerFunc(func(_ context.Context, _ *ConnectionContext, nch ssh.NewChannel) {
		err := nch.Reject(ssh.Prohibited, "nothing to see here")
		assert.NoError(t, err)
	})

	// create a server that only speaks aes128-ctr
	srv, err := NewServer(
		"test",
		utils.NetAddr{AddrNetwork: "tcp", Addr: "localhost:0"},
		fn,
		[]ssh.Signer{signer},
		AuthMethods{Password: pass("abc123")},
		SetCiphers([]string{"aes128-ctr"}),
	)
	require.NoError(t, err)
	require.NoError(t, srv.Start())

	// client only speaks aes256-ctr, should fail
	cc := ssh.ClientConfig{
		Config: ssh.Config{
			Ciphers: []string{"aes256-ctr"},
		},
		Auth:            []ssh.AuthMethod{ssh.Password("abc123")},
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	}
	_, err = ssh.Dial("tcp", srv.Addr(), &cc)
	require.Error(t, err, "cipher mismatch, should fail, got nil")

	// client only speaks aes128-ctr, should succeed
	cc = ssh.ClientConfig{
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr"},
		},
		Auth:            []ssh.AuthMethod{ssh.Password("abc123")},
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	}
	clt, err := ssh.Dial("tcp", srv.Addr(), &cc)
	require.NoError(t, err)
	defer clt.Close()
}

// TestHostSigner makes sure Teleport can not be started with a invalid host
// certificate. The main check is the certificate algorithms.
func TestHostSignerFIPS(t *testing.T) {
	t.Parallel()

	_, signer, err := utils.CreateCertificate("foo", ssh.HostCert)
	require.NoError(t, err)

	_, ellipticSigner, err := utils.CreateEllipticCertificate("foo", ssh.HostCert)
	require.NoError(t, err)

	fn := NewChanHandlerFunc(func(_ context.Context, _ *ConnectionContext, nch ssh.NewChannel) {
		err := nch.Reject(ssh.Prohibited, "nothing to see here")
		assert.NoError(t, err)
	})

	var tests = []struct {
		inSigner ssh.Signer
		inFIPS   bool
		assert   require.ErrorAssertionFunc
	}{
		// ECDSA when in FIPS mode should fail.
		{
			inSigner: ellipticSigner,
			inFIPS:   true,
			assert:   require.Error,
		},
		// RSA when in FIPS mode is okay.
		{
			inSigner: signer,
			inFIPS:   true,
			assert:   require.NoError,
		},
		// ECDSA when in not FIPS mode should succeed.
		{
			inSigner: ellipticSigner,
			inFIPS:   false,
			assert:   require.NoError,
		},
		// RSA when in not FIPS mode should succeed.
		{
			inSigner: signer,
			inFIPS:   false,
			assert:   require.NoError,
		},
	}
	for _, tt := range tests {
		_, err := NewServer(
			"test",
			utils.NetAddr{AddrNetwork: "tcp", Addr: "localhost:0"},
			fn,
			[]ssh.Signer{tt.inSigner},
			AuthMethods{Password: pass("abc123")},
			SetCiphers([]string{"aes128-ctr"}),
			SetFIPS(tt.inFIPS),
		)
		tt.assert(t, err)
	}
}

func pass(need string) PasswordFunc {
	return func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) == need {
			return &ssh.Permissions{
				Extensions: map[string]string{
					utils.ExtIntCertType: utils.ExtIntCertTypeUser,
				},
			}, nil
		}
		return nil, fmt.Errorf("passwords don't match")
	}
}
