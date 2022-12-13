/*
Copyright 2016 Gravitational, Inc.

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

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/gravitational/teleport/api/client/webclient"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/observability/tracing"
	"github.com/gravitational/teleport/lib/session"
	"github.com/gravitational/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

func TestParseProxyHostString(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		input     string
		expectErr bool
		expect    ParsedProxyHost
	}{
		{
			name:      "Empty port string",
			input:     "example.org",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: true,
				WebProxyAddr:             "example.org:3080",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "Web proxy port only",
			input:     "example.org:1234",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:1234",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "Web proxy port with whitespace",
			input:     "example.org: 1234",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:1234",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "Web proxy port empty with whitespace",
			input:     "example.org:  ,200",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: true,
				WebProxyAddr:             "example.org:3080",
				SSHProxyAddr:             "example.org:200",
			},
		}, {
			name:      "SSH port only",
			input:     "example.org:,200",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: true,
				WebProxyAddr:             "example.org:3080",
				SSHProxyAddr:             "example.org:200",
			},
		}, {
			name:      "SSH port empty",
			input:     "example.org:100,",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:100",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "SSH port with whitespace",
			input:     "example.org:100, 200 ",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:100",
				SSHProxyAddr:             "example.org:200",
			},
		}, {
			name:      "SSH port empty with whitespace",
			input:     "example.org:100,  ",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:100",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "Both ports specified",
			input:     "example.org:100,200",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: false,
				WebProxyAddr:             "example.org:100",
				SSHProxyAddr:             "example.org:200",
			},
		}, {
			name:      "Both ports empty with whitespace",
			input:     "example.org: , ",
			expectErr: false,
			expect: ParsedProxyHost{
				Host:                     "example.org",
				UsingDefaultWebProxyPort: true,
				WebProxyAddr:             "example.org:3080",
				SSHProxyAddr:             "example.org:3023",
			},
		}, {
			name:      "Too many parts",
			input:     "example.org:100,200,300,400",
			expectErr: true,
			expect:    ParsedProxyHost{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			expected := testCase.expect
			actual, err := ParseProxyHost(testCase.input)

			if testCase.expectErr {
				require.Error(t, err)
				require.Nil(t, actual)
				return
			}

			require.NoError(t, err)
			require.Equal(t, expected.Host, actual.Host)
			require.Equal(t, expected.UsingDefaultWebProxyPort, actual.UsingDefaultWebProxyPort)
			require.Equal(t, expected.WebProxyAddr, actual.WebProxyAddr)
			require.Equal(t, expected.SSHProxyAddr, actual.SSHProxyAddr)
		})
	}
}

func TestNew(t *testing.T) {
	conf := Config{
		Host:      "localhost",
		HostLogin: "vincent",
		HostPort:  22,
		KeysDir:   "/tmp",
		Username:  "localuser",
		SiteName:  "site",
		Tracer:    tracing.NoopProvider().Tracer("test"),
	}
	err := conf.ParseProxyHost("proxy")
	require.NoError(t, err)

	tc, err := NewClient(&conf)
	require.NoError(t, err)
	require.NotNil(t, tc)

	la := tc.LocalAgent()
	require.NotNil(t, la)
}

func TestParseLabels(t *testing.T) {
	// simplest case:
	m, err := ParseLabelSpec("key=value")
	require.NotNil(t, m)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(m, map[string]string{
		"key": "value",
	}))

	// multiple values:
	m, err = ParseLabelSpec(`type="database";" role"=master,ver="mongoDB v1,2"`)
	require.NotNil(t, m)
	require.NoError(t, err)
	require.Len(t, m, 3)
	require.Equal(t, m["role"], "master")
	require.Equal(t, m["type"], "database")
	require.Equal(t, m["ver"], "mongoDB v1,2")

	// multiple and unicode:
	m, err = ParseLabelSpec(`服务器环境=测试,操作系统类别=Linux,机房=华北`)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.Len(t, m, 3)
	require.Equal(t, m["服务器环境"], "测试")
	require.Equal(t, m["操作系统类别"], "Linux")
	require.Equal(t, m["机房"], "华北")

	// invalid specs
	m, err = ParseLabelSpec(`type="database,"role"=master,ver="mongoDB v1,2"`)
	require.Nil(t, m)
	require.NotNil(t, err)
	m, err = ParseLabelSpec(`type="database",role,master`)
	require.Nil(t, m)
	require.NotNil(t, err)
}

func TestPortsParsing(t *testing.T) {
	// empty:
	ports, err := ParsePortForwardSpec(nil)
	require.Nil(t, ports)
	require.NoError(t, err)
	ports, err = ParsePortForwardSpec([]string{})
	require.Nil(t, ports)
	require.NoError(t, err)
	// not empty (but valid)
	spec := []string{
		"80:remote.host:180",
		"10.0.10.1:443:deep.host:1443",
	}
	ports, err = ParsePortForwardSpec(spec)
	require.NoError(t, err)
	require.Len(t, ports, 2)
	require.Empty(t, cmp.Diff(ports, ForwardedPorts{
		{
			SrcIP:    "127.0.0.1",
			SrcPort:  80,
			DestHost: "remote.host",
			DestPort: 180,
		},
		{
			SrcIP:    "10.0.10.1",
			SrcPort:  443,
			DestHost: "deep.host",
			DestPort: 1443,
		},
	}))

	// back to strings:
	clone := ports.String()
	require.Equal(t, spec[0], clone[0])
	require.Equal(t, spec[1], clone[1])

	// parse invalid spec:
	spec = []string{"foo", "bar"}
	ports, err = ParsePortForwardSpec(spec)
	require.Empty(t, ports)
	require.True(t, trace.IsBadParameter(err), "expected bad parameter, got %v", err)
}

func TestDynamicPortsParsing(t *testing.T) {
	tests := []struct {
		spec    []string
		isError bool
		output  DynamicForwardedPorts
	}{
		{
			spec:    nil,
			isError: false,
			output:  DynamicForwardedPorts{},
		},
		{
			spec:    []string{},
			isError: false,
			output:  DynamicForwardedPorts{},
		},
		{
			spec:    []string{"localhost"},
			isError: true,
			output:  DynamicForwardedPorts{},
		},
		{
			spec:    []string{"localhost:123:456"},
			isError: true,
			output:  DynamicForwardedPorts{},
		},
		{
			spec:    []string{"8080"},
			isError: false,
			output: DynamicForwardedPorts{
				DynamicForwardedPort{
					SrcIP:   "127.0.0.1",
					SrcPort: 8080,
				},
			},
		},
		{
			spec:    []string{":8080"},
			isError: false,
			output: DynamicForwardedPorts{
				DynamicForwardedPort{
					SrcIP:   "127.0.0.1",
					SrcPort: 8080,
				},
			},
		},
		{
			spec:    []string{":8080:8081"},
			isError: true,
			output:  DynamicForwardedPorts{},
		},
		{
			spec:    []string{"[::1]:8080"},
			isError: false,
			output: DynamicForwardedPorts{
				DynamicForwardedPort{
					SrcIP:   "::1",
					SrcPort: 8080,
				},
			},
		},
		{
			spec:    []string{"10.0.0.1:8080"},
			isError: false,
			output: DynamicForwardedPorts{
				DynamicForwardedPort{
					SrcIP:   "10.0.0.1",
					SrcPort: 8080,
				},
			},
		},
		{
			spec:    []string{":8080", "10.0.0.1:8080"},
			isError: false,
			output: DynamicForwardedPorts{
				DynamicForwardedPort{
					SrcIP:   "127.0.0.1",
					SrcPort: 8080,
				},
				DynamicForwardedPort{
					SrcIP:   "10.0.0.1",
					SrcPort: 8080,
				},
			},
		},
	}

	for _, tt := range tests {
		specs, err := ParseDynamicPortForwardSpec(tt.spec)
		if tt.isError {
			require.NotNil(t, err)
			continue
		} else {
			require.NoError(t, err)
		}

		require.Empty(t, cmp.Diff(specs, tt.output))
	}
}

func TestWebProxyHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc         string
		webProxyAddr string
		wantHost     string
		wantPort     int
	}{
		{
			desc:         "valid WebProxyAddr",
			webProxyAddr: "example.com:12345",
			wantHost:     "example.com",
			wantPort:     12345,
		},
		{
			desc:         "WebProxyAddr without port",
			webProxyAddr: "example.com",
			wantHost:     "example.com",
			wantPort:     defaults.HTTPListenPort,
		},
		{
			desc:         "invalid WebProxyAddr",
			webProxyAddr: "not a valid addr",
			wantHost:     "unknown",
			wantPort:     defaults.HTTPListenPort,
		},
		{
			desc:         "empty WebProxyAddr",
			webProxyAddr: "",
			wantHost:     "unknown",
			wantPort:     defaults.HTTPListenPort,
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			c := &Config{WebProxyAddr: tt.webProxyAddr}
			gotHost, gotPort := c.WebProxyHostPort()
			require.Equal(t, tt.wantHost, gotHost)
			require.Equal(t, tt.wantPort, gotPort)
		})
	}
}

func TestGetKubeTLSServerName(t *testing.T) {
	tests := []struct {
		name          string
		kubeProxyAddr string
		want          string
	}{
		{
			name:          "ipv4 format, API domain should be used",
			kubeProxyAddr: "127.0.0.1",
			want:          "kube.teleport.cluster.local",
		},
		{
			name:          "empty host, API domain should be used",
			kubeProxyAddr: "",
			want:          "kube.teleport.cluster.local",
		},
		{
			name:          "ipv4 unspecified, API domain should be used ",
			kubeProxyAddr: "0.0.0.0",
			want:          "kube.teleport.cluster.local",
		},
		{
			name:          "valid hostname",
			kubeProxyAddr: "example.com",
			want:          "kube.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetKubeTLSServerName(tt.kubeProxyAddr)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestApplyProxySettings validates that settings received from the proxy's
// ping endpoint are correctly applied to Teleport client.
func TestApplyProxySettings(t *testing.T) {
	tests := []struct {
		desc        string
		settingsIn  webclient.ProxySettings
		tcConfigIn  Config
		tcConfigOut Config
	}{
		{
			desc:       "Postgres public address unspecified, defaults to web proxy address",
			settingsIn: webclient.ProxySettings{},
			tcConfigIn: Config{
				WebProxyAddr: "web.example.com:443",
			},
			tcConfigOut: Config{
				WebProxyAddr:      "web.example.com:443",
				PostgresProxyAddr: "web.example.com:443",
			},
		},
		{
			desc: "MySQL enabled without public address, defaults to web proxy host and MySQL default port",
			settingsIn: webclient.ProxySettings{
				DB: webclient.DBProxySettings{
					MySQLListenAddr: "0.0.0.0:3036",
				},
			},
			tcConfigIn: Config{
				WebProxyAddr: "web.example.com:443",
			},
			tcConfigOut: Config{
				WebProxyAddr:      "web.example.com:443",
				PostgresProxyAddr: "web.example.com:443",
				MySQLProxyAddr:    "web.example.com:3036",
			},
		},
		{
			desc: "both Postgres and MySQL custom public addresses are specified",
			settingsIn: webclient.ProxySettings{
				DB: webclient.DBProxySettings{
					PostgresPublicAddr: "postgres.example.com:5432",
					MySQLListenAddr:    "0.0.0.0:3036",
					MySQLPublicAddr:    "mysql.example.com:3306",
				},
			},
			tcConfigIn: Config{
				WebProxyAddr: "web.example.com:443",
			},
			tcConfigOut: Config{
				WebProxyAddr:      "web.example.com:443",
				PostgresProxyAddr: "postgres.example.com:5432",
				MySQLProxyAddr:    "mysql.example.com:3306",
			},
		},
		{
			desc: "Postgres public address port unspecified, defaults to web proxy address port",
			settingsIn: webclient.ProxySettings{
				DB: webclient.DBProxySettings{
					PostgresPublicAddr: "postgres.example.com",
				},
			},
			tcConfigIn: Config{
				WebProxyAddr: "web.example.com:443",
			},
			tcConfigOut: Config{
				WebProxyAddr:      "web.example.com:443",
				PostgresProxyAddr: "postgres.example.com:443",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			tc := &TeleportClient{Config: test.tcConfigIn}
			err := tc.applyProxySettings(test.settingsIn)
			require.NoError(t, err)
			require.EqualValues(t, test.tcConfigOut, tc.Config)
		})
	}
}

type mockAgent struct {
	// Agent is embedded to avoid redeclaring all interface methods.
	// Only the Signers method is implemented by testAgent.
	agent.ExtendedAgent
	ValidPrincipals []string
}

type mockSigner struct {
	ValidPrincipals []string
}

func (s *mockSigner) PublicKey() ssh.PublicKey {
	return &ssh.Certificate{
		ValidPrincipals: s.ValidPrincipals,
	}
}

func (s *mockSigner) Sign(rand io.Reader, b []byte) (*ssh.Signature, error) {
	return nil, fmt.Errorf("mockSigner does not implement Sign")
}

// Signers implements agent.Agent.Signers.
func (m *mockAgent) Signers() ([]ssh.Signer, error) {
	return []ssh.Signer{&mockSigner{ValidPrincipals: m.ValidPrincipals}}, nil
}

func TestNewClient_UseKeyPrincipals(t *testing.T) {
	cfg := &Config{
		Username:         "xyz",
		HostLogin:        "xyz",
		WebProxyAddr:     "localhost",
		SkipLocalAuth:    true,
		UseKeyPrincipals: true, // causes VALID to be returned, as key was used
		Agent:            &mockAgent{ValidPrincipals: []string{"VALID"}},
		AuthMethods:      []ssh.AuthMethod{ssh.Password("xyz") /* placeholder authmethod */},
		Tracer:           tracing.NoopProvider().Tracer("test"),
	}
	client, err := NewClient(cfg)
	require.NoError(t, err)
	require.Equal(t, "VALID", client.getProxySSHPrincipal(), "ProxySSHPrincipal mismatch")

	cfg.UseKeyPrincipals = false // causes xyz to be returned as key was not used

	client, err = NewClient(cfg)
	require.NoError(t, err)
	require.Equal(t, "xyz", client.getProxySSHPrincipal(), "ProxySSHPrincipal mismatch")
}

