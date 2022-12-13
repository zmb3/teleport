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

package webauthn_test

import (
	"context"
	"crypto/x509"
	"fmt"
	"testing"
	"time"

	"github.com/duo-labs/webauthn/protocol"
	"github.com/gogo/protobuf/proto"
	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/types"
	wantypes "github.com/zmb3/teleport/api/types/webauthn"
	"github.com/zmb3/teleport/lib/auth/mocku2f"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
)

func TestLoginFlow_BeginFinish(t *testing.T) {
	// Simulate a previously registered U2F device.
	u2fKey, err := mocku2f.Create()
	require.NoError(t, err)
	u2fKey.SetCounter(10)                          // Arbitrary
	devAddedAt := time.Now().Add(-5 * time.Minute) // Make sure devAddedAt is in the past.
	u2fDev, err := keyToMFADevice(u2fKey, devAddedAt /* addedAt */, devAddedAt /* lastUsed */)
	require.NoError(t, err)

	// U2F user has a legacy device and no webID.
	const u2fUser = "alpaca"
	u2fIdentity := newFakeIdentity(u2fUser, u2fDev)

	// webUser gets a newly registered device and a webID.
	const webUser = "llama"
	webIdentity := newFakeIdentity(webUser)

	u2fConfig := &types.U2F{AppID: "https://example.com:3080"}
	webConfig := &types.Webauthn{RPID: "example.com"}

	const u2fOrigin = "https://example.com:3080"
	const webOrigin = "https://example.com"
	ctx := context.Background()

	// Register a Webauthn device.
	// Last registration step creates the user webID and adds the new device to
	// identity.
	webKey, err := mocku2f.Create()
	require.NoError(t, err)
	webKey.PreferRPID = true // Webauthn-registered device
	webKey.SetCounter(20)    // Arbitrary, recorded during registration
	webRegistration := &wanlib.RegistrationFlow{
		Webauthn: webConfig,
		Identity: webIdentity,
	}
	cc, err := webRegistration.Begin(ctx, webUser, false /* passwordless */)
	require.NoError(t, err)
	ccr, err := webKey.SignCredentialCreation(webOrigin, cc)
	require.NoError(t, err)
	_, err = webRegistration.Finish(ctx, wanlib.RegisterResponse{
		User:             webUser,
		DeviceName:       "webauthn1",
		CreationResponse: ccr,
	})
	require.NoError(t, err)

	tests := []struct {
		name         string
		identity     *fakeIdentity
		user, origin string
		key          *mocku2f.Key
		wantWebID    bool
	}{
		{
			name:     "OK U2F device login",
			identity: u2fIdentity,
			user:     u2fUser,
			origin:   u2fOrigin,
			key:      u2fKey,
		},
		{
			name:      "OK Webauthn device login",
			identity:  webIdentity,
			user:      webUser,
			origin:    webOrigin,
			key:       webKey,
			wantWebID: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := test.identity
			user := test.user

			webLogin := &wanlib.LoginFlow{
				U2F:      u2fConfig,
				Webauthn: webConfig,
				Identity: test.identity,
			}

			// 1st step of the login ceremony.
			assertion, err := webLogin.Begin(ctx, user)
			require.NoError(t, err)
			// We care about a few specific settings, for everything else defaults are
			// OK.
			require.Equal(t, webConfig.RPID, assertion.Response.RelyingPartyID)
			require.Equal(t, u2fConfig.AppID, assertion.Response.Extensions["appid"])
			require.Equal(t, protocol.VerificationDiscouraged, assertion.Response.UserVerification)
			// Did we record the SessionData in storage?
			require.Len(t, identity.SessionData, 1)
			// Did we record the web ID in the SessionData?
			var sd *wantypes.SessionData
			for _, v := range identity.SessionData {
				sd = v // Retrieve without guessing the key
				break
			}
			if test.wantWebID {
				require.NotEmpty(t, sd.UserId)
			} else {
				require.Empty(t, sd.UserId)
			}

			// User interaction would happen here.
			wantCounter := test.key.Counter()
			assertionResp, err := test.key.SignAssertion(test.origin, assertion)
			require.NoError(t, err)

			// 2nd and last step of the login ceremony.
			beforeLastUsed := time.Now().Add(-1 * time.Second)
			loginDevice, err := webLogin.Finish(ctx, user, assertionResp)
			require.NoError(t, err)
			// Last used time and counter are updated.
			require.True(t, beforeLastUsed.Before(loginDevice.LastUsed))
			require.Equal(t, wantCounter, getSignatureCounter(loginDevice))
			// Did we update the device in storage?
			require.NotEmpty(t, identity.UpdatedDevices)
			got := identity.UpdatedDevices[len(identity.UpdatedDevices)-1]
			if diff := cmp.Diff(loginDevice, got); diff != "" {
				t.Errorf("Updated device mismatch (-want +got):\n%s", diff)
			}
			// Did we delete the challenge?
			require.Empty(t, identity.SessionData)
		})
	}
}

