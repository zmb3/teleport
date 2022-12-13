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

package client_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pquerna/otp/totp"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/mocku2f"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
	wancli "github.com/zmb3/teleport/lib/auth/webauthncli"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/observability/tracing"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/prompt"
)

func TestTeleportClient_Login_local(t *testing.T) {
	silenceLogger(t)

	clock := clockwork.NewFakeClockAt(time.Now())
	sa := newStandaloneTeleport(t, clock)
	username := sa.Username
	password := sa.Password
	webID := sa.WebAuthnID
	device := sa.Device
	otpKey := sa.OTPKey

	// Prepare client config, it won't change throughout the test.
	cfg := client.MakeDefaultConfig()
	cfg.Stdout = io.Discard
	cfg.Stderr = io.Discard
	cfg.Stdin = &bytes.Buffer{}
	cfg.Username = username
	cfg.HostLogin = username
	cfg.AddKeysToAgent = client.AddKeysToAgentNo
	// Replace "127.0.0.1" with "localhost". The proxy address becomes the origin
	// for Webauthn requests, and Webauthn doesn't take IP addresses.
	cfg.WebProxyAddr = strings.Replace(sa.ProxyWebAddr, "127.0.0.1", "localhost", 1 /* n */)
	cfg.KeysDir = t.TempDir()
	cfg.InsecureSkipVerify = true

	// Reset functions after tests.
	oldStdin, oldWebauthn := prompt.Stdin(), *client.PromptWebauthn
	oldHasPlatformSupport := *client.HasPlatformSupport
	*client.HasPlatformSupport = func() bool {
		return true
	}
	oldHasCredentials := *client.HasTouchIDCredentials

	t.Cleanup(func() {
		prompt.SetStdin(oldStdin)
		*client.PromptWebauthn = oldWebauthn
		*client.HasPlatformSupport = oldHasPlatformSupport
		*client.HasTouchIDCredentials = oldHasCredentials
	})

	waitForCancelFn := func(ctx context.Context) (string, error) {
		<-ctx.Done() // wait for timeout
		return "", ctx.Err()
	}
	noopWebauthnFn := func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
		<-ctx.Done() // wait for timeout
		return nil, ctx.Err()
	}

	solveOTP := func(ctx context.Context) (string, error) {
		return totp.GenerateCode(otpKey, clock.Now())
	}
	solveWebauthn := func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
		car, err := device.SignAssertion(origin, assertion)
		if err != nil {
			return nil, err
		}
		return &proto.MFAAuthenticateResponse{
			Response: &proto.MFAAuthenticateResponse_Webauthn{
				Webauthn: wanlib.CredentialAssertionResponseToProto(car),
			},
		}, nil
	}
	solvePwdless := func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
		resp, err := solveWebauthn(ctx, origin, assertion, prompt)
		if err == nil {
			resp.GetWebauthn().Response.UserHandle = webID
		}
		return resp, err
	}

	const pin = "pin123"
	userPINFn := func(ctx context.Context) (string, error) {
		return pin, nil
	}
	solvePIN := func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
		// Ask and verify the PIN. Usually the authenticator would verify the PIN,
		// but we are faking it here.
		got, err := prompt.PromptPIN()
		switch {
		case err != nil:
			return nil, err
		case got != pin:
			return nil, errors.New("invalid PIN")
		}

		// Realistically, this would happen too.
		if err := prompt.PromptTouch(); err != nil {
			return nil, err
		}

		return solveWebauthn(ctx, origin, assertion, prompt)
	}

	ctx := context.Background()
	tests := []struct {
		name                    string
		secondFactor            constants.SecondFactorType
		inputReader             *prompt.FakeReader
		solveWebauthn           func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error)
		authConnector           string
		allowStdinHijack        bool
		preferOTP               bool
		hasTouchIDCredentials   bool
		authenticatorAttachment wancli.AuthenticatorAttachment
	}{
		{
			name:             "OTP device login with hijack",
			secondFactor:     constants.SecondFactorOptional,
			inputReader:      prompt.NewFakeReader().AddString(password).AddReply(solveOTP),
			solveWebauthn:    noopWebauthnFn,
			allowStdinHijack: true,
		},
		{
			name:             "Webauthn device login with hijack",
			secondFactor:     constants.SecondFactorOptional,
			inputReader:      prompt.NewFakeReader().AddString(password).AddReply(waitForCancelFn),
			solveWebauthn:    solveWebauthn,
			allowStdinHijack: true,
		},
		{
			name:             "Webauthn device with PIN and hijack", // a bit hypothetical, but _could_ happen.
			secondFactor:     constants.SecondFactorOptional,
			inputReader:      prompt.NewFakeReader().AddString(password).AddReply(waitForCancelFn).AddReply(userPINFn),
			solveWebauthn:    solvePIN,
			allowStdinHijack: true,
		},
		{
			name:         "OTP preferred",
			secondFactor: constants.SecondFactorOptional,
			inputReader:  prompt.NewFakeReader().AddString(password).AddReply(solveOTP),
			solveWebauthn: func(ctx context.Context, origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt) (*proto.MFAAuthenticateResponse, error) {
				panic("this should not be called")
			},
			preferOTP: true,
		},
		{
			name:         "Webauthn device login",
			secondFactor: constants.SecondFactorOptional,
			inputReader: prompt.NewFakeReader().
				AddString(password).
				AddReply(func(ctx context.Context) (string, error) {
					panic("this should not be called")
				}),
			solveWebauthn: solveWebauthn,
		},
		{
			name:          "passwordless login",
			secondFactor:  constants.SecondFactorOptional,
			inputReader:   prompt.NewFakeReader(), // no inputs
			solveWebauthn: solvePwdless,
			authConnector: constants.PasswordlessConnector,
		},
		{
			name:                  "default to passwordless if registered",
			secondFactor:          constants.SecondFactorOptional,
			inputReader:           prompt.NewFakeReader(), // no inputs
			solveWebauthn:         solvePwdless,
			authConnector:         constants.LocalConnector,
			hasTouchIDCredentials: true,
		},
		{
			name:         "cross-platform attachment doesn't default to passwordless",
			secondFactor: constants.SecondFactorOptional,
			inputReader: prompt.NewFakeReader().
				AddString(password).
				AddReply(func(ctx context.Context) (string, error) {
					panic("this should not be called")
				}),
			solveWebauthn:           solveWebauthn,
			authConnector:           constants.LocalConnector,
			hasTouchIDCredentials:   true,
			authenticatorAttachment: wancli.AttachmentCrossPlatform,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			prompt.SetStdin(test.inputReader)
			*client.PromptWebauthn = func(
				ctx context.Context,
				origin string, assertion *wanlib.CredentialAssertion, prompt wancli.LoginPrompt, _ *wancli.LoginOpts,
			) (*proto.MFAAuthenticateResponse, string, error) {
				resp, err := test.solveWebauthn(ctx, origin, assertion, prompt)
				return resp, "", err
			}

			*client.HasTouchIDCredentials = func(rpid, user string) bool {
				return test.hasTouchIDCredentials
			}
			authServer := sa.Auth.GetAuthServer()
			pref, err := authServer.GetAuthPreference(ctx)
			require.NoError(t, err)
			if pref.GetSecondFactor() != test.secondFactor {
				pref.SetSecondFactor(test.secondFactor)
				require.NoError(t, authServer.SetAuthPreference(ctx, pref))
			}

			tc, err := client.NewClient(cfg)
			require.NoError(t, err)
			tc.AllowStdinHijack = test.allowStdinHijack
			tc.AuthConnector = test.authConnector
			tc.PreferOTP = test.preferOTP
			tc.AuthenticatorAttachment = test.authenticatorAttachment

			clock.Advance(30 * time.Second)
			_, err = tc.Login(ctx)
			require.NoError(t, err)
		})
	}
}

