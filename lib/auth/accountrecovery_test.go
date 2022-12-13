/**
 * Copyright 2021 Gravitational, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package auth

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	wantypes "github.com/zmb3/teleport/api/types/webauthn"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/events/eventstest"
	"github.com/zmb3/teleport/lib/modules"
)

// TestGenerateAndUpsertRecoveryCodes tests the following:
//   - generation of recovery codes are of correct format
//   - recovery codes are upserted
//   - recovery codes can be verified and marked used
//   - reusing a used or non-existing token returns error
func TestGenerateAndUpsertRecoveryCodes(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)
	ctx := context.Background()

	user := "fake@fake.com"
	rc, err := srv.Auth().generateAndUpsertRecoveryCodes(ctx, user)
	require.NoError(t, err)
	require.Len(t, rc.Codes, numOfRecoveryCodes)
	require.NotEmpty(t, rc.Created)

	// Test codes are not marked used.
	recovery, err := srv.Auth().GetRecoveryCodes(ctx, user, true /* withSecrets */)
	require.NoError(t, err)
	for _, token := range recovery.GetCodes() {
		require.False(t, token.IsUsed)
	}

	// Test each codes are of correct format and used.
	for _, code := range rc.Codes {
		s := strings.Split(code, "-")

		// 9 b/c 1 for prefix, 8 for words.
		require.Len(t, s, 9)
		require.True(t, strings.HasPrefix(code, "tele-"))

		// Test codes match.
		err := srv.Auth().verifyRecoveryCode(ctx, user, []byte(code))
		require.NoError(t, err)
	}

	// Test used codes are marked used.
	recovery, err = srv.Auth().GetRecoveryCodes(ctx, user, true /* withSecrets */)
	require.NoError(t, err)
	for _, token := range recovery.GetCodes() {
		require.True(t, token.IsUsed)
	}

	// Test with a used code returns error.
	err = srv.Auth().verifyRecoveryCode(ctx, user, []byte(rc.Codes[0]))
	require.True(t, trace.IsAccessDenied(err))

	// Test with invalid recovery code returns error.
	err = srv.Auth().verifyRecoveryCode(ctx, user, []byte("invalidcode"))
	require.True(t, trace.IsAccessDenied(err))

	// Test with non-existing user returns error.
	err = srv.Auth().verifyRecoveryCode(ctx, "doesnotexist", []byte(rc.Codes[0]))
	require.True(t, trace.IsAccessDenied(err))
}

func TestRecoveryCodeEventsEmitted(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)
	ctx := context.Background()
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	user := "fake@fake.com"

	// Test generated recovery codes event.
	rc, err := srv.Auth().generateAndUpsertRecoveryCodes(ctx, user)
	require.NoError(t, err)
	event := mockEmitter.LastEvent()
	require.Equal(t, events.RecoveryCodeGeneratedEvent, event.GetType())
	require.Equal(t, events.RecoveryCodesGenerateCode, event.GetCode())

	// Test used recovery code event.
	err = srv.Auth().verifyRecoveryCode(ctx, user, []byte(rc.Codes[0]))
	require.NoError(t, err)
	event = mockEmitter.LastEvent()
	require.Equal(t, events.RecoveryCodeUsedEvent, event.GetType())
	require.Equal(t, events.RecoveryCodeUseSuccessCode, event.GetCode())

	// Re-using the same token emits failed event.
	err = srv.Auth().verifyRecoveryCode(ctx, user, []byte(rc.Codes[0]))
	require.Error(t, err)
	event = mockEmitter.LastEvent()
	require.Equal(t, events.RecoveryCodeUsedEvent, event.GetType())
	require.Equal(t, events.RecoveryCodeUseFailureCode, event.GetCode())
}

func TestStartAccountRecovery(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	fakeClock := srv.Clock().(clockwork.FakeClock)
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	// Test with no recover type.
	_, err = srv.Auth().StartAccountRecovery(ctx, &proto.StartAccountRecoveryRequest{
		Username:     u.username,
		RecoveryCode: []byte(u.recoveryCodes[0]),
	})
	require.Error(t, err)

	cases := []struct {
		name         string
		recoverType  types.UserTokenUsage
		recoveryCode string
	}{
		{
			name:         "request StartAccountRecovery to recover a MFA",
			recoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
			recoveryCode: u.recoveryCodes[1],
		},
		{
			name:         "request StartAccountRecovery to recover password",
			recoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD,
			recoveryCode: u.recoveryCodes[2],
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			startToken, err := srv.Auth().StartAccountRecovery(ctx, &proto.StartAccountRecoveryRequest{
				Username:     u.username,
				RecoveryCode: []byte(c.recoveryCode),
				RecoverType:  c.recoverType,
			})
			require.NoError(t, err)
			require.Equal(t, UserTokenTypeRecoveryStart, startToken.GetSubKind())
			require.Equal(t, c.recoverType, startToken.GetUsage())
			require.Equal(t, startToken.GetURL(), fmt.Sprintf("https://<proxyhost>:3080/web/recovery/steps/%s/verify", startToken.GetName()))

			// Test token returned correct byte length.
			bytes, err := hex.DecodeString(startToken.GetName())
			require.NoError(t, err)
			require.Len(t, bytes, RecoveryTokenLenBytes)

			// Test expired token.
			fakeClock.Advance(defaults.RecoveryStartTokenTTL)
			_, err = srv.Auth().GetUserToken(ctx, startToken.GetName())
			require.True(t, trace.IsNotFound(err))

			// Test events emitted.
			event := mockEmitter.LastEvent()
			require.Equal(t, event.GetType(), events.RecoveryTokenCreateEvent)
			require.Equal(t, event.GetCode(), events.RecoveryTokenCreateCode)
			require.Equal(t, event.(*apievents.UserTokenCreate).Name, u.username)
		})
	}
}