func TestParseSearchKeywords(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		spec     string
		expected []string
	}{
		{
			name: "empty input",
			spec: "",
		},
		{
			name:     "simple input",
			spec:     "foo",
			expected: []string{"foo"},
		},
		{
			name:     "complex input",
			spec:     `"foo,bar","some phrase's",baz=qux's ,"some other  phrase"," another one  "`,
			expected: []string{"foo,bar", "some phrase's", "baz=qux's", "some other  phrase", "another one"},
		},
		{
			name:     "unicode input",
			spec:     `"服务器环境=测试,操作系统类别", Linux , 机房=华北 `,
			expected: []string{"服务器环境=测试,操作系统类别", "Linux", "机房=华北"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := ParseSearchKeywords(tc.spec, ',')
			require.Equal(t, tc.expected, m)
		})
	}

	// Test default delimiter (which is a comma)
	m := ParseSearchKeywords("foo,bar", rune(0))
	require.Equal(t, []string{"foo", "bar"}, m)
}

func TestParseSearchKeywords_SpaceDelimiter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		spec     string
		expected []string
	}{
		{
			name:     "simple input",
			spec:     "foo",
			expected: []string{"foo"},
		},
		{
			name:     "complex input",
			spec:     `foo,bar "some phrase's" baz=qux's "some other  phrase" " another one  "`,
			expected: []string{"foo,bar", "some phrase's", "baz=qux's", "some other  phrase", "another one"},
		},
		{
			name:     "unicode input",
			spec:     `服务器环境=测试,操作系统类别 Linux  机房=华北 `,
			expected: []string{"服务器环境=测试,操作系统类别", "Linux", "机房=华北"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := ParseSearchKeywords(tc.spec, ' ')
			require.Equal(t, tc.expected, m)
		})
	}
}

