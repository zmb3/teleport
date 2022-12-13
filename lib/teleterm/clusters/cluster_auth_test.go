/**
 * Copyright 2022 Gravitational, Inc.
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

package clusters

import (
	"context"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	wancli "github.com/zmb3/teleport/lib/auth/webauthncli"
	api "github.com/zmb3/teleport/lib/teleterm/api/protogen/golang/v1"
)

func TestPwdlessLoginPrompt_PromptPIN(t *testing.T) {
	stream := &mockLoginPwdlessStream{}

	// Test valid pin.
	stream.assertResp = func(res *api.LoginPasswordlessResponse) error {
		require.Equal(t, api.PasswordlessPrompt_PASSWORDLESS_PROMPT_PIN, res.Prompt)
		return nil
	}
	stream.serverReq = func() (*api.LoginPasswordlessRequest, error) {
		return &api.LoginPasswordlessRequest{Request: &api.LoginPasswordlessRequest_Pin{
			Pin: &api.LoginPasswordlessRequest_LoginPasswordlessPINResponse{
				Pin: "1234"},
		}}, nil
	}

	prompt := newPwdlessLoginPrompt(context.Background(), stream)
	pin, err := prompt.PromptPIN()
	require.NoError(t, err)
	require.Equal(t, "1234", pin)

	// Test invalid pin.
	stream.serverReq = func() (*api.LoginPasswordlessRequest, error) {
		return &api.LoginPasswordlessRequest{Request: &api.LoginPasswordlessRequest_Pin{
			Pin: &api.LoginPasswordlessRequest_LoginPasswordlessPINResponse{
				Pin: ""},
		}}, nil
	}

	_, err = prompt.PromptPIN()
	require.True(t, trace.IsBadParameter(err))
}

func TestPwdlessLoginPrompt_PromptTouch(t *testing.T) {
	stream := &mockLoginPwdlessStream{}

	stream.assertResp = func(res *api.LoginPasswordlessResponse) error {
		require.Equal(t, api.PasswordlessPrompt_PASSWORDLESS_PROMPT_TAP, res.Prompt)
		return nil
	}

	prompt := newPwdlessLoginPrompt(context.Background(), stream)
	err := prompt.PromptTouch()
	require.NoError(t, err)
}

func TestPwdlessLoginPrompt_PromptCredential(t *testing.T) {
	stream := &mockLoginPwdlessStream{}

	unsortedCreds := []*wancli.CredentialInfo{
		{User: wancli.UserInfo{Name: "foo"}}, // will select
		{User: wancli.UserInfo{Name: "bar"}},
		{User: wancli.UserInfo{Name: "ape"}},
		{User: wancli.UserInfo{Name: "llama"}},
	}

	expectedCredResponse := []*api.CredentialInfo{
		{Username: "ape"},
		{Username: "bar"},
		{Username: "foo"},
		{Username: "llama"},
	}

	// Test valid index.
	stream.assertResp = func(res *api.LoginPasswordlessResponse) error {
		require.Equal(t, api.PasswordlessPrompt_PASSWORDLESS_PROMPT_CREDENTIAL, res.Prompt)
		require.Equal(t, expectedCredResponse, res.GetCredentials())
		return nil
	}
	stream.serverReq = func() (*api.LoginPasswordlessRequest, error) {
		return &api.LoginPasswordlessRequest{Request: &api.LoginPasswordlessRequest_Credential{
			Credential: &api.LoginPasswordlessRequest_LoginPasswordlessCredentialResponse{
				Index: 2},
		}}, nil
	}

	prompt := newPwdlessLoginPrompt(context.Background(), stream)
	cred, err := prompt.PromptCredential(unsortedCreds)
	require.NoError(t, err)
	require.Equal(t, "foo", cred.User.Name)

	// Test invalid index.
	stream.serverReq = func() (*api.LoginPasswordlessRequest, error) {
		return &api.LoginPasswordlessRequest{Request: &api.LoginPasswordlessRequest_Credential{
			Credential: &api.LoginPasswordlessRequest_LoginPasswordlessCredentialResponse{
				Index: 4},
		}}, nil
	}
	_, err = prompt.PromptCredential(unsortedCreds)
	require.True(t, trace.IsBadParameter(err))
}

type mockLoginPwdlessStream struct {
	grpc.ServerStream
	assertResp func(resp *api.LoginPasswordlessResponse) error
	serverReq  func() (*api.LoginPasswordlessRequest, error)
}

func (m *mockLoginPwdlessStream) Send(resp *api.LoginPasswordlessResponse) error {
	if m.assertResp != nil {
		return m.assertResp(resp)
	}
	return trace.NotImplemented("assertResp not implemented")
}

func (m *mockLoginPwdlessStream) Recv() (*api.LoginPasswordlessRequest, error) {
	if m.serverReq != nil {
		return m.serverReq()
	}
	return nil, trace.NotImplemented("serverReq not implemented")
}