func TestStartAccountRecovery_WithLock(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	// Trigger login lock.
	triggerLoginLock(t, srv.Auth(), u.username)

	// Test recovery is still allowed after login lock.
	_, err = srv.Auth().StartAccountRecovery(ctx, &proto.StartAccountRecoveryRequest{
		Username:     u.username,
		RecoveryCode: []byte(u.recoveryCodes[0]),
		RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
	})
	require.NoError(t, err)

	// Trigger max failed recovery attempts.
	for i := 1; i <= defaults.MaxAccountRecoveryAttempts; i++ {
		_, err = srv.Auth().StartAccountRecovery(ctx, &proto.StartAccountRecoveryRequest{
			Username: u.username,
		})
		require.True(t, trace.IsAccessDenied(err))

		if i == defaults.MaxAccountRecoveryAttempts {
			require.Equal(t, MaxFailedAttemptsFromStartRecoveryErrMsg, err.Error())
		}
	}

	// Test recovery is denied from attempt recovery lock.
	_, err = srv.Auth().StartAccountRecovery(ctx, &proto.StartAccountRecoveryRequest{
		Username:     u.username,
		RecoveryCode: []byte(u.recoveryCodes[1]),
		RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
	})
	require.True(t, trace.IsAccessDenied(err))
	require.Equal(t, startRecoveryMaxFailedAttemptsErrMsg, err.Error())

	// Test locks have been placed.
	user, err := srv.Auth().GetUser(u.username, false)
	require.NoError(t, err)
	require.True(t, user.GetStatus().IsLocked)
	require.False(t, user.GetStatus().RecoveryAttemptLockExpires.IsZero())
	require.Equal(t, user.GetStatus().LockExpires, user.GetStatus().RecoveryAttemptLockExpires)
}

func TestStartAccountRecovery_UserErrors(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	cases := []struct {
		desc      string
		expErrMsg string
		req       *proto.StartAccountRecoveryRequest
	}{
		{
			desc:      "username not in valid email format",
			expErrMsg: startRecoveryGenericErrMsg,
			req: &proto.StartAccountRecoveryRequest{
				Username: "malformed-email",
			},
		},
		{
			desc:      "user does not exist",
			expErrMsg: startRecoveryBadAuthnErrMsg,
			req: &proto.StartAccountRecoveryRequest{
				Username: "dne@test.com",
			},
		},
		{
			desc:      "invalid recovery code",
			expErrMsg: startRecoveryBadAuthnErrMsg,
			req: &proto.StartAccountRecoveryRequest{
				Username:     u.username,
				RecoveryCode: []byte("invalid-code"),
			},
		},
		{
			desc:      "missing recover type in request",
			expErrMsg: startRecoveryGenericErrMsg,
			req: &proto.StartAccountRecoveryRequest{
				Username:     u.username,
				RecoveryCode: []byte(u.recoveryCodes[0]),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			_, err = srv.Auth().StartAccountRecovery(ctx, c.req)
			require.True(t, trace.IsAccessDenied(err))
			require.Equal(t, c.expErrMsg, err.Error())
		})
	}
}

