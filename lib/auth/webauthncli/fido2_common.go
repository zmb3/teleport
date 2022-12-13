// Copyright 2022 Gravitational, Inc
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

package webauthncli

import (
	"context"
	"io"
	"time"

	"github.com/duo-labs/webauthn/protocol"
	"github.com/duo-labs/webauthn/protocol/webauthncose"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/client/proto"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
)

// FIDO2PollInterval is the poll interval used to check for new FIDO2 devices.
var FIDO2PollInterval = 200 * time.Millisecond

// FIDO2Login implements Login for CTAP1 and CTAP2 devices.
// It must be called with a context with timeout, otherwise it can run
// indefinitely.
// The informed user is used to disambiguate credentials in case of passwordless
// logins.
// It returns an MFAAuthenticateResponse and the credential user, if a resident
// credential is used.
// Most callers should call Login directly, as it is correctly guarded by
// IsFIDO2Available.
func FIDO2Login(
	ctx context.Context,
	origin string, assertion *wanlib.CredentialAssertion, prompt LoginPrompt, opts *LoginOpts,
) (*proto.MFAAuthenticateResponse, string, error) {
	return fido2Login(ctx, origin, assertion, prompt, opts)
}

// FIDO2Register implements Register for CTAP1 and CTAP2 devices.
// It must be called with a context with timeout, otherwise it can run
// indefinitely.
// Most callers should call Register directly, as it is correctly guarded by
// IsFIDO2Available.
func FIDO2Register(
	ctx context.Context,
	origin string, cc *wanlib.CredentialCreation, prompt RegisterPrompt,
) (*proto.MFARegisterResponse, error) {
	return fido2Register(ctx, origin, cc, prompt)
}

type FIDO2DiagResult struct {
	Available                           bool
	RegisterSuccessful, LoginSuccessful bool
}

// FIDO2Diag runs a few diagnostic commands and returns the result.
// User interaction is required.
func FIDO2Diag(ctx context.Context, promptOut io.Writer) (*FIDO2DiagResult, error) {
	res := &FIDO2DiagResult{}
	if !isLibfido2Enabled() {
		return res, nil
	}
	res.Available = true

	// Attempt registration.
	const origin = "localhost"
	cc := &wanlib.CredentialCreation{
		Response: protocol.PublicKeyCredentialCreationOptions{
			Challenge: make([]byte, 32),
			RelyingParty: protocol.RelyingPartyEntity{
				CredentialEntity: protocol.CredentialEntity{
					Name: "localhost",
				},
				ID: "localhost",
			},
			User: protocol.UserEntity{
				CredentialEntity: protocol.CredentialEntity{
					Name: "test",
				},
				DisplayName: "test",
				ID:          []byte("test"),
			},
			Parameters: []protocol.CredentialParameter{
				{
					Type:      protocol.PublicKeyCredentialType,
					Algorithm: webauthncose.AlgES256,
				},
			},
			Attestation: protocol.PreferNoAttestation,
		},
	}
	prompt := NewDefaultPrompt(ctx, promptOut)
	ccr, err := FIDO2Register(ctx, origin, cc, prompt)
	if err != nil {
		return res, trace.Wrap(err)
	}
	res.RegisterSuccessful = true

	// Attempt login.
	assertion := &wanlib.CredentialAssertion{
		Response: protocol.PublicKeyCredentialRequestOptions{
			Challenge:      make([]byte, 32),
			RelyingPartyID: cc.Response.RelyingParty.ID,
			AllowedCredentials: []protocol.CredentialDescriptor{
				{
					Type:         protocol.PublicKeyCredentialType,
					CredentialID: ccr.GetWebauthn().GetRawId(),
				},
			},
			UserVerification: protocol.VerificationDiscouraged,
		},
	}
	prompt = NewDefaultPrompt(ctx, promptOut) // Avoid reusing prompts
	if _, _, err := FIDO2Login(ctx, origin, assertion, prompt, nil /* opts */); err != nil {
		return res, trace.Wrap(err)
	}
	res.LoginSuccessful = true

	return res, nil
}