// TestTeleportClient_PromptMFAChallenge tests logic specific to the
// TeleportClient's wrapper of PromptMFAChallenge.
// Actual prompt and login behavior is tested by TestTeleportClient_Login_local.
func TestTeleportClient_PromptMFAChallenge(t *testing.T) {
	oldPromptStandalone := client.PromptMFAStandalone
	t.Cleanup(func() {
		client.PromptMFAStandalone = oldPromptStandalone
	})

	const proxy1 = "proxy1.goteleport.com"
	const proxy2 = "proxy2.goteleport.com"

	defaultClient := &client.TeleportClient{
		Config: client.Config{
			WebProxyAddr: proxy1,
			// MFA opts.
			AuthenticatorAttachment: wancli.AttachmentAuto,
			PreferOTP:               false,
			Tracer:                  tracing.NoopProvider().Tracer("test"),
		},
	}

	// client with non-default MFA options.
	opinionatedClient := &client.TeleportClient{
		Config: client.Config{
			WebProxyAddr: proxy1,
			// MFA opts.
			AuthenticatorAttachment: wancli.AttachmentCrossPlatform,
			PreferOTP:               true,
			Tracer:                  tracing.NoopProvider().Tracer("test"),
		},
	}

	// challenge contents not relevant for test
	challenge := &proto.MFAAuthenticateChallenge{}

	customizedOpts := &client.PromptMFAChallengeOpts{
		HintBeforePrompt:        "some hint explaining the imminent prompt",
		PromptDevicePrefix:      "llama",
		Quiet:                   true,
		AllowStdinHijack:        true,
		AuthenticatorAttachment: wancli.AttachmentPlatform,
		PreferOTP:               true,
	}

	ctx := context.Background()
	tests := []struct {
		name      string
		tc        *client.TeleportClient
		proxyAddr string
		applyOpts func(*client.PromptMFAChallengeOpts)
		wantProxy string
		wantOpts  *client.PromptMFAChallengeOpts
	}{
		{
			name:      "default TeleportClient",
			tc:        defaultClient,
			wantProxy: defaultClient.WebProxyAddr,
			wantOpts: &client.PromptMFAChallengeOpts{
				AuthenticatorAttachment: defaultClient.AuthenticatorAttachment,
				PreferOTP:               defaultClient.PreferOTP,
			},
		},
		{
			name:      "opinionated TeleportClient",
			tc:        opinionatedClient,
			wantProxy: opinionatedClient.WebProxyAddr,
			wantOpts: &client.PromptMFAChallengeOpts{
				AuthenticatorAttachment: opinionatedClient.AuthenticatorAttachment,
				PreferOTP:               opinionatedClient.PreferOTP,
			},
		},
		{
			name:      "custom proxyAddr and options",
			tc:        defaultClient,
			proxyAddr: proxy2,
			applyOpts: func(opts *client.PromptMFAChallengeOpts) {
				*opts = *customizedOpts
			},
			wantProxy: proxy2,
			wantOpts:  customizedOpts,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			promptCalled := false
			*client.PromptMFAStandalone = func(
				gotCtx context.Context, gotChallenge *proto.MFAAuthenticateChallenge, gotProxy string,
				gotOpts *client.PromptMFAChallengeOpts,
			) (*proto.MFAAuthenticateResponse, error) {
				promptCalled = true
				assert.Equal(t, challenge, gotChallenge, "challenge mismatch")
				assert.Equal(t, test.wantProxy, gotProxy, "proxy mismatch")
				assert.Equal(t, test.wantOpts, gotOpts, "opts mismatch")
				return &proto.MFAAuthenticateResponse{}, nil
			}

			_, err := test.tc.PromptMFAChallenge(ctx, test.proxyAddr, challenge, test.applyOpts)
			require.NoError(t, err, "PromptMFAChallenge errored")
			require.True(t, promptCalled, "Mocked PromptMFAStandlone not called")
		})
	}
}