func TestVerifyAccountRecovery_WithAuthnErrors(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	fakeClock := srv.Clock().(clockwork.FakeClock)
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	cases := []struct {
		name           string
		recoverType    types.UserTokenUsage
		invalidReq     *proto.VerifyAccountRecoveryRequest
		createValidReq func(*proto.MFAAuthenticateChallenge) *proto.VerifyAccountRecoveryRequest
	}{
		{
			name:        "authenticate with invalid/valid totp code",
			recoverType: types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD,
			invalidReq: &proto.VerifyAccountRecoveryRequest{
				AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{MFAAuthenticateResponse: &proto.MFAAuthenticateResponse{
					Response: &proto.MFAAuthenticateResponse_TOTP{TOTP: &proto.TOTPResponse{Code: "invalid-totp-code"}},
				}},
			},
			createValidReq: func(c *proto.MFAAuthenticateChallenge) *proto.VerifyAccountRecoveryRequest {
				mfaResp, err := u.totpDev.SolveAuthn(c)
				require.NoError(t, err)
				return &proto.VerifyAccountRecoveryRequest{
					AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{
						MFAAuthenticateResponse: mfaResp,
					},
				}
			},
		},
		{
			name:        "authenticate with invalid/valid webauthn response",
			recoverType: types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD,
			invalidReq: &proto.VerifyAccountRecoveryRequest{
				AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{
					MFAAuthenticateResponse: &proto.MFAAuthenticateResponse{
						Response: &proto.MFAAuthenticateResponse_Webauthn{
							Webauthn: &wantypes.CredentialAssertionResponse{}, // invalid response
						},
					},
				},
			},
			createValidReq: func(c *proto.MFAAuthenticateChallenge) *proto.VerifyAccountRecoveryRequest {
				mfaResp, err := u.webDev.SolveAuthn(c)
				require.NoError(t, err)
				return &proto.VerifyAccountRecoveryRequest{
					AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{
						MFAAuthenticateResponse: mfaResp,
					},
				}
			},
		},
		{
			name:        "authenticate with invalid/valid password",
			recoverType: types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
			invalidReq: &proto.VerifyAccountRecoveryRequest{
				AuthnCred: &proto.VerifyAccountRecoveryRequest_Password{Password: []byte("invalid-password")},
			},
			createValidReq: func(c *proto.MFAAuthenticateChallenge) *proto.VerifyAccountRecoveryRequest {
				return &proto.VerifyAccountRecoveryRequest{
					AuthnCred: &proto.VerifyAccountRecoveryRequest_Password{Password: u.password},
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Acquire a start token.
			startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, c.recoverType)
			require.NoError(t, err)

			// Try a failed attempt, to test it gets cleared later.
			c.invalidReq.Username = u.username
			c.invalidReq.RecoveryStartTokenID = startToken.GetName()
			_, err = srv.Auth().VerifyAccountRecovery(ctx, c.invalidReq)
			require.True(t, trace.IsAccessDenied(err))
			require.Equal(t, verifyRecoveryBadAuthnErrMsg, err.Error())

			attempts, err := srv.Auth().GetUserRecoveryAttempts(ctx, u.username)
			require.NoError(t, err)
			require.Len(t, attempts, 1)

			// Get request with authn.
			mfaChallenge, err := srv.Auth().CreateAuthenticateChallenge(ctx, &proto.CreateAuthenticateChallengeRequest{
				Request: &proto.CreateAuthenticateChallengeRequest_UserCredentials{UserCredentials: &proto.UserCredentials{
					Username: u.username,
					Password: u.password,
				}},
			})
			require.NoError(t, err)
			req := c.createValidReq(mfaChallenge)
			req.Username = u.username
			req.RecoveryStartTokenID = startToken.GetName()

			// Acquire an approval token with the start token.
			approvedToken, err := srv.Auth().VerifyAccountRecovery(ctx, req)
			require.NoError(t, err)
			require.Equal(t, UserTokenTypeRecoveryApproved, approvedToken.GetSubKind())
			require.Equal(t, c.recoverType.String(), approvedToken.GetUsage().String())

			// Test events emitted.
			event := mockEmitter.LastEvent()
			require.Equal(t, event.GetType(), events.RecoveryTokenCreateEvent)
			require.Equal(t, event.GetCode(), events.RecoveryTokenCreateCode)
			require.Equal(t, event.(*apievents.UserTokenCreate).Name, u.username)

			// Test start token got deleted.
			_, err = srv.Auth().GetUserToken(ctx, startToken.GetName())
			require.True(t, trace.IsNotFound(err))

			// Test expired token.
			fakeClock.Advance(defaults.RecoveryApprovedTokenTTL)
			_, err = srv.Auth().GetUserToken(ctx, approvedToken.GetName())
			require.True(t, trace.IsNotFound(err))

			// Test recovery attempts are deleted.
			attempts, err = srv.Auth().GetUserRecoveryAttempts(ctx, u.username)
			require.NoError(t, err)
			require.Len(t, attempts, 0)
		})
	}
}

func TestVerifyAccountRecovery_WithLock(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	// Acquire a start token.
	startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
	require.NoError(t, err)

	// Trigger login lock.
	triggerLoginLock(t, srv.Auth(), u.username)

	// Test recovery is still allowed after login lock.
	_, err = srv.Auth().VerifyAccountRecovery(ctx, &proto.VerifyAccountRecoveryRequest{
		Username:             u.username,
		RecoveryStartTokenID: startToken.GetName(),
		AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: u.password},
	})
	require.NoError(t, err)

	// Acquire another start token, as last success would have deleted it.
	startToken, err = srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
	require.NoError(t, err)

	// Trigger max failed recovery attempts.
	for i := 1; i <= defaults.MaxAccountRecoveryAttempts; i++ {
		_, err = srv.Auth().VerifyAccountRecovery(ctx, &proto.VerifyAccountRecoveryRequest{
			RecoveryStartTokenID: startToken.GetName(),
			Username:             u.username,
			AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: []byte("wrong-password")},
		})
		require.True(t, trace.IsAccessDenied(err))

		if i == defaults.MaxAccountRecoveryAttempts {
			require.Equal(t, MaxFailedAttemptsFromVerifyRecoveryErrMsg, err.Error())
		}
	}

	// Test start token is deleted from max failed attempts.
	_, err = srv.Auth().VerifyAccountRecovery(ctx, &proto.VerifyAccountRecoveryRequest{
		Username:             u.username,
		RecoveryStartTokenID: startToken.GetName(),
		AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: u.password},
	})
	require.True(t, trace.IsAccessDenied(err))

	// Test only login is locked.
	user, err := srv.Auth().GetUser(u.username, false)
	require.NoError(t, err)
	require.True(t, user.GetStatus().IsLocked)
	require.True(t, user.GetStatus().RecoveryAttemptLockExpires.IsZero())
	require.False(t, user.GetStatus().LockExpires.IsZero())

	// Test recovery attempts got reset.
	attempts, err := srv.Auth().GetUserRecoveryAttempts(ctx, u.username)
	require.NoError(t, err)
	require.Len(t, attempts, 0)
}

