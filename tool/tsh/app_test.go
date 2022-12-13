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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zmb3/teleport/lib/client"
)

func TestFormatAppConfig(t *testing.T) {
	defaultTc := &client.TeleportClient{
		Config: client.Config{
			WebProxyAddr: "test-tp.teleport:8443",
		},
	}
	testProfile := &client.ProfileStatus{
		Username: "test-user",
		Dir:      "/test/dir",
	}
	testAppName := "test-tp"
	testAppPublicAddr := "test-tp.teleport"
	testCluster := "test-tp"

	// func formatAppConfig(tc *client.TeleportClient, profile *client.ProfileStatus, appName,
	// appPublicAddr, format, cluster string) (string, error) {
	tests := []struct {
		name     string
		tc       *client.TeleportClient
		format   string
		insecure bool
		expected string
	}{
		{
			name: "format URI standard HTTPS port",
			tc: &client.TeleportClient{
				Config: client.Config{
					WebProxyAddr: "test-tp.teleport:443",
				},
			},
			format:   appFormatURI,
			expected: "https://test-tp.teleport",
		},
		{
			name:     "format URI standard non-standard HTTPS port",
			tc:       defaultTc,
			format:   appFormatURI,
			expected: "https://test-tp.teleport:8443",
		},
		{
			name:     "format CA",
			tc:       defaultTc,
			format:   appFormatCA,
			expected: "/test/dir/keys/cas/test-tp.pem",
		},
		{
			name:     "format cert",
			tc:       defaultTc,
			format:   appFormatCert,
			expected: "/test/dir/keys/test-user-app/test-tp-x509.pem",
		},
		{
			name:     "format key",
			tc:       defaultTc,
			format:   appFormatKey,
			expected: "/test/dir/keys/test-user",
		},
		{
			name:   "format curl standard non-standard HTTPS port",
			tc:     defaultTc,
			format: appFormatCURL,
			expected: `curl \
  --cert /test/dir/keys/test-user-app/test-tp-x509.pem \
  --key /test/dir/keys/test-user \
  https://test-tp.teleport:8443`,
		},
		{
			name:     "format insecure curl standard non-standard HTTPS port",
			tc:       defaultTc,
			format:   appFormatCURL,
			insecure: true,
			expected: `curl --insecure \
  --cert /test/dir/keys/test-user-app/test-tp-x509.pem \
  --key /test/dir/keys/test-user \
  https://test-tp.teleport:8443`,
		},
		{
			name:   "format JSON",
			tc:     defaultTc,
			format: appFormatJSON,
			expected: `{
  "name": "test-tp",
  "uri": "https://test-tp.teleport:8443",
  "ca": "/test/dir/keys/cas/test-tp.pem",
  "cert": "/test/dir/keys/test-user-app/test-tp-x509.pem",
  "key": "/test/dir/keys/test-user",
  "curl": "curl \\\n  --cert /test/dir/keys/test-user-app/test-tp-x509.pem \\\n  --key /test/dir/keys/test-user \\\n  https://test-tp.teleport:8443"
}
`,
		},
		{
			name:   "format YAML",
			tc:     defaultTc,
			format: appFormatYAML,
			expected: `ca: /test/dir/keys/cas/test-tp.pem
cert: /test/dir/keys/test-user-app/test-tp-x509.pem
curl: |-
  curl \
    --cert /test/dir/keys/test-user-app/test-tp-x509.pem \
    --key /test/dir/keys/test-user \
    https://test-tp.teleport:8443
key: /test/dir/keys/test-user
name: test-tp
uri: https://test-tp.teleport:8443
`,
		},
		{
			name:   "format default",
			tc:     defaultTc,
			format: "detaul",
			expected: `Name:      test-tp
URI:       https://test-tp.teleport:8443
CA:        /test/dir/keys/cas/test-tp.pem
Cert:      /test/dir/keys/test-user-app/test-tp-x509.pem
Key:       /test/dir/keys/test-user
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.tc.InsecureSkipVerify = test.insecure
			result, err := formatAppConfig(test.tc, testProfile, testAppName, testAppPublicAddr, test.format, testCluster)
			assert.NoError(t, err)
			assert.Equal(t, test.expected, result)
		})
	}
}