func keyToMFADevice(dev *mocku2f.Key, addedAt, lastUsed time.Time) (*types.MFADevice, error) {
	pubKeyDER, err := x509.MarshalPKIXPublicKey(&dev.PrivateKey.PublicKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &types.MFADevice{
		AddedAt:  addedAt,
		LastUsed: lastUsed,
		Device: &types.MFADevice_U2F{
			U2F: &types.U2FDevice{
				KeyHandle: dev.KeyHandle,
				PubKey:    pubKeyDER,
				Counter:   dev.Counter(),
			},
		},
	}, nil
}

func getSignatureCounter(dev *types.MFADevice) uint32 {
	switch d := dev.Device.(type) {
	case *types.MFADevice_U2F:
		return d.U2F.Counter
	case *types.MFADevice_Webauthn:
		return d.Webauthn.SignatureCounter
	default:
		return 0
	}
}

func TestLoginFlow_Begin_errors(t *testing.T) {
	const user = "llama"
	webLogin := wanlib.LoginFlow{
		Webauthn: &types.Webauthn{RPID: "localhost"},
		Identity: newFakeIdentity(user),
	}

	ctx := context.Background()
	tests := []struct {
		name          string
		user          string
		assertErrType func(error) bool
		wantErr       string
	}{
		{
			name:          "NOK empty user",
			assertErrType: trace.IsBadParameter,
			wantErr:       "user required",
		},
		{
			name:          "NOK no registered devices",
			user:          user,
			assertErrType: trace.IsNotFound,
			wantErr:       "no credentials",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := webLogin.Begin(ctx, test.user)
			require.True(t, test.assertErrType(err), "got err = %v, want BadParameter", err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestLoginFlow_Finish_errors(t *testing.T) {
	ctx := context.Background()
	const user = "llama"
	const webOrigin = "https://localhost"

	webConfig := &types.Webauthn{RPID: "localhost"}
	identity := newFakeIdentity(user)
	webRegistration := &wanlib.RegistrationFlow{
		Webauthn: webConfig,
		Identity: identity,
	}

	key, err := mocku2f.Create()
	require.NoError(t, err)
	key.PreferRPID = true
	cc, err := webRegistration.Begin(ctx, user, false /* passwordless */)
	require.NoError(t, err)
	ccr, err := key.SignCredentialCreation(webOrigin, cc)
	require.NoError(t, err)
	_, err = webRegistration.Finish(ctx, wanlib.RegisterResponse{
		User:             user,
		DeviceName:       "webauthn1",
		CreationResponse: ccr,
	})
	require.NoError(t, err)

	webLogin := wanlib.LoginFlow{
		U2F:      &types.U2F{AppID: "https://example.com"},
		Webauthn: webConfig,
		Identity: identity,
	}
	assertion, err := webLogin.Begin(ctx, user)
	require.NoError(t, err)
	okResp, err := key.SignAssertion(webOrigin, assertion)
	require.NoError(t, err)

	tests := []struct {
		name       string
		user       string
		createResp func() *wanlib.CredentialAssertionResponse
	}{
		{
			name:       "NOK empty user",
			user:       "",
			createResp: func() *wanlib.CredentialAssertionResponse { return okResp },
		},
		{
			name:       "NOK nil resp",
			user:       user,
			createResp: func() *wanlib.CredentialAssertionResponse { return nil },
		},
		{
			name:       "NOK empty resp",
			user:       user,
			createResp: func() *wanlib.CredentialAssertionResponse { return &wanlib.CredentialAssertionResponse{} },
		},
		{
			name: "NOK assertion with bad origin",
			user: user,
			createResp: func() *wanlib.CredentialAssertionResponse {
				assertion, err := webLogin.Begin(ctx, user)
				require.NoError(t, err)
				resp, err := key.SignAssertion("https://badorigin.com", assertion)
				require.NoError(t, err)
				return resp
			},
		},
		{
			name: "NOK assertion with bad RPID",
			user: user,
			createResp: func() *wanlib.CredentialAssertionResponse {
				assertion, err := webLogin.Begin(ctx, user)
				require.NoError(t, err)
				assertion.Response.RelyingPartyID = "badrpid.com"

				resp, err := key.SignAssertion(webOrigin, assertion)
				require.NoError(t, err)
				return resp
			},
		},
		{
			name: "NOK assertion signed by unknown device",
			user: user,
			createResp: func() *wanlib.CredentialAssertionResponse {
				assertion, err := webLogin.Begin(ctx, user)
				require.NoError(t, err)

				unknownKey, err := mocku2f.Create()
				require.NoError(t, err)
				unknownKey.PreferRPID = true
				unknownKey.IgnoreAllowedCredentials = true

				resp, err := unknownKey.SignAssertion(webOrigin, assertion)
				require.NoError(t, err)
				return resp
			},
		},
		{
			name: "NOK assertion with invalid signature",
			user: user,
			createResp: func() *wanlib.CredentialAssertionResponse {
				assertion, err := webLogin.Begin(ctx, user)
				require.NoError(t, err)
				// Flip a challenge bit, this should be enough to consistently fail
				// signature checking.
				assertion.Response.Challenge[0] = 1 ^ assertion.Response.Challenge[0]

				resp, err := key.SignAssertion(webOrigin, assertion)
				require.NoError(t, err)
				return resp
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := webLogin.Finish(ctx, test.user, test.createResp())
			require.Error(t, err)
		})
	}
}

func TestPasswordlessFlow_BeginAndFinish(t *testing.T) {
	// Prepare identity and configs.
	const user = "llama"
	identity := newFakeIdentity(user)
	webConfig := &types.Webauthn{RPID: "example.com"}

	const webOrigin = "https://example.com"
	ctx := context.Background()

	// Register a Webauthn device.
	// Last registration step adds the created device to identity.
	webKey, err := mocku2f.Create()
	require.NoError(t, err)
	webKey.IgnoreAllowedCredentials = true // Allowed credentials will be empty
	webKey.SetUV = true                    // Required for passwordless
	webKey.AllowResidentKey = true         // Required for passwordless
	webRegistration := &wanlib.RegistrationFlow{
		Webauthn: webConfig,
		Identity: identity,
	}
	cc, err := webRegistration.Begin(ctx, user, true /* passwordless */)
	require.NoError(t, err)
	ccr, err := webKey.SignCredentialCreation(webOrigin, cc)
	require.NoError(t, err)
	_, err = webRegistration.Finish(ctx, wanlib.RegisterResponse{
		User:             user,
		DeviceName:       "webauthn1",
		CreationResponse: ccr,
		Passwordless:     true,
	})
	require.NoError(t, err)

	webLogin := &wanlib.PasswordlessFlow{
		Webauthn: webConfig,
		Identity: identity,
	}

	tests := []struct {
		name   string
		origin string
		key    *mocku2f.Key
		user   string
	}{
		{
			name:   "OK",
			origin: webOrigin,
			key:    webKey,
			user:   user,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// 1st step of the login ceremony.
			assertion, err := webLogin.Begin(ctx)
			require.NoError(t, err)

			// Verify that passwordless settings are correct.
			require.Empty(t, assertion.Response.AllowedCredentials)
			require.Equal(t, protocol.VerificationRequired, assertion.Response.UserVerification)

			// Verify that we recorded user verification requirements in storage.
			require.Len(t, identity.SessionData, 1)
			var sd *wantypes.SessionData
			for _, v := range identity.SessionData {
				sd = v // Get SessionData without guessing the key.
				break
			}
			wantSD := &wantypes.SessionData{
				Challenge:        sd.Challenge,
				UserId:           nil,   // aka unset
				AllowCredentials: nil,   // aka unset
				ResidentKey:      false, // irrelevant for login
				UserVerification: string(protocol.VerificationRequired),
			}
			if !proto.Equal(sd, wantSD) {
				diff := cmp.Diff(wantSD, sd)
				t.Fatalf("SessionData mismatch (-want +got):\n%s", diff)
			}

			// User interaction would happen here.
			assertionResp, err := test.key.SignAssertion(test.origin, assertion)
			require.NoError(t, err)
			// Fetch the stored user handle; in a real-world the scenario the
			// authenticator knows it, as passwordless requires a resident credential.
			wla, err := identity.GetWebauthnLocalAuth(ctx, test.user)
			require.NoError(t, err)
			assertionResp.AssertionResponse.UserHandle = wla.UserID

			// 2nd and last step of the login ceremony.
			mfaDevice, user, err := webLogin.Finish(ctx, assertionResp)
			require.NoError(t, err)
			require.NotNil(t, mfaDevice)
			require.Equal(t, test.user, user)
		})
	}
}

func TestPasswordlessFlow_Finish_errors(t *testing.T) {
	const user = "llama"
	const webOrigin = "https://example.com"
	identity := newFakeIdentity(user)
	webConfig := &types.Webauthn{RPID: "example.com"}

	// webKey is an unregistered device.
	webKey, err := mocku2f.Create()
	require.NoError(t, err)
	webKey.IgnoreAllowedCredentials = true // Allowed credentials will be empty
	webKey.SetUV = true                    // Required for passwordless

	ctx := context.Background()
	webLogin := &wanlib.PasswordlessFlow{
		Webauthn: webConfig,
		Identity: identity,
	}

	// Prepare a signed assertion response. The response would be accepted if
	// webKey was previously registered.
	assertion, err := webLogin.Begin(ctx)
	require.NoError(t, err)
	assertionResp, err := webKey.SignAssertion(webOrigin, assertion)
	require.NoError(t, err)

	tests := []struct {
		name          string
		createResp    func() *wanlib.CredentialAssertionResponse
		assertErrType func(error) bool
		wantErrMsg    string
	}{
		{
			name: "NOK response without UserID",
			createResp: func() *wanlib.CredentialAssertionResponse {
				// UserHandle is already nil on assertionResp
				return assertionResp
			},
			assertErrType: trace.IsBadParameter,
			wantErrMsg:    "user handle required",
		},
		{
			name: "NOK unknown user handle",
			createResp: func() *wanlib.CredentialAssertionResponse {
				unknownHandle := make([]byte, 10 /* arbitrary */)
				cp := *assertionResp
				cp.AssertionResponse.UserHandle = unknownHandle
				return &cp
			},
			assertErrType: trace.IsNotFound,
			wantErrMsg:    "not found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := webLogin.Finish(ctx, test.createResp())
			require.True(t, test.assertErrType(err), "assertErrType failed, err = %v", err)
			require.Contains(t, err.Error(), test.wantErrMsg)
		})
	}
}

type fakeIdentity struct {
	User *types.UserV2
	// MappedUser is used as the reply to GetTeleportUserByWebauthnID.
	// It's automatically assigned when UpsertWebauthnLocalAuth is called.
	MappedUser     string
	UpdatedDevices []*types.MFADevice
	SessionData    map[string]*wantypes.SessionData
}

func newFakeIdentity(user string, devices ...*types.MFADevice) *fakeIdentity {
	return &fakeIdentity{
		User: &types.UserV2{
			Metadata: types.Metadata{
				Name: user,
			},
			Spec: types.UserSpecV2{
				LocalAuth: &types.LocalAuthSecrets{
					MFA: devices,
				},
			},
		},
		SessionData: make(map[string]*wantypes.SessionData),
	}
}

func (f *fakeIdentity) GetMFADevices(ctx context.Context, user string, withSecrets bool) ([]*types.MFADevice, error) {
	return f.User.GetLocalAuth().MFA, nil
}

func (f *fakeIdentity) UpsertMFADevice(ctx context.Context, user string, d *types.MFADevice) error {
	f.UpdatedDevices = append(f.UpdatedDevices, d)

	// Is this an update?
	for i, dev := range f.User.GetLocalAuth().MFA {
		if dev.Id == d.Id {
			f.User.GetLocalAuth().MFA[i] = dev
			return nil
		}
	}

	// Insert new device.
	f.User.GetLocalAuth().MFA = append(f.User.GetLocalAuth().MFA, d)
	return nil
}

func (f *fakeIdentity) UpsertWebauthnLocalAuth(ctx context.Context, user string, wla *types.WebauthnLocalAuth) error {
	f.User.GetLocalAuth().Webauthn = wla
	f.MappedUser = user
	return nil
}

func (f *fakeIdentity) GetWebauthnLocalAuth(ctx context.Context, user string) (*types.WebauthnLocalAuth, error) {
	wla := f.User.GetLocalAuth().Webauthn
	if wla == nil {
		return nil, trace.NotFound("not found")
	}
	return wla, nil
}

func (f *fakeIdentity) GetTeleportUserByWebauthnID(ctx context.Context, webID []byte) (string, error) {
	if f.MappedUser == "" {
		return "", trace.NotFound("not found")
	}
	return f.MappedUser, nil
}

func (f *fakeIdentity) UpsertWebauthnSessionData(ctx context.Context, user, sessionID string, sd *wantypes.SessionData) error {
	f.SessionData[sessionDataKey(user, sessionID)] = sd
	return nil
}

func (f *fakeIdentity) GetWebauthnSessionData(ctx context.Context, user, sessionID string) (*wantypes.SessionData, error) {
	sd, ok := f.SessionData[sessionDataKey(user, sessionID)]
	if !ok {
		return nil, trace.NotFound("not found")
	}
	return sd, nil
}

func (f *fakeIdentity) DeleteWebauthnSessionData(ctx context.Context, user, sessionID string) error {
	delete(f.SessionData, sessionDataKey(user, sessionID))
	return nil
}

func sessionDataKey(user string, sessionID string) string {
	return fmt.Sprintf("user/%v/%v", user, sessionID)
}

func (f *fakeIdentity) UpsertGlobalWebauthnSessionData(ctx context.Context, scope, id string, sd *wantypes.SessionData) error {
	f.SessionData[globalSessionDataKey(scope, id)] = sd
	return nil
}

func (f *fakeIdentity) GetGlobalWebauthnSessionData(ctx context.Context, scope, id string) (*wantypes.SessionData, error) {
	sd, ok := f.SessionData[globalSessionDataKey(scope, id)]
	if !ok {
		return nil, trace.NotFound("not found")
	}
	return sd, nil
}

func (f *fakeIdentity) DeleteGlobalWebauthnSessionData(ctx context.Context, scope, id string) error {
	delete(f.SessionData, globalSessionDataKey(scope, id))
	return nil
}

func globalSessionDataKey(scope string, id string) string {
	return fmt.Sprintf("global/%v/%v", scope, id)
}
