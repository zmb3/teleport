// Copyright 2022 Gravitational, Inc
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

package tester

import (
	"fmt"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/api/types"
)

func Test_formatString(t *testing.T) {
	tests := []struct {
		name        string
		description string
		msg         string
		want        string
	}{
		{
			name:        "empty",
			description: "",
			msg:         "",
			want:        ":\n\n",
		},
		{
			name:        "something",
			description: "a field",
			msg:         "foo baz bar blah",
			want: `a field:
foo baz bar blah
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatString(tt.description, tt.msg)
			require.Equal(t, tt.want, got)
		})
	}
}

func Test_formatYAML(t *testing.T) {
	tests := []struct {
		name        string
		description string
		object      interface{}
		want        string
	}{
		{
			name:        "empty",
			description: "",
			object:      nil,
			want:        ":\nnull\n",
		},
		{
			name:        "simple object",
			description: "my field",
			object: types.RoleSpecV6{
				Allow: types.RoleConditions{
					Logins:        []string{"username"},
					ClusterLabels: types.Labels{"access": []string{"ops"}},
				},
			},
			want: `my field:
allow:
  cluster_labels:
    access: ops
  logins:
  - username
deny: {}
options:
  cert_format: ""
  create_host_user: null
  desktop_clipboard: null
  desktop_directory_sharing: null
  forward_agent: false
  pin_source_ip: false
  record_session: null
  ssh_file_copy: null
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatYAML(tt.description, tt.object)
			require.Equal(t, tt.want, got)
		})
	}
}

func Test_formatJSON(t *testing.T) {
	tests := []struct {
		name        string
		description string
		object      interface{}
		want        string
	}{
		{
			name:        "empty",
			description: "empty field",
			object:      struct{}{},
			want: `empty field:
{}
`,
		},
		{
			name:        "simple object",
			description: "my field",
			object: types.RoleSpecV6{
				Allow: types.RoleConditions{
					Logins:        []string{"username"},
					ClusterLabels: types.Labels{"access": []string{"ops"}},
				},
			},
			want: `my field:
{
    "options": {
        "forward_agent": false,
        "cert_format": "",
        "record_session": null,
        "desktop_clipboard": null,
        "desktop_directory_sharing": null,
        "create_host_user": null,
        "pin_source_ip": false,
        "ssh_file_copy": null
    },
    "allow": {
        "logins": [
            "username"
        ],
        "cluster_labels": {
            "access": "ops"
        }
    },
    "deny": {}
}
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatJSON(tt.description, tt.object)
			require.Equal(t, tt.want, got)
		})
	}
}

func Test_formatUserDetails(t *testing.T) {
	tests := []struct {
		name        string
		description string
		info        *types.CreateUserParams
		want        string
	}{
		{
			name:        "empty",
			description: "",
			info:        nil,
			want:        "",
		},
		{
			name:        "some details",
			description: "user details",
			info: &types.CreateUserParams{
				ConnectorName: "foo",
				Username:      "bar",
				Logins:        []string{"laa", "lbb", "lcc"},
				KubeGroups:    []string{"kgaa", "kgbb", "kgcc"},
				KubeUsers:     []string{"kuaa", "kubb", "kucc"},
				Roles:         []string{"raa", "rbb", "rcc"},
				Traits: map[string][]string{
					"groups": {"gfoo", "gbar", "gbaz"},
				},
				SessionTTL: 1230,
			},
			want: `user details:
   kube_groups:
   - kgaa
   - kgbb
   - kgcc
   kube_users:
   - kuaa
   - kubb
   - kucc
   logins:
   - laa
   - lbb
   - lcc
   roles:
   - raa
   - rbb
   - rcc
   traits:
     groups:
     - gfoo
     - gbar
     - gbaz
   username: bar`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUserDetails(tt.description, tt.info)
			require.Equal(t, tt.want, got)
		})
	}
}

func Test_formatError(t *testing.T) {
	tests := []struct {
		name      string
		fieldDesc string
		err       error
		want      string
	}{
		{
			name:      "empty",
			fieldDesc: "my field",
			err:       nil,
			want:      "my field: error rendering field: <nil>\n",
		},
		{
			name:      "plain error",
			fieldDesc: "my field",
			err:       fmt.Errorf("foo: %v", 123),
			want:      "my field: error rendering field: foo: 123\n",
		},
		{
			name:      "trace error",
			fieldDesc: "my field",
			err:       trace.Errorf("bar: %v", 321),
			want:      "my field: error rendering field: bar: 321\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatError(tt.fieldDesc, tt.err)
			require.Equal(t, tt.want, got)
		})
	}
}