func TestVirtualPathNames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		kind     VirtualPathKind
		params   VirtualPathParams
		expected []string
	}{
		{
			name:   "dummy",
			kind:   VirtualPathKind("foo"),
			params: VirtualPathParams{"a", "b", "c"},
			expected: []string{
				"TSH_VIRTUAL_PATH_FOO_A_B_C",
				"TSH_VIRTUAL_PATH_FOO_A_B",
				"TSH_VIRTUAL_PATH_FOO_A",
				"TSH_VIRTUAL_PATH_FOO",
			},
		},
		{
			name:     "key",
			kind:     VirtualPathKey,
			params:   nil,
			expected: []string{"TSH_VIRTUAL_PATH_KEY"},
		},
		{
			name:   "database ca",
			kind:   VirtualPathCA,
			params: VirtualPathCAParams(types.DatabaseCA),
			expected: []string{
				"TSH_VIRTUAL_PATH_CA_DB",
				"TSH_VIRTUAL_PATH_CA",
			},
		},
		{
			name:   "host ca",
			kind:   VirtualPathCA,
			params: VirtualPathCAParams(types.HostCA),
			expected: []string{
				"TSH_VIRTUAL_PATH_CA_HOST",
				"TSH_VIRTUAL_PATH_CA",
			},
		},
		{
			name:   "database",
			kind:   VirtualPathDatabase,
			params: VirtualPathDatabaseParams("foo"),
			expected: []string{
				"TSH_VIRTUAL_PATH_DB_FOO",
				"TSH_VIRTUAL_PATH_DB",
			},
		},
		{
			name:   "app",
			kind:   VirtualPathApp,
			params: VirtualPathAppParams("foo"),
			expected: []string{
				"TSH_VIRTUAL_PATH_APP_FOO",
				"TSH_VIRTUAL_PATH_APP",
			},
		},
		{
			name:   "kube",
			kind:   VirtualPathKubernetes,
			params: VirtualPathKubernetesParams("foo"),
			expected: []string{
				"TSH_VIRTUAL_PATH_KUBE_FOO",
				"TSH_VIRTUAL_PATH_KUBE",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			names := VirtualPathEnvNames(tc.kind, tc.params)
			require.Equal(t, tc.expected, names)
		})
	}
}

