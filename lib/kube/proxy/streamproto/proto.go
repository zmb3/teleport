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

package streamproto

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/utils"
)

// metaMessage is a control message containing one or more payloads.
type metaMessage struct {
	Resize          *remotecommand.TerminalSize `json:"resize,omitempty"`
	ForceTerminate  bool                        `json:"force_terminate,omitempty"`
	ClientHandshake *ClientHandshake            `json:"client_handshake,omitempty"`
	ServerHandshake *ServerHandshake            `json:"server_handshake,omitempty"`
}

// ClientHandshake is the first message sent by a client to inform a server of it's intentions.
type ClientHandshake struct {
	Mode types.SessionParticipantMode `json:"mode"`
}

// ServerHandshake is the first message sent by a server to inform a client of the session settings.
type ServerHandshake struct {
	MFARequired bool `json:"mfa_required"`
}

// SessionStream represents one end of the bidirectional session connection.
type SessionStream struct {
	// The underlying websocket connection.
	conn *websocket.Conn

	// A stream of incoming session packets.
	in chan []byte

	// Optionally contains a partially read session packet.
	currentIn []byte

	// A list of resize requests.
	resizeQueue chan *remotecommand.TerminalSize

	// A notification channel for force termination requests.
	forceTerminate chan struct{}

	writeSync   sync.Mutex
	done        chan struct{}
	closeOnce   sync.Once
	closed      int32
	MFARequired bool
	Mode        types.SessionParticipantMode
}

// NewSessionStream creates a new session stream.
// The type of the handshake parameter determines if this is the client or server end.
func NewSessionStream(conn *websocket.Conn, handshake interface{}) (*SessionStream, error) {
	s := &SessionStream{
		conn:           conn,
		in:             make(chan []byte),
		done:           make(chan struct{}),
		resizeQueue:    make(chan *remotecommand.TerminalSize, 1),
		forceTerminate: make(chan struct{}),
	}

	clientHandshake, isClient := handshake.(ClientHandshake)
	serverHandshake, ok := handshake.(ServerHandshake)

	if !isClient && !ok {
		return nil, trace.BadParameter("Handshake must be either client or server handshake, got %T", handshake)
	}

	if isClient {
		ty, data, err := conn.ReadMessage()
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if ty != websocket.TextMessage {
			return nil, trace.Errorf("Expected websocket control message, got %v", ty)
		}

		var msg metaMessage
		if err := utils.FastUnmarshal(data, &msg); err != nil {
			return nil, trace.Wrap(err)
		}

		if msg.ServerHandshake == nil {
			return nil, trace.Errorf("Expected websocket server handshake, got %v", msg)
		}

		s.MFARequired = msg.ServerHandshake.MFARequired
		handshakeMsg := metaMessage{ClientHandshake: &clientHandshake}
		dataClientHandshake, err := utils.FastMarshal(handshakeMsg)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if err := conn.WriteMessage(websocket.TextMessage, dataClientHandshake); err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		handshakeMsg := metaMessage{ServerHandshake: &serverHandshake}
		dataServerHandshake, err := utils.FastMarshal(handshakeMsg)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if err := conn.WriteMessage(websocket.TextMessage, dataServerHandshake); err != nil {
			return nil, trace.Wrap(err)
		}

		ty, data, err := conn.ReadMessage()
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if ty != websocket.TextMessage {
			return nil, trace.Errorf("Expected websocket control message, got %v", ty)
		}

		var msg metaMessage
		if err := utils.FastUnmarshal(data, &msg); err != nil {
			return nil, trace.Wrap(err)
		}

		if msg.ClientHandshake == nil {
			return nil, trace.Errorf("Expected websocket client handshake")
		}

		s.Mode = msg.ClientHandshake.Mode
	}

	go s.readTask()
	return s, nil
}

func (s *SessionStream) readTask() {
	for {
		defer s.closeOnce.Do(func() { close(s.done) })

		ty, data, err := s.conn.ReadMessage()
		if err != nil {
			if err != io.EOF && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure, websocket.CloseNoStatusReceived) {
				log.WithError(err).Warn("Failed to read message from websocket")
			}

			return
		}

		if ty == websocket.BinaryMessage {
			s.in <- data
		}

		if ty == websocket.TextMessage {
			var msg metaMessage
			if err := utils.FastUnmarshal(data, &msg); err != nil {
				return
			}

			if msg.Resize != nil {
				s.resizeQueue <- msg.Resize
			}

			if msg.ForceTerminate {
				close(s.forceTerminate)
			}
		}

		if ty == websocket.CloseMessage {
			s.conn.Close()
			atomic.StoreInt32(&s.closed, 1)
			return
		}
	}
}

func (s *SessionStream) Read(p []byte) (int, error) {
	if len(s.currentIn) == 0 {
		select {
		case s.currentIn = <-s.in:
		case <-s.done:
			return 0, io.EOF
		}
	}

	n := copy(p, s.currentIn)
	s.currentIn = s.currentIn[n:]
	return n, nil
}

func (s *SessionStream) Write(data []byte) (int, error) {
	s.writeSync.Lock()
	defer s.writeSync.Unlock()

	err := s.conn.WriteMessage(websocket.BinaryMessage, data)
	if err != nil {
		return 0, trace.Wrap(err)
	}

	return len(data), nil
}

// Resize sends a resize request to the other party.
func (s *SessionStream) Resize(size *remotecommand.TerminalSize) error {
	msg := metaMessage{Resize: size}
	json, err := utils.FastMarshal(msg)
	if err != nil {
		return trace.Wrap(err)
	}

	s.writeSync.Lock()
	defer s.writeSync.Unlock()
	return trace.Wrap(s.conn.WriteMessage(websocket.TextMessage, json))
}

// ResizeQueue returns a channel that will receive resize requests.
func (s *SessionStream) ResizeQueue() <-chan *remotecommand.TerminalSize {
	return s.resizeQueue
}

// ForceTerminateQueue returns the channel used for force termination requests.
func (s *SessionStream) ForceTerminateQueue() <-chan struct{} {
	return s.forceTerminate
}

// ForceTerminate sends a force termination request to the other end.
func (s *SessionStream) ForceTerminate() error {
	msg := metaMessage{ForceTerminate: true}
	json, err := utils.FastMarshal(msg)
	if err != nil {
		return trace.Wrap(err)
	}

	s.writeSync.Lock()
	defer s.writeSync.Unlock()

	return trace.Wrap(s.conn.WriteMessage(websocket.TextMessage, json))
}

func (s *SessionStream) Done() <-chan struct{} {
	return s.done
}

// Close closes the stream.
func (s *SessionStream) Close() error {
	if atomic.LoadInt32(&s.closed) == 0 {
		atomic.StoreInt32(&s.closed, 1)

		err := s.conn.WriteMessage(websocket.CloseMessage, []byte{})
		if err != nil {
			log.Warnf("Failed to gracefully close websocket connection: %v", err)
		}

		select {
		case <-s.done:
		case <-time.After(time.Second * 5):
			s.conn.Close()
		}
	}

	return nil
}
