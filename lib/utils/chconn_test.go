/*
Copyright 2021 Gravitational, Inc.

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

package utils

import (
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/utils/sshutils"
)

// TestChConn validates that reads from the channel connection can be
// canceled by setting a read deadline.
func TestChConn(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })

	sshConnCh := make(chan sshConn)

	go startSSHServer(t, listener, sshConnCh)

	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Second,
	})
	require.NoError(t, err)

	_, _, err = client.OpenChannel("test", []byte("hello ssh"))
	require.NoError(t, err)

	select {
	case sshConn := <-sshConnCh:
		chConn := sshutils.NewChConn(sshConn.conn, sshConn.ch)
		t.Cleanup(func() { chConn.Close() })
		doneCh := make(chan error, 1)
		go func() {
			// Nothing is sent on the channel so this will block until the
			// read is canceled by the deadline set below.
			_, err := io.ReadAll(chConn)
			doneCh <- err
		}()
		// Set the read deadline in the past and make sure that the read
		// above is canceled with a timeout error.
		chConn.SetReadDeadline(time.Unix(1, 0))
		select {
		case err := <-doneCh:
			require.True(t, os.IsTimeout(err))
		case <-time.After(time.Second):
			t.Fatal("read from channel connection wasn't canceled after 1 second")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ssh channel after 1 second")
	}
}

type sshConn struct {
	conn ssh.Conn
	ch   ssh.Channel
}

func startSSHServer(t *testing.T, listener net.Listener, sshConnCh chan<- sshConn) {
	nConn, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { nConn.Close() })

	privateKey, err := rsa.GenerateKey(rand.Reader, constants.RSAKeySize)
	require.NoError(t, err)

	_, private, err := MarshalPrivateKey(privateKey)
	require.NoError(t, err)

	signer, err := ssh.ParsePrivateKey(private)
	require.NoError(t, err)

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	conn, chans, _, err := ssh.NewServerConn(nConn, config)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	go func() {
		for newCh := range chans {
			ch, _, err := newCh.Accept()
			require.NoError(t, err)

			sshConnCh <- sshConn{
				conn: conn,
				ch:   ch,
			}
		}
	}()
}
