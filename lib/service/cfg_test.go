/*
Copyright 2015 Gravitational, Inc.

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

package service

import (
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/backend/lite"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/fixtures"
	"github.com/zmb3/teleport/lib/srv/app/common"
	"github.com/zmb3/teleport/lib/utils"
)

func TestDefaultConfig(t *testing.T) {
	config := MakeDefaultConfig()
	require.NotNil(t, config)

	// all 3 services should be enabled by default
	require.True(t, config.Auth.Enabled)
	require.True(t, config.SSH.Enabled)
	require.True(t, config.Proxy.Enabled)

	localAuthAddr := utils.NetAddr{AddrNetwork: "tcp", Addr: "0.0.0.0:3025"}

	// data dir, hostname and auth server
	require.Equal(t, config.DataDir, defaults.DataDir)
	if len(config.Hostname) < 2 {
		t.Fatal("default hostname wasn't properly set")
	}

	// crypto settings
	require.Equal(t, config.CipherSuites, utils.DefaultCipherSuites())
	// Unfortunately the below algos don't have exported constants in
	// golang.org/x/crypto/ssh for us to use.
	require.Equal(t, config.Ciphers, []string{
		"aes128-gcm@openssh.com",
		"chacha20-poly1305@openssh.com",
		"aes128-ctr",
		"aes192-ctr",
		"aes256-ctr",
	})
	require.Equal(t, config.KEXAlgorithms, []string{
		"curve25519-sha256",
		"curve25519-sha256@libssh.org",
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group14-sha256",
	})
	require.Equal(t, config.MACAlgorithms, []string{
		"hmac-sha2-256-etm@openssh.com",
		"hmac-sha2-256",
	})

	// auth section
	auth := config.Auth
	require.Equal(t, auth.ListenAddr, localAuthAddr)
	require.Equal(t, auth.Limiter.MaxConnections, int64(defaults.LimiterMaxConnections))
	require.Equal(t, auth.Limiter.MaxNumberOfUsers, defaults.LimiterMaxConcurrentUsers)
	require.Equal(t, config.Auth.StorageConfig.Type, lite.GetName())
	require.Equal(t, auth.StorageConfig.Params[defaults.BackendPath], filepath.Join(config.DataDir, defaults.BackendDir))

	// SSH section
	ssh := config.SSH
	require.Equal(t, ssh.Limiter.MaxConnections, int64(defaults.LimiterMaxConnections))
	require.Equal(t, ssh.Limiter.MaxNumberOfUsers, defaults.LimiterMaxConcurrentUsers)
	require.Equal(t, ssh.AllowTCPForwarding, true)

	// proxy section
	proxy := config.Proxy
	require.Equal(t, proxy.Limiter.MaxConnections, int64(defaults.LimiterMaxConnections))
	require.Equal(t, proxy.Limiter.MaxNumberOfUsers, defaults.LimiterMaxConcurrentUsers)

	// Misc levers and dials
	require.Equal(t, config.RotationConnectionInterval, defaults.HighResPollingPeriod)
}

// TestCheckApp validates application configuration.
func TestCheckApp(t *testing.T) {
	type tc struct {
		desc  string
		inApp App
		err   string
	}
	tests := []tc{
		{
			desc: "valid subdomain",
			inApp: App{
				Name: "foo",
				URI:  "http://localhost",
			},
		},
		{
			desc: "subdomain cannot start with a dash",
			inApp: App{
				Name: "-foo",
				URI:  "http://localhost",
			},
			err: "must be a valid DNS subdomain",
		},
		{
			desc: `subdomain cannot contain the exclamation mark character "!"`,
			inApp: App{
				Name: "foo!bar",
				URI:  "http://localhost",
			},
			err: "must be a valid DNS subdomain",
		},
		{
			desc: "subdomain of length 63 characters is valid (maximum length)",
			inApp: App{
				Name: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				URI:  "http://localhost",
			},
		},
		{
			desc: "subdomain of length 64 characters is invalid",
			inApp: App{
				Name: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				URI:  "http://localhost",
			},
			err: "must be a valid DNS subdomain",
		},
	}
	for _, h := range common.ReservedHeaders {
		tests = append(tests, tc{
			desc: fmt.Sprintf("reserved header rewrite %v", h),
			inApp: App{
				Name: "foo",
				URI:  "http://localhost",
				Rewrite: &Rewrite{
					Headers: []Header{
						{
							Name:  h,
							Value: "rewritten",
						},
					},
				},
			},
			err: `invalid application "foo" header rewrite configuration`,
		})
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			err := tt.inApp.CheckAndSetDefaults()
			if tt.err != "" {
				require.Contains(t, err.Error(), tt.err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCheckDatabase(t *testing.T) {
	tests := []struct {
		desc       string
		inDatabase Database
		outErr     bool
	}{
		{
			desc: "ok",
			inDatabase: Database{
				Name:     "example",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
			},
			outErr: false,
		},
		{
			desc: "empty database name",
			inDatabase: Database{
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
			},
			outErr: true,
		},
		{
			desc: "fails services.ValidateDatabase",
			inDatabase: Database{
				Name:     "??--++",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
			},
			outErr: true,
		},
		{
			desc: "GCP valid configuration",
			inDatabase: Database{
				Name:     "example",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
				GCP: DatabaseGCP{
					ProjectID:  "project-1",
					InstanceID: "instance-1",
				},
				TLS: DatabaseTLS{
					CACert: fixtures.LocalhostCert,
				},
			},
			outErr: false,
		},
		{
			desc: "GCP project ID specified without instance ID",
			inDatabase: Database{
				Name:     "example",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
				GCP: DatabaseGCP{
					ProjectID: "project-1",
				},
				TLS: DatabaseTLS{
					CACert: fixtures.LocalhostCert,
				},
			},
			outErr: true,
		},
		{
			desc: "GCP instance ID specified without project ID",
			inDatabase: Database{
				Name:     "example",
				Protocol: defaults.ProtocolPostgres,
				URI:      "localhost:5432",
				GCP: DatabaseGCP{
					InstanceID: "instance-1",
				},
				TLS: DatabaseTLS{
					CACert: fixtures.LocalhostCert,
				},
			},
			outErr: true,
		},
		{
			desc: "SQL Server correct configuration",
			inDatabase: Database{
				Name:     "sqlserver",
				Protocol: defaults.ProtocolSQLServer,
				URI:      "localhost:1433",
				AD: DatabaseAD{
					KeytabFile: "/etc/keytab",
					Domain:     "test-domain",
					SPN:        "test-spn",
				},
			},
			outErr: false,
		},
		{
			desc: "SQL Server missing keytab",
			inDatabase: Database{
				Name:     "sqlserver",
				Protocol: defaults.ProtocolSQLServer,
				URI:      "localhost:1433",
				AD: DatabaseAD{
					Domain: "test-domain",
					SPN:    "test-spn",
				},
			},
			outErr: true,
		},
		{
			desc: "SQL Server missing AD domain",
			inDatabase: Database{
				Name:     "sqlserver",
				Protocol: defaults.ProtocolSQLServer,
				URI:      "localhost:1433",
				AD: DatabaseAD{
					KeytabFile: "/etc/keytab",
					SPN:        "test-spn",
				},
			},
			outErr: true,
		},
		{
			desc: "SQL Server missing SPN",
			inDatabase: Database{
				Name:     "sqlserver",
				Protocol: defaults.ProtocolSQLServer,
				URI:      "localhost:1433",
				AD: DatabaseAD{
					KeytabFile: "/etc/keytab",
					Domain:     "test-domain",
				},
			},
			outErr: true,
		},
		{
			desc: "MySQL with server version",
			inDatabase: Database{
				Name:     "mysql-foo",
				Protocol: defaults.ProtocolMySQL,
				URI:      "localhost:3306",
				MySQL: MySQLOptions{
					ServerVersion: "8.0.31",
				},
			},
			outErr: false,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			err := test.inDatabase.CheckAndSetDefaults()
			if test.outErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestParseHeaders validates parsing of strings into http header objects.
func TestParseHeaders(t *testing.T) {
	tests := []struct {
		desc string
		in   []string
		out  []Header
		err  string
	}{
		{
			desc: "parse multiple headers",
			in: []string{
				"Host: example.com    ",
				"X-Teleport-Logins: root, {{internal.logins}}",
				"X-Env  : {{external.env}}",
				"X-Env: env:prod",
				"X-Empty:",
			},
			out: []Header{
				{Name: "Host", Value: "example.com"},
				{Name: "X-Teleport-Logins", Value: "root, {{internal.logins}}"},
				{Name: "X-Env", Value: "{{external.env}}"},
				{Name: "X-Env", Value: "env:prod"},
				{Name: "X-Empty", Value: ""},
			},
		},
		{
			desc: "invalid header format (missing value)",
			in:   []string{"X-Header"},
			err:  `failed to parse "X-Header" as http header`,
		},
		{
			desc: "invalid header name (empty)",
			in:   []string{": missing"},
			err:  `invalid http header name: ": missing"`,
		},
		{
			desc: "invalid header name (space)",
			in:   []string{"X Space: space"},
			err:  `invalid http header name: "X Space: space"`,
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			out, err := ParseHeaders(test.in)
			if test.err != "" {
				require.EqualError(t, err, test.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.out, out)
			}
		})
	}
}

// TestHostLabelMatching tests regex-based host matching.
func TestHostLabelMatching(t *testing.T) {
	matchAllRule := regexp.MustCompile(`^.+`)

	for _, test := range []struct {
		desc      string
		hostnames []string
		rules     HostLabelRules
		expected  map[string]string
	}{
		{
			desc:      "single rule matches all",
			hostnames: []string{"foo", "foo.bar", "127.0.0.1", "test.example.com"},
			rules:     NewHostLabelRules(HostLabelRule{Regexp: matchAllRule, Labels: map[string]string{"foo": "bar"}}),
			expected:  map[string]string{"foo": "bar"},
		},
		{
			desc:      "only one rule matches",
			hostnames: []string{"db.example.com"},
			rules: NewHostLabelRules(
				HostLabelRule{Regexp: regexp.MustCompile(`^db\.example\.com$`), Labels: map[string]string{"role": "db"}},
				HostLabelRule{Regexp: regexp.MustCompile(`^app\.example\.com$`), Labels: map[string]string{"role": "app"}},
			),
			expected: map[string]string{"role": "db"},
		},
		{
			desc:      "all rules match",
			hostnames: []string{"test.example.com"},
			rules: NewHostLabelRules(
				HostLabelRule{Regexp: regexp.MustCompile(`\.example\.com$`), Labels: map[string]string{"foo": "bar"}},
				HostLabelRule{Regexp: regexp.MustCompile(`\.example\.com$`), Labels: map[string]string{"baz": "quux"}},
			),
			expected: map[string]string{"foo": "bar", "baz": "quux"},
		},
		{
			desc:      "no rules match",
			hostnames: []string{"test.example.com"},
			rules: NewHostLabelRules(
				HostLabelRule{Regexp: regexp.MustCompile(`\.xyz$`), Labels: map[string]string{"foo": "bar"}},
				HostLabelRule{Regexp: regexp.MustCompile(`\.xyz$`), Labels: map[string]string{"baz": "quux"}},
			),
			expected: map[string]string{},
		},
		{
			desc:      "conflicting rules, last one wins",
			hostnames: []string{"test.example.com"},
			rules: NewHostLabelRules(
				HostLabelRule{Regexp: regexp.MustCompile(`\.example\.com$`), Labels: map[string]string{"test": "one"}},
				HostLabelRule{Regexp: regexp.MustCompile(`^test\.`), Labels: map[string]string{"test": "two"}},
			),
			expected: map[string]string{"test": "two"},
		},
	} {
		t.Run(test.desc, func(t *testing.T) {
			for _, host := range test.hostnames {
				require.Equal(t, test.expected, test.rules.LabelsForHost(host))
			}
		})
	}
}
