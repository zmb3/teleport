package client_test

import (
	"crypto/tls"
	"testing"

	"github.com/gravitational/teleport/api/client"
)

func TestNew(t *testing.T) {
	tests := []struct {
		cfg     client.Config
		wantErr bool
	}{
		{roles: []Role{}, wantErr: false},
		{roles: []Role{RoleAuth}, wantErr: false},
		{roles: []Role{RoleAuth, RoleWeb, RoleNode, RoleProxy, RoleAdmin, RoleProvisionToken, RoleTrustedCluster, LegacyClusterTokenType, RoleSignup, RoleNop}, wantErr: false},
		{roles: []Role{RoleAuth, RoleNode, RoleAuth}, wantErr: true},
		{roles: []Role{Role("unknown")}, wantErr: true},
	}

	for _, tt := range tests {
		client, err := client.New(client.Config{
			Addrs: []string{"localhost:3025"},
			// ContextDialer is optional context dialer
			ContextDialer: net.DialContext,
			// direct TLS credentials config
			Credentials: client.TLSCreds(tls.Config),
		})

		defer client.Close()

		ping, err := client.Ping()

		err := tt.roles.Check()
		if (err != nil) != tt.wantErr {
			t.Errorf("%v.Check(): got error %q, want error %v", tt.roles, err, tt.wantErr)
		}
	}
}
