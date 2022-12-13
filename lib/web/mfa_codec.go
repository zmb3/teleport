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

package web

import (
	"bytes"
	"encoding/json"

	proto "github.com/gogo/protobuf/proto"
	"github.com/gravitational/trace"

	authproto "github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/srv/desktop/tdp"
	"github.com/zmb3/teleport/lib/web/mfajson"
)

// mfaCodec converts MFA challenges/responses between their native types and a format
// suitable for being sent over a network connection.
type mfaCodec interface {
	// encode converts an MFA challenge to wire format
	encode(chal *client.MFAAuthenticateChallenge, envelopeType string) ([]byte, error)

	// decode parses an MFA authentication response
	decode(bytes []byte, envelopeType string) (*authproto.MFAAuthenticateResponse, error)
}

// protobufMFACodec converts MFA challenges and responses to the protobuf
// format used by SSH web sessions
type protobufMFACodec struct{}

func (protobufMFACodec) encode(chal *client.MFAAuthenticateChallenge, envelopeType string) ([]byte, error) {
	jsonBytes, err := json.Marshal(chal)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	envelope := &Envelope{
		Version: defaults.WebsocketVersion,
		Type:    envelopeType,
		Payload: string(jsonBytes),
	}
	protoBytes, err := proto.Marshal(envelope)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return protoBytes, nil
}

func (protobufMFACodec) decode(bytes []byte, envelopeType string) (*authproto.MFAAuthenticateResponse, error) {
	envelope := &Envelope{}
	if err := proto.Unmarshal(bytes, envelope); err != nil {
		return nil, trace.Wrap(err)
	}

	return mfajson.Decode([]byte(envelope.Payload), envelopeType)
}

// tdpMFACodec converts MFA challenges and responses to Teleport Desktop
// Protocol (TDP) messages used by Desktop Access web sessions
type tdpMFACodec struct{}

func (tdpMFACodec) encode(chal *client.MFAAuthenticateChallenge, envelopeType string) ([]byte, error) {
	switch envelopeType {
	case defaults.WebsocketWebauthnChallenge:
	default:
		return nil, trace.BadParameter(
			"received envelope type %v, expected %v (WebAuthn)", envelopeType, defaults.WebsocketWebauthnChallenge)
	}

	tdpMsg := tdp.MFA{
		Type:                     envelopeType[0],
		MFAAuthenticateChallenge: chal,
	}
	return tdpMsg.Encode()
}

func (tdpMFACodec) decode(buf []byte, envelopeType string) (*authproto.MFAAuthenticateResponse, error) {
	if len(buf) == 0 || tdp.MessageType(buf[0]) != tdp.TypeMFA {
		return nil, trace.BadParameter("expected MFA message type %v, got %v", tdp.TypeMFA, buf[0])
	}
	msg, err := tdp.DecodeMFA(bytes.NewReader(buf[1:]))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return msg.MFAAuthenticateResponse, nil
}
