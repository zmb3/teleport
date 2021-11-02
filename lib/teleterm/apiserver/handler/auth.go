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

package handler

import (
	"context"

	api "github.com/gravitational/teleport/lib/teleterm/api/protogen/golang/v1"
	"github.com/gravitational/trace"
)

// CreateAuthChallenge creates auth challenge request
func (s *Handler) CreateAuthChallenge(ctx context.Context, req *api.CreateAuthChallengeRequest) (*api.CreateAuthChallengeResponse, error) {
	return &api.CreateAuthChallengeResponse{}, nil
}

// GetAuthSettings returns cluster auth preferences
func (s *Handler) GetAuthSettings(ctx context.Context, req *api.GetAuthSettingsRequest) (*api.AuthSettings, error) {
	cluster, err := s.DaemonService.GetCluster(req.ClusterUri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	preferences, err := cluster.SyncAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	result := &api.AuthSettings{
		Type:          preferences.Type,
		SecondFactor:  string(preferences.SecondFactor),
		AuthProviders: []*api.AuthProvider{},
	}

	for _, provider := range preferences.AuthProviders {
		result.AuthProviders = append(result.AuthProviders, &api.AuthProvider{
			Type:    provider.Type,
			Name:    provider.Name,
			Display: provider.Display,
		})
	}

	return result, nil
}

// CreateAuthSSOChallenge creates auth sso challenge and automatically solves it
func (s *Handler) CreateAuthSSOChallenge(ctx context.Context, req *api.CreateAuthSSOChallengeRequest) (*api.EmptyResponse, error) {
	cluster, err := s.DaemonService.GetCluster(req.ClusterUri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := cluster.SSOLogin(ctx, req.ProviderType, req.ProviderName); err != nil {
		return nil, trace.Wrap(err)
	}

	return &api.EmptyResponse{}, nil
}

// SolveAuthChallenge solves auth challenge and logs into a cluster
func (s *Handler) SolveAuthChallenge(ctx context.Context, req *api.SolveAuthChallengeRequest) (*api.SolveAuthChallengeResponse, error) {
	cluster, err := s.DaemonService.GetCluster(req.ClusterUri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := cluster.LocalLogin(ctx, req.User, req.Password, ""); err != nil {
		return nil, trace.Wrap(err)
	}

	return &api.SolveAuthChallengeResponse{}, nil
}