func TestFormatConnectToProxyErr(t *testing.T) {
	tests := []struct {
		name string
		err  error

		wantError       string
		wantUserMessage string
	}{
		{
			name: "nil error passes through",
			err:  nil,
		},
		{
			name:      "unrelated error passes through",
			err:       fmt.Errorf("flux capacitor undercharged"),
			wantError: "flux capacitor undercharged",
		},
		{
			name:            "principals mismatch user message injected",
			err:             trace.Wrap(fmt.Errorf(`ssh: handshake failed: ssh: principal "" not in the set of valid principals for given certificate`)),
			wantError:       `ssh: handshake failed: ssh: principal "" not in the set of valid principals for given certificate`,
			wantUserMessage: unconfiguredPublicAddrMsg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := formatConnectToProxyErr(tt.err)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			traceErr, isTraceErr := err.(*trace.TraceErr)

			if isTraceErr {
				require.EqualError(t, traceErr.OrigError(), tt.wantError)
			} else {
				require.EqualError(t, err, tt.wantError)
			}

			if tt.wantUserMessage != "" {
				require.True(t, isTraceErr)
				require.Contains(t, traceErr.Messages, tt.wantUserMessage)
			}
		})
	}
}

func TestGetDesktopEventWebURL(t *testing.T) {
	initDate := time.Date(2021, 1, 1, 12, 0, 0, 0, time.UTC)

	tt := []struct {
		name      string
		proxyHost string
		cluster   string
		sid       session.ID
		events    []events.EventFields
		expected  string
	}{
		{
			name:     "nil events",
			events:   nil,
			expected: "",
		},
		{
			name:     "empty events",
			events:   make([]events.EventFields, 0),
			expected: "",
		},
		{
			name:      "two events, 1000 ms duration",
			proxyHost: "host",
			cluster:   "cluster",
			sid:       "session_id",
			events: []events.EventFields{
				{
					"time": initDate,
				},
				{
					"time": initDate.Add(1000 * time.Millisecond),
				},
			},
			expected: "https://host/web/cluster/cluster/session/session_id?recordingType=desktop&durationMs=1000",
		},
		{
			name:      "multiple events",
			proxyHost: "host",
			cluster:   "cluster",
			sid:       "session_id",
			events: []events.EventFields{
				{
					"time": initDate,
				},
				{
					"time": initDate.Add(10 * time.Millisecond),
				},
				{
					"time": initDate.Add(20 * time.Millisecond),
				},
				{
					"time": initDate.Add(30 * time.Millisecond),
				},
				{
					"time": initDate.Add(40 * time.Millisecond),
				},
				{
					"time": initDate.Add(50 * time.Millisecond),
				},
			},
			expected: "https://host/web/cluster/cluster/session/session_id?recordingType=desktop&durationMs=50",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, getDesktopEventWebURL(tc.proxyHost, tc.cluster, &tc.sid, tc.events))
		})
	}
}

