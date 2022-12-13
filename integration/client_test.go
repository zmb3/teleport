/*
Copyright 2020-2022 Gravitational, Inc.

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

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/integration/helpers"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/utils"
)

// TestClientWithExpiredCredentialsAndDetailedErrorMessage creates and connects to the Auth service
// using an expired user identity
// We should receive an error message which contains the real cause (ssh: handshake)
func TestClientWithExpiredCredentialsAndDetailedErrorMessage(t *testing.T) {
	cfg := helpers.InstanceConfig{
		ClusterName: "root.example.com",
		HostID:      uuid.New().String(),
		NodeName:    Loopback,
		Log:         utils.NewLoggerForTests(),
	}
	cfg.Listeners = helpers.SingleProxyPortSetup(t, &cfg.Fds)
	rc := helpers.NewInstance(t, cfg)

	rcConf := service.MakeDefaultConfig()
	rcConf.DataDir = t.TempDir()
	rcConf.Auth.Enabled = true
	rcConf.Proxy.Enabled = true
	rcConf.Proxy.DisableWebInterface = true
	rcConf.SSH.Enabled = true
	rcConf.Version = "v2"

	username := helpers.MustGetCurrentUser(t).Username
	rc.AddUser(username, []string{username})

	err := rc.CreateEx(t, nil, rcConf)
	require.NoError(t, err)
	err = rc.Start()
	require.NoError(t, err)
	defer rc.StopAll()

	// Create an expired identity file: ttl is 1 second in the past
	identityFilePath := helpers.MustCreateUserIdentityFile(t, rc, username, -time.Second)

	ctx, cancelFunc := context.WithTimeout(context.Background(), time.Second)
	defer cancelFunc()
	_, err = client.New(ctx, client.Config{
		Addrs:       []string{rc.Auth},
		Credentials: []client.Credentials{client.LoadIdentityFile(identityFilePath)},
		DialOpts: []grpc.DialOption{
			// ask for underlying errors
			grpc.WithReturnConnectionError(),
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh: handshake failed")
}
