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

package common

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/config"
)

type addedToken struct {
	Token   string
	Roles   []string
	Expires time.Time
}

type listedToken struct {
	Kind     string
	Version  string
	Metadata struct {
		Name    string
		Expires time.Time
		ID      uint
	}
	Spec struct {
		Roles      []string
		JoinMethod string
	}
}

func TestTokens(t *testing.T) {
	fileConfig := &config.FileConfig{
		Global: config.Global{
			DataDir: t.TempDir(),
		},
		Apps: config.Apps{
			Service: config.Service{
				EnabledFlag: "true",
			},
		},
		Proxy: config.Proxy{
			Service: config.Service{
				EnabledFlag: "true",
			},
			WebAddr: mustGetFreeLocalListenerAddr(t),
			TunAddr: mustGetFreeLocalListenerAddr(t),
		},
		Auth: config.Auth{
			Service: config.Service{
				EnabledFlag:   "true",
				ListenAddress: mustGetFreeLocalListenerAddr(t),
			},
		},
	}

	makeAndRunTestAuthServer(t, withFileConfig(fileConfig))

	// Test all output formats of "tokens add".
	t.Run("add", func(t *testing.T) {
		buf, err := runTokensCommand(t, fileConfig, []string{"add", "--type=node"})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(buf.String(), "The invite token:"))

		buf, err = runTokensCommand(t, fileConfig, []string{"add", "--type=node,app", "--format", teleport.Text})
		require.NoError(t, err)
		require.Equal(t, strings.Count(buf.String(), "\n"), 1)

		var out addedToken

		buf, err = runTokensCommand(t, fileConfig, []string{"add", "--type=node,app", "--format", teleport.JSON})
		require.NoError(t, err)
		mustDecodeJSON(t, buf, &out)

		require.Len(t, out.Roles, 2)
		require.Equal(t, types.KindNode, strings.ToLower(out.Roles[0]))
		require.Equal(t, types.KindApp, strings.ToLower(out.Roles[1]))

		buf, err = runTokensCommand(t, fileConfig, []string{"add", "--type=node,app", "--format", teleport.YAML})
		require.NoError(t, err)
		mustDecodeYAML(t, buf, &out)

		require.Len(t, out.Roles, 2)
		require.Equal(t, types.KindNode, strings.ToLower(out.Roles[0]))
		require.Equal(t, types.KindApp, strings.ToLower(out.Roles[1]))
	})

	// Test all output formats of "tokens ls".
	t.Run("ls", func(t *testing.T) {
		buf, err := runTokensCommand(t, fileConfig, []string{"ls"})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(buf.String(), "Token "))
		require.Equal(t, strings.Count(buf.String(), "\n"), 6) // account for header lines

		buf, err = runTokensCommand(t, fileConfig, []string{"ls", "--format", teleport.Text})
		require.NoError(t, err)
		require.Equal(t, strings.Count(buf.String(), "\n"), 4)

		var jsonOut []listedToken
		buf, err = runTokensCommand(t, fileConfig, []string{"ls", "--format", teleport.JSON})
		require.NoError(t, err)
		mustDecodeJSON(t, buf, &jsonOut)
		require.Len(t, jsonOut, 4)

		var yamlOut []listedToken
		buf, err = runTokensCommand(t, fileConfig, []string{"ls", "--format", teleport.YAML})
		require.NoError(t, err)
		mustDecodeYAML(t, buf, &yamlOut)
		require.Len(t, yamlOut, 4)

		require.Equal(t, jsonOut, yamlOut)
	})
}
