/*
Copyright 2020-2021 Gravitational, Inc.

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

package postgres

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/client/db/profile"
)

func TestServiceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), pgServiceFile)

	serviceFile, err := LoadFromPath(path)
	require.NoError(t, err)

	profile := profile.ConnectProfile{
		Name:       "test",
		Host:       "localhost",
		Port:       5342,
		User:       "postgres",
		Database:   "postgres",
		Insecure:   false,
		CACertPath: "ca.pem",
		CertPath:   "cert.pem",
		KeyPath:    "key.pem",
	}

	err = serviceFile.Upsert(profile)
	require.NoError(t, err)

	env, err := serviceFile.Env(profile.Name)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"PGHOST":        profile.Host,
		"PGPORT":        strconv.Itoa(profile.Port),
		"PGUSER":        profile.User,
		"PGDATABASE":    profile.Database,
		"PGSSLMODE":     SSLModeVerifyFull,
		"PGSSLROOTCERT": profile.CACertPath,
		"PGSSLCERT":     profile.CertPath,
		"PGSSLKEY":      profile.KeyPath,
		"PGGSSENCMODE":  "disable",
	}, env)

	err = serviceFile.Delete(profile.Name)
	require.NoError(t, err)

	_, err = serviceFile.Env(profile.Name)
	require.Error(t, err)
	require.IsType(t, trace.NotFound(""), err)
}