func TestVerifyAccountRecovery_WithErrors(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	cases := []struct {
		name       string
		expErrMsg  string
		getRequest func() *proto.VerifyAccountRecoveryRequest
	}{
		{
			name: "invalid token type",
			getRequest: func() *proto.VerifyAccountRecoveryRequest {
				// Generate an incorrect token type.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: approvedToken.GetName(),
				}
			},
		},
		{
			name:      "token not found",
			expErrMsg: verifyRecoveryGenericErrMsg,
			getRequest: func() *proto.VerifyAccountRecoveryRequest {
				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: "non-existent-token-id",
				}
			},
		},
		{
			name:      "username does not match",
			expErrMsg: verifyRecoveryBadAuthnErrMsg,
			getRequest: func() *proto.VerifyAccountRecoveryRequest {
				// Acquire a start token.
				startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startToken.GetName(),
					Username:             "invalid-username",
				}
			},
		},
		{
			name:      "provide password when it expects MFA authn response",
			expErrMsg: verifyRecoveryBadAuthnErrMsg,
			getRequest: func() *proto.VerifyAccountRecoveryRequest {
				// Acquire a start token for recovering second factor.
				startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startToken.GetName(),
					AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: []byte("some-password")},
				}
			},
		},
		{
			name:      "provide MFA authn response when it expects password",
			expErrMsg: verifyRecoveryBadAuthnErrMsg,
			getRequest: func() *proto.VerifyAccountRecoveryRequest {
				// Acquire a start token for recovering password.
				startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startToken.GetName(),
					AuthnCred:            &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{MFAAuthenticateResponse: &proto.MFAAuthenticateResponse{}},
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err = srv.Auth().VerifyAccountRecovery(ctx, c.getRequest())
			switch {
			case c.expErrMsg != "":
				require.True(t, trace.IsAccessDenied(err))
				require.Equal(t, c.expErrMsg, err.Error())
			default:
				require.True(t, trace.IsAccessDenied(err))
			}
		})
	}
}

func TestCompleteAccountRecovery(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	// Test new password with lock that should not affect changing authn.
	triggerLoginLock(t, srv.Auth(), u.username)

	// Acquire an approved token for recovering password.
	approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD)
	require.NoError(t, err)

	err = srv.Auth().CompleteAccountRecovery(ctx, &proto.CompleteAccountRecoveryRequest{
		RecoveryApprovedTokenID: approvedToken.GetName(),
		NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewPassword{NewPassword: []byte("llamas-are-cool")},
	})
	require.NoError(t, err)

	// Test locks are removed.
	user, err := srv.Auth().GetUser(u.username, false)
	require.NoError(t, err)
	require.False(t, user.GetStatus().IsLocked)
	require.True(t, user.GetStatus().RecoveryAttemptLockExpires.IsZero())
	require.True(t, user.GetStatus().LockExpires.IsZero())

	// Test login attempts are removed.
	attempts, err := srv.Auth().GetUserLoginAttempts(u.username)
	require.NoError(t, err)
	require.Len(t, attempts, 0)

	// Test adding MFA devices.
	approvedToken, err = srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
	require.NoError(t, err)

	cases := []struct {
		name       string
		getRequest func() *proto.CompleteAccountRecoveryRequest
	}{
		{
			name: "add new TOTP device",
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				res, err := srv.Auth().CreateRegisterChallenge(ctx, &proto.CreateRegisterChallengeRequest{
					TokenID:    approvedToken.GetName(),
					DeviceType: proto.DeviceType_DEVICE_TYPE_TOTP,
				})
				require.NoError(t, err)

				otpCode, err := totp.GenerateCode(res.GetTOTP().GetSecret(), srv.Clock().Now())
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					NewDeviceName:           "new-otp",
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred: &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: &proto.MFARegisterResponse{
						Response: &proto.MFARegisterResponse_TOTP{TOTP: &proto.TOTPRegisterResponse{Code: otpCode}},
					}},
				}
			},
		},
		{
			name: "add new WEBAUTHN device",
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				_, webauthnRegRes, err := getMockedWebauthnAndRegisterRes(srv.Auth(), approvedToken.GetName(), proto.DeviceUsage_DEVICE_USAGE_MFA)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					NewDeviceName:           "new-webauthn",
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: webauthnRegRes},
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := c.getRequest()

			// Change authentication.
			err = srv.Auth().CompleteAccountRecovery(ctx, req)
			require.NoError(t, err)

			// Test events emitted.
			event := mockEmitter.LastEvent()
			require.Equal(t, event.GetType(), events.MFADeviceAddEvent)
			require.Equal(t, event.GetCode(), events.MFADeviceAddEventCode)
			require.Equal(t, event.(*apievents.MFADeviceAdd).UserMetadata.User, u.username)

			// Test new device has been added.
			mfas, err := srv.Auth().Services.GetMFADevices(ctx, u.username, false)
			require.NoError(t, err)

			found := false
			for _, mfa := range mfas {
				if mfa.GetName() == req.NewDeviceName {
					found = true
					break
				}
			}
			require.True(t, found, "MFA device %q not found", req.NewDeviceName)
		})
	}
}