type standaloneBundle struct {
	AuthAddr, ProxyWebAddr string
	Username, Password     string
	WebAuthnID             []byte
	Device                 *mocku2f.Key
	OTPKey                 string
	Auth, Proxy            *service.TeleportProcess
}

// TODO(codingllama): Consider refactoring newStandaloneTeleport into a public
//
//	function and reusing in other places.
func newStandaloneTeleport(t *testing.T, clock clockwork.Clock) *standaloneBundle {
	randomAddr := utils.NetAddr{AddrNetwork: "tcp", Addr: "127.0.0.1:0"}

	// Silent logger and console.
	logger := utils.NewLoggerForTests()
	logger.SetLevel(log.PanicLevel)
	logger.SetOutput(io.Discard)
	console := io.Discard

	staticToken := uuid.New().String()

	user, err := types.NewUser("llama")
	require.NoError(t, err)
	role, err := types.NewRoleV3(user.GetName(), types.RoleSpecV5{
		Allow: types.RoleConditions{
			Logins: []string{user.GetName()},
		},
	})
	require.NoError(t, err)

	// AuthServer setup.
	cfg := service.MakeDefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Hostname = "localhost"
	cfg.Clock = clock
	cfg.Console = console
	cfg.Log = logger
	cfg.SetAuthServerAddress(randomAddr) // must be present
	cfg.Auth.Preference, err = types.NewAuthPreferenceFromConfigFile(types.AuthPreferenceSpecV2{
		Type:         constants.Local,
		SecondFactor: constants.SecondFactorOptional,
		Webauthn: &types.Webauthn{
			RPID: "localhost",
		},
	})
	require.NoError(t, err)
	cfg.Auth.Resources = []types.Resource{user, role}
	cfg.Auth.StaticTokens, err = types.NewStaticTokens(types.StaticTokensSpecV2{
		StaticTokens: []types.ProvisionTokenV1{
			{
				Roles:   []types.SystemRole{types.RoleProxy},
				Expires: time.Now().Add(1 * time.Hour),
				Token:   staticToken,
			},
		},
	})
	require.NoError(t, err)
	cfg.Auth.StorageConfig.Params = backend.Params{defaults.BackendPath: filepath.Join(cfg.DataDir, defaults.BackendDir)}
	cfg.Auth.ListenAddr = randomAddr
	cfg.Proxy.Enabled = false
	cfg.SSH.Enabled = false
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	authProcess := startAndWait(t, cfg, service.AuthTLSReady)
	t.Cleanup(func() { authProcess.Close() })
	authAddr, err := authProcess.AuthAddr()
	require.NoError(t, err)

	// Use the same clock on AuthServer, it doesn't appear to cascade from
	// configs.
	authServer := authProcess.GetAuthServer()
	authServer.SetClock(clock)

	// Initialize user's password and MFA.
	ctx := context.Background()
	username := user.GetName()
	const password = "supersecretpassword"
	token, err := authServer.CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: username,
	})
	require.NoError(t, err)
	tokenID := token.GetName()
	res, err := authServer.CreateRegisterChallenge(ctx, &proto.CreateRegisterChallengeRequest{
		TokenID:     tokenID,
		DeviceType:  proto.DeviceType_DEVICE_TYPE_WEBAUTHN,
		DeviceUsage: proto.DeviceUsage_DEVICE_USAGE_PASSWORDLESS,
	})
	require.NoError(t, err)
	cc := wanlib.CredentialCreationFromProto(res.GetWebauthn())
	webID := cc.Response.User.ID
	device, err := mocku2f.Create()
	require.NoError(t, err)
	device.SetPasswordless()
	const origin = "https://localhost"
	ccr, err := device.SignCredentialCreation(origin, cc)
	require.NoError(t, err)
	_, err = authServer.ChangeUserAuthentication(ctx, &proto.ChangeUserAuthenticationRequest{
		TokenID:     tokenID,
		NewPassword: []byte(password),
		NewMFARegisterResponse: &proto.MFARegisterResponse{
			Response: &proto.MFARegisterResponse_Webauthn{
				Webauthn: wanlib.CredentialCreationResponseToProto(ccr),
			},
		},
	})
	require.NoError(t, err)

	// Insert an OTP device.
	otpKey := base32.StdEncoding.EncodeToString([]byte("llamasrule"))
	otpDevice, err := services.NewTOTPDevice("otp", otpKey, clock.Now() /* addedAt */)
	require.NoError(t, err)
	require.NoError(t, authServer.UpsertMFADevice(ctx, username, otpDevice))

	// Proxy setup.
	cfg = service.MakeDefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Hostname = "localhost"
	cfg.SetToken(staticToken)
	cfg.Clock = clock
	cfg.Console = console
	cfg.Log = logger
	cfg.SetAuthServerAddress(*authAddr)
	cfg.Auth.Enabled = false
	cfg.Proxy.Enabled = true
	cfg.Proxy.WebAddr = randomAddr
	cfg.Proxy.SSHAddr = randomAddr
	cfg.Proxy.ReverseTunnelListenAddr = randomAddr
	cfg.Proxy.DisableWebInterface = true
	cfg.SSH.Enabled = false
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()
	proxyProcess := startAndWait(t, cfg, service.ProxyWebServerReady)
	t.Cleanup(func() { proxyProcess.Close() })
	proxyWebAddr, err := proxyProcess.ProxyWebAddr()
	require.NoError(t, err)

	return &standaloneBundle{
		AuthAddr:     authAddr.String(),
		ProxyWebAddr: proxyWebAddr.String(),
		Username:     username,
		Password:     password,
		WebAuthnID:   webID,
		Device:       device,
		OTPKey:       otpKey,
		Auth:         authProcess,
		Proxy:        proxyProcess,
	}
}

func startAndWait(t *testing.T, cfg *service.Config, eventName string) *service.TeleportProcess {
	instance, err := service.NewTeleport(cfg)
	require.NoError(t, err)
	require.NoError(t, instance.Start())

	_, err = instance.WaitForEventTimeout(30*time.Second, eventName)
	require.NoError(t, err, "timed out waiting for teleport")

	return instance
}

// silenceLogger silences logger during testing.
func silenceLogger(t *testing.T) {
	lvl := log.GetLevel()
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetLevel(lvl)
	})
	log.SetOutput(io.Discard)
	log.SetLevel(log.PanicLevel)
}
