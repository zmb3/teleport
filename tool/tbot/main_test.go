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
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/utils/golden"
)

func TestRun_Configure(t *testing.T) {
	t.Parallel()

	// This is slightly rubbish, but due to the global nature of `botfs`,
	// it's difficult to configure the default acl and symlink values to be
	// the same across dev laptops and GCB.
	// If we switch to a more dependency injected model for botfs, we can
	// ensure that the test one returns the same value across operating systems.
	normalizeOSDependentValues := func(data []byte) []byte {
		cpy := append([]byte{}, data...)
		cpy = bytes.ReplaceAll(
			cpy, []byte("symlinks: try-secure"), []byte("symlinks: secure"),
		)
		cpy = bytes.ReplaceAll(
			cpy, []byte(`acls: "off"`), []byte("acls: try"),
		)
		return cpy
	}

	baseArgs := []string{"configure"}
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "no parameters provided",
			args: baseArgs,
		},
		{
			name: "all parameters provided",
			args: append(baseArgs, []string{
				"-a", "example.com",
				"--token", "xxyzz",
				"--ca-pin", "sha256:capindata",
				"--data-dir", "/custom/data/dir",
				"--join-method", "token",
				"--oneshot",
				"--certificate-ttl", "42m",
				"--renewal-interval", "21m",
			}...),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Run("file", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				args := append(tt.args, []string{"-o", path}...)
				err := Run(args, nil)
				require.NoError(t, err)

				data, err := os.ReadFile(path)
				data = normalizeOSDependentValues(data)
				require.NoError(t, err)
				if golden.ShouldSet() {
					golden.Set(t, data)
				}
				require.Equal(t, string(golden.Get(t)), string(data))
			})

			t.Run("stdout", func(t *testing.T) {
				stdout := new(bytes.Buffer)
				err := Run(tt.args, stdout)
				require.NoError(t, err)
				data := normalizeOSDependentValues(stdout.Bytes())
				if golden.ShouldSet() {
					golden.Set(t, data)
				}
				require.Equal(t, string(golden.Get(t)), string(data))
			})
		})
	}
}