func TestCompleteAccountRecovery_WithErrors(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()
	mockEmitter := &eventstest.MockEmitter{}
	srv.Auth().emitter = mockEmitter

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	cases := []struct {
		name           string
		expErrMsg      string
		isDuplicate    bool
		isBadParameter bool
		getRequest     func() *proto.CompleteAccountRecoveryRequest
	}{
		{
			name: "invalid token type",
			// expectErrMsg not supplied on purpose, there is no const err message for this error.
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Generate an incorrect token type.
				startToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: startToken.GetName(),
				}
			},
		},
		{
			name:      "token not found",
			expErrMsg: completeRecoveryGenericErrMsg,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: "non-existent-token-id",
				}
			},
		},
		{
			name:      "provide new password when it expects new MFA register response",
			expErrMsg: completeRecoveryGenericErrMsg,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Acquire an approved token for recovering second factor.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewPassword{NewPassword: []byte("some-new-password")},
				}
			},
		},
		{
			name:      "provide new MFA register response when it expects new password",
			expErrMsg: completeRecoveryGenericErrMsg,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Acquire an approved token for recovering password.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: &proto.MFARegisterResponse{}},
				}
			},
		},
		{
			name:        "duplicate device name",
			isDuplicate: true,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Acquire an approved token for recovering second factor.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				// Retrieve list of devices to get the name of an existing device.
				devs, err := srv.Auth().Services.GetMFADevices(ctx, u.username, false)
				require.NoError(t, err)
				require.NotEmpty(t, devs)

				// New register response.
				_, mfaResp, err := getMockedWebauthnAndRegisterRes(srv.Auth(), approvedToken.GetName(), proto.DeviceUsage_DEVICE_USAGE_MFA)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewDeviceName:           devs[0].GetName(),
					NewAuthnCred: &proto.CompleteAccountRecoveryRequest_NewMFAResponse{
						NewMFAResponse: mfaResp,
					},
				}
			},
		},
		{
			name:           "providing TOTP fields when TOTP is not enabled by auth settings",
			isBadParameter: true,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Acquire an approved token for recovering second factor.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
					Type:         constants.Local,
					SecondFactor: constants.SecondFactorWebauthn,
					Webauthn: &types.Webauthn{
						RPID: "localhost",
					},
				})
				require.NoError(t, err)
				err = srv.Auth().SetAuthPreference(ctx, ap)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred: &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: &proto.MFARegisterResponse{
						Response: &proto.MFARegisterResponse_TOTP{},
					}},
				}
			},
		},
		{
			name:           "providing Webauthn fields when Webauthn is not enabled by auth settings",
			isBadParameter: true,
			getRequest: func() *proto.CompleteAccountRecoveryRequest {
				// Acquire an approved token for recovering second factor.
				approvedToken, err := srv.Auth().createRecoveryToken(ctx, u.username, UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
					Type:         constants.Local,
					SecondFactor: constants.SecondFactorOTP,
				})
				require.NoError(t, err)
				err = srv.Auth().SetAuthPreference(ctx, ap)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedToken.GetName(),
					NewAuthnCred: &proto.CompleteAccountRecoveryRequest_NewMFAResponse{
						NewMFAResponse: &proto.MFARegisterResponse{
							Response: &proto.MFARegisterResponse_Webauthn{
								Webauthn: &wantypes.CredentialCreationResponse{},
							},
						},
					},
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err = srv.Auth().CompleteAccountRecovery(ctx, c.getRequest())
			switch {
			case c.isDuplicate:
				require.True(t, trace.IsAlreadyExists(err))
			case c.isBadParameter:
				require.True(t, trace.IsBadParameter(err))
			case c.expErrMsg != "":
				require.True(t, trace.IsAccessDenied(err))
				require.Equal(t, c.expErrMsg, err.Error())
			default:
				require.True(t, trace.IsAccessDenied(err))
			}
		})
	}
}