type mockRoleGetter func(ctx context.Context) ([]types.Role, error)

func (m mockRoleGetter) GetRoles(ctx context.Context) ([]types.Role, error) {
	return m(ctx)
}

func TestCommandLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mfaRequired bool
		getter      roleGetter
		expected    int
	}{
		{
			name:        "mfa required",
			mfaRequired: true,
			expected:    1,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				role, err := types.NewRole("test", types.RoleSpecV6{
					Options: types.RoleOptions{MaxConnections: 500},
				})
				require.NoError(t, err)

				return []types.Role{role}, nil
			}),
		},
		{
			name:     "failure getting roles",
			expected: 1,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				return nil, errors.New("fail")
			}),
		},
		{
			name:     "no roles",
			expected: -1,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				return nil, nil
			}),
		},
		{
			name:     "max_connections=1",
			expected: 1,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				role, err := types.NewRole("test", types.RoleSpecV6{
					Options: types.RoleOptions{MaxConnections: 1},
				})
				require.NoError(t, err)

				return []types.Role{role}, nil
			}),
		},
		{
			name:     "max_connections=2",
			expected: 1,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				role, err := types.NewRole("test", types.RoleSpecV6{
					Options: types.RoleOptions{MaxConnections: 2},
				})
				require.NoError(t, err)

				return []types.Role{role}, nil
			}),
		},
		{
			name:     "max_connections=500",
			expected: 250,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				role, err := types.NewRole("test", types.RoleSpecV6{
					Options: types.RoleOptions{MaxConnections: 500},
				})
				require.NoError(t, err)

				return []types.Role{role}, nil
			}),
		},
		{
			name:     "max_connections=max",
			expected: math.MaxInt64 / 2,
			getter: mockRoleGetter(func(ctx context.Context) ([]types.Role, error) {
				role, err := types.NewRole("test", types.RoleSpecV6{
					Options: types.RoleOptions{MaxConnections: math.MaxInt64},
				})
				require.NoError(t, err)

				return []types.Role{role}, nil
			}),
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, commandLimit(context.Background(), tt.getter, tt.mfaRequired))
		})
	}
}
