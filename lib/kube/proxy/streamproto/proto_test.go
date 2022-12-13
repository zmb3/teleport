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

package streamproto

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/types"
)

var upgrader = websocket.Upgrader{}

func TestPingPong(t *testing.T) {
	t.Parallel()

	runClient := func(conn *websocket.Conn) error {
		client, err := NewSessionStream(conn, ClientHandshake{Mode: types.SessionPeerMode})
		if err != nil {
			return trace.Wrap(err)
		}

		n, err := client.Write([]byte("ping"))
		if err != nil {
			return trace.Wrap(err)
		}
		if n != 4 {
			return trace.Errorf("unexpected write size: %d", n)
		}

		out := make([]byte, 4)
		_, err = io.ReadFull(client, out)
		if err != nil {
			return trace.Wrap(err)
		}
		if string(out) != "pong" {
			return trace.BadParameter("expected pong, got %q", out)
		}

		return nil
	}

	runServer := func(conn *websocket.Conn) error {
		server, err := NewSessionStream(conn, ServerHandshake{MFARequired: false})
		if err != nil {
			return trace.Wrap(err)
		}

		out := make([]byte, 4)
		_, err = io.ReadFull(server, out)
		if err != nil {
			return trace.Wrap(err)
		}
		if string(out) != "ping" {
			return trace.BadParameter("expected ping, got %q", out)
		}

		n, err := server.Write([]byte("pong"))
		if err != nil {
			return trace.Wrap(err)
		}
		if n != 4 {
			return trace.Errorf("unexpected write size: %d", n)
		}

		return nil
	}

	errCh := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		defer ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Time{})
		errCh <- runServer(ws)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)

	// Always drain/close the body.
	io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	go func() {
		defer ws.Close()
		errCh <- runClient(ws)
	}()

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
}