// TestAccountRecoveryFlow tests the recovery flow from start to finish.
func TestAccountRecoveryFlow(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	cases := []struct {
		name               string
		getStartRequest    func(*userAuthCreds) *proto.StartAccountRecoveryRequest
		getApproveRequest  func(*userAuthCreds, *proto.MFAAuthenticateChallenge, string) *proto.VerifyAccountRecoveryRequest
		getCompleteRequest func(*userAuthCreds, string) *proto.CompleteAccountRecoveryRequest
	}{
		{
			name: "recover password with otp",
			getStartRequest: func(u *userAuthCreds) *proto.StartAccountRecoveryRequest {
				return &proto.StartAccountRecoveryRequest{
					Username:     u.username,
					RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD,
					RecoveryCode: []byte(u.recoveryCodes[0]),
				}
			},
			getApproveRequest: func(u *userAuthCreds, c *proto.MFAAuthenticateChallenge, startTokenID string) *proto.VerifyAccountRecoveryRequest {
				mfaResp, err := u.totpDev.SolveAuthn(c)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startTokenID,
					Username:             u.username,
					AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{
						MFAAuthenticateResponse: mfaResp,
					},
				}
			},
			getCompleteRequest: func(u *userAuthCreds, approvedTokenID string) *proto.CompleteAccountRecoveryRequest {
				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedTokenID,
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewPassword{NewPassword: []byte("new-password-1")},
				}
			},
		},
		{
			name: "recover password with webauthn",
			getStartRequest: func(u *userAuthCreds) *proto.StartAccountRecoveryRequest {
				return &proto.StartAccountRecoveryRequest{
					Username:     u.username,
					RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_PASSWORD,
					RecoveryCode: []byte(u.recoveryCodes[0]),
				}
			},
			getApproveRequest: func(u *userAuthCreds, c *proto.MFAAuthenticateChallenge, startTokenID string) *proto.VerifyAccountRecoveryRequest {
				mfaResp, err := u.webDev.SolveAuthn(c)
				require.NoError(t, err)

				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startTokenID,
					Username:             u.username,
					AuthnCred: &proto.VerifyAccountRecoveryRequest_MFAAuthenticateResponse{
						MFAAuthenticateResponse: mfaResp,
					},
				}
			},
			getCompleteRequest: func(u *userAuthCreds, approvedTokenID string) *proto.CompleteAccountRecoveryRequest {
				return &proto.CompleteAccountRecoveryRequest{
					RecoveryApprovedTokenID: approvedTokenID,
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewPassword{NewPassword: []byte("new-password-2")},
				}
			},
		},
		{
			name: "recover webauthn with password",
			getStartRequest: func(u *userAuthCreds) *proto.StartAccountRecoveryRequest {
				return &proto.StartAccountRecoveryRequest{
					Username:     u.username,
					RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
					RecoveryCode: []byte(u.recoveryCodes[0]),
				}
			},
			getApproveRequest: func(u *userAuthCreds, c *proto.MFAAuthenticateChallenge, startTokenID string) *proto.VerifyAccountRecoveryRequest {
				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startTokenID,
					Username:             u.username,
					AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: u.password},
				}
			},
			getCompleteRequest: func(u *userAuthCreds, approvedTokenID string) *proto.CompleteAccountRecoveryRequest {
				_, webauthnRegRes, err := getMockedWebauthnAndRegisterRes(srv.Auth(), approvedTokenID, proto.DeviceUsage_DEVICE_USAGE_MFA)
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					NewDeviceName:           "new-webauthn",
					RecoveryApprovedTokenID: approvedTokenID,
					NewAuthnCred:            &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: webauthnRegRes},
				}
			},
		},
		{
			name: "recover otp with password",
			getStartRequest: func(u *userAuthCreds) *proto.StartAccountRecoveryRequest {
				return &proto.StartAccountRecoveryRequest{
					Username:     u.username,
					RecoverType:  types.UserTokenUsage_USER_TOKEN_RECOVER_MFA,
					RecoveryCode: []byte(u.recoveryCodes[0]),
				}
			},
			getApproveRequest: func(u *userAuthCreds, c *proto.MFAAuthenticateChallenge, startTokenID string) *proto.VerifyAccountRecoveryRequest {
				return &proto.VerifyAccountRecoveryRequest{
					RecoveryStartTokenID: startTokenID,
					Username:             u.username,
					AuthnCred:            &proto.VerifyAccountRecoveryRequest_Password{Password: u.password},
				}
			},
			getCompleteRequest: func(u *userAuthCreds, approvedTokenID string) *proto.CompleteAccountRecoveryRequest {
				res, err := srv.Auth().CreateRegisterChallenge(ctx, &proto.CreateRegisterChallengeRequest{
					TokenID:    approvedTokenID,
					DeviceType: proto.DeviceType_DEVICE_TYPE_TOTP,
				})
				require.NoError(t, err)

				otpCode, err := totp.GenerateCode(res.GetTOTP().GetSecret(), srv.Clock().Now())
				require.NoError(t, err)

				return &proto.CompleteAccountRecoveryRequest{
					NewDeviceName:           "new-otp",
					RecoveryApprovedTokenID: approvedTokenID,
					NewAuthnCred: &proto.CompleteAccountRecoveryRequest_NewMFAResponse{NewMFAResponse: &proto.MFARegisterResponse{
						Response: &proto.MFARegisterResponse_TOTP{TOTP: &proto.TOTPRegisterResponse{Code: otpCode}},
					}},
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			user, err := createUserWithSecondFactors(srv)
			require.NoError(t, err)

			// Step 1: Obtain a start token.
			startToken, err := srv.Auth().StartAccountRecovery(ctx, c.getStartRequest(user))
			require.NoError(t, err)

			// Step 2: Obtain an approval token using the start token.
			mfaChallenge, err := srv.Auth().CreateAuthenticateChallenge(ctx, &proto.CreateAuthenticateChallengeRequest{
				Request: &proto.CreateAuthenticateChallengeRequest_UserCredentials{UserCredentials: &proto.UserCredentials{
					Username: user.username,
					Password: user.password,
				}},
			})
			require.NoError(t, err)
			approvedToken, err := srv.Auth().VerifyAccountRecovery(ctx, c.getApproveRequest(user, mfaChallenge, startToken.GetName()))
			require.NoError(t, err)

			// Step 3: Complete recovery with the obtained approved token.
			err = srv.Auth().CompleteAccountRecovery(ctx, c.getCompleteRequest(user, approvedToken.GetName()))
			require.NoError(t, err)
		})
	}
}

func TestGetAccountRecoveryToken(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		tokenType  string
		wantErr    bool
		getRequest func() *proto.GetAccountRecoveryTokenRequest
	}{
		{
			name:    "invalid token type",
			wantErr: true,
			getRequest: func() *proto.GetAccountRecoveryTokenRequest {
				wrongTokenType, err := srv.Auth().newUserToken(CreateUserTokenRequest{
					Name: "llama",
					TTL:  5 * time.Minute,
					Type: UserTokenTypeResetPassword,
				})
				require.NoError(t, err)

				_, err = srv.Auth().CreateUserToken(ctx, wrongTokenType)
				require.NoError(t, err)

				return &proto.GetAccountRecoveryTokenRequest{
					RecoveryTokenID: wrongTokenType.GetName(),
				}
			},
		},
		{
			name:    "token not found",
			wantErr: true,
			getRequest: func() *proto.GetAccountRecoveryTokenRequest {
				return &proto.GetAccountRecoveryTokenRequest{
					RecoveryTokenID: "token-not-found",
				}
			},
		},
		{
			name:      "recovery start token",
			tokenType: UserTokenTypeRecoveryStart,
			getRequest: func() *proto.GetAccountRecoveryTokenRequest {
				token, err := srv.Auth().createRecoveryToken(ctx, "llama", UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.GetAccountRecoveryTokenRequest{
					RecoveryTokenID: token.GetName(),
				}
			},
		},
		{
			name:      "recovery approve token",
			tokenType: UserTokenTypeRecoveryApproved,
			getRequest: func() *proto.GetAccountRecoveryTokenRequest {
				token, err := srv.Auth().createRecoveryToken(ctx, "llama", UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.GetAccountRecoveryTokenRequest{
					RecoveryTokenID: token.GetName(),
				}
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			retToken, err := srv.Auth().GetAccountRecoveryToken(ctx, c.getRequest())

			switch {
			case c.wantErr:
				require.True(t, trace.IsAccessDenied(err))
			default:
				require.NoError(t, err)
				require.Equal(t, c.tokenType, retToken.GetSubKind())
			}
		})
	}
}

func TestCreateAccountRecoveryCodes(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	// Enable second factors.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOTP,
	})
	require.NoError(t, err)

	err = srv.Auth().SetAuthPreference(ctx, ap)
	require.NoError(t, err)

	cases := []struct {
		name        string
		wantErr     bool
		forRecovery bool
		getRequest  func() *proto.CreateAccountRecoveryCodesRequest
	}{
		{
			name:    "invalid token type",
			wantErr: true,
			getRequest: func() *proto.CreateAccountRecoveryCodesRequest {
				token, err := srv.Auth().createRecoveryToken(ctx, "llama@example.com", UserTokenTypeRecoveryStart, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.CreateAccountRecoveryCodesRequest{
					TokenID: token.GetName(),
				}
			},
		},
		{
			name:    "token not found",
			wantErr: true,
			getRequest: func() *proto.CreateAccountRecoveryCodesRequest {
				return &proto.CreateAccountRecoveryCodesRequest{
					TokenID: "token-not-found",
				}
			},
		},
		{
			name:    "invalid user name",
			wantErr: true,
			getRequest: func() *proto.CreateAccountRecoveryCodesRequest {
				token, err := srv.Auth().createRecoveryToken(ctx, "invalid-username", UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.CreateAccountRecoveryCodesRequest{
					TokenID: token.GetName(),
				}
			},
		},
		{
			name:        "recovery approved token",
			forRecovery: true,
			getRequest: func() *proto.CreateAccountRecoveryCodesRequest {
				token, err := srv.Auth().createRecoveryToken(ctx, "llama@example.com", UserTokenTypeRecoveryApproved, types.UserTokenUsage_USER_TOKEN_RECOVER_MFA)
				require.NoError(t, err)

				return &proto.CreateAccountRecoveryCodesRequest{
					TokenID: token.GetName(),
				}
			},
		},
		{
			name: "privilege token",
			getRequest: func() *proto.CreateAccountRecoveryCodesRequest {
				token, err := srv.Auth().createPrivilegeToken(ctx, "llama@example.com", UserTokenTypePrivilege)
				require.NoError(t, err)

				return &proto.CreateAccountRecoveryCodesRequest{
					TokenID: token.GetName(),
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := c.getRequest()
			res, err := srv.Auth().CreateAccountRecoveryCodes(ctx, req)

			switch {
			case c.wantErr:
				require.True(t, trace.IsAccessDenied(err))

			default:
				require.NoError(t, err)
				require.Len(t, res.GetCodes(), numOfRecoveryCodes)
				require.NotEmpty(t, res.GetCreated())

				// Check token is deleted after success.
				_, err = srv.Auth().GetUserToken(ctx, req.TokenID)
				switch {
				case c.forRecovery:
					require.True(t, trace.IsNotFound(err))
				default:
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestGetAccountRecoveryCodes(t *testing.T) {
	srv := newTestTLSServer(t)
	ctx := context.Background()

	modules.SetTestModules(t, &modules.TestModules{
		TestFeatures: modules.Features{
			RecoveryCodes: true,
		},
	})

	authPreference, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOn,
		Webauthn: &types.Webauthn{
			RPID: "teleport",
		},
	})
	require.NoError(t, err)
	err = srv.Auth().SetAuthPreference(ctx, authPreference)
	require.NoError(t, err)

	u, err := createUserWithSecondFactors(srv)
	require.NoError(t, err)

	clt, err := srv.NewClient(TestUser(u.username))
	require.NoError(t, err)

	rc, err := clt.GetAccountRecoveryCodes(ctx, &proto.GetAccountRecoveryCodesRequest{})
	require.NoError(t, err)
	require.Empty(t, rc.Codes)
	require.NotEmpty(t, rc.Created)
}

func triggerLoginLock(t *testing.T, srv *Server, username string) {
	for i := 1; i <= defaults.MaxLoginAttempts; i++ {
		_, _, err := srv.authenticateUser(context.Background(), AuthenticateUserRequest{
			Username: username,
			OTP:      &OTPCreds{},
		})
		require.True(t, trace.IsAccessDenied(err))

		// Test last attempt returns locked error.
		if i == defaults.MaxLoginAttempts {
			require.Equal(t, err.Error(), MaxFailedAttemptsErrMsg)
		} else {
			require.NotEqual(t, err.Error(), MaxFailedAttemptsErrMsg)
		}
	}
}

type userAuthCreds struct {
	recoveryCodes []string
	username      string
	password      []byte

	totpDev, webDev *TestDevice
}

func createUserWithSecondFactors(srv *TestTLSServer) (*userAuthCreds, error) {
	ctx := context.Background()
	username := fmt.Sprintf("llama%v@goteleport.com", rand.Int())
	password := []byte("abc123")

	// Enable second factors.
	ap, err := types.NewAuthPreference(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOn,
		Webauthn: &types.Webauthn{
			RPID: "localhost",
		},
		// Use default Webauthn config.
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := srv.Auth().SetAuthPreference(ctx, ap); err != nil {
		return nil, trace.Wrap(err)
	}

	_, _, err = CreateUserAndRole(srv.Auth(), username, []string{username})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resetToken, err := srv.Auth().CreateResetPasswordToken(ctx, CreateUserTokenRequest{
		Name: username,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Insert a password, device, and recovery codes.
	webDev, mfaResp, err := getMockedWebauthnAndRegisterRes(srv.Auth(), resetToken.GetName(), proto.DeviceUsage_DEVICE_USAGE_MFA)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	res, err := srv.Auth().ChangeUserAuthentication(ctx, &proto.ChangeUserAuthenticationRequest{
		TokenID:                resetToken.GetName(),
		NewPassword:            password,
		NewMFARegisterResponse: mfaResp,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clt, err := srv.NewClient(TestUser(username))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	totpDev, err := RegisterTestDevice(ctx, clt, "otp-1", proto.DeviceType_DEVICE_TYPE_TOTP, webDev, WithTestDeviceClock(srv.Clock()))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &userAuthCreds{
		username:      username,
		password:      password,
		recoveryCodes: res.GetRecovery().GetCodes(),
		totpDev:       totpDev,
		webDev:        webDev,
	}, nil
}

func getMockedWebauthnAndRegisterRes(authSrv *Server, tokenID string, usage proto.DeviceUsage) (*TestDevice, *proto.MFARegisterResponse, error) {
	res, err := authSrv.CreateRegisterChallenge(context.Background(), &proto.CreateRegisterChallengeRequest{
		TokenID:     tokenID,
		DeviceType:  proto.DeviceType_DEVICE_TYPE_WEBAUTHN,
		DeviceUsage: usage,
	})
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	var dev *TestDevice
	var regRes *proto.MFARegisterResponse

	if usage == proto.DeviceUsage_DEVICE_USAGE_PASSWORDLESS {
		dev, regRes, err = NewTestDeviceFromChallenge(res, WithPasswordless())
	} else {
		dev, regRes, err = NewTestDeviceFromChallenge(res)
	}

	return dev, regRes, trace.Wrap(err)
}
