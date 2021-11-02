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

package daemon

import (
	"context"

	"github.com/gravitational/teleport/api/client/webclient"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/client"

	"github.com/gravitational/trace"
)

// SyncAuthPreference fetches Teleport auth preferences and stores it in the cluster profile
func (c *Cluster) SyncAuthPreference(ctx context.Context) (*webclient.AuthenticationSettings, error) {
	pingResponse, err := c.clusterClient.Ping(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := c.clusterClient.SaveProfile(c.dir, false); err != nil {
		return nil, trace.Wrap(err)
	}

	return &pingResponse.Auth, nil
}

// SSOLogin logs in a user to the Teleport cluster using supported SSO provider
func (c *Cluster) SSOLogin(ctx context.Context, providerType, providerName string) error {
	if _, err := c.clusterClient.Ping(ctx); err != nil {
		return trace.Wrap(err)
	}

	key, err := client.NewKey()
	if err != nil {
		return trace.Wrap(err)
	}

	response, err := client.SSHAgentSSOLogin(ctx, client.SSHLoginSSO{
		SSHLogin: client.SSHLogin{
			ProxyAddr:     c.clusterClient.WebProxyAddr,
			PubKey:        key.Pub,
			TTL:           c.clusterClient.KeyTTL,
			Insecure:      c.clusterClient.InsecureSkipVerify,
			Compatibility: c.clusterClient.CertificateFormat,
			//RouteToCluster:    c.clusterClient.SiteName,
			KubernetesCluster: c.clusterClient.KubernetesCluster,
		},
		ConnectorID: providerName,
		Protocol:    providerType,
		BindAddr:    c.clusterClient.BindAddr,
		Browser:     c.clusterClient.Browser,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	if err := c.processAuthResponse(ctx, key, response); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// LocalLogin processes local logins for this cluster
func (c *Cluster) LocalLogin(ctx context.Context, user, password, otpToken string) error {
	if _, err := c.clusterClient.Ping(ctx); err != nil {
		return trace.Wrap(err)
	}

	key, err := client.NewKey()
	if err != nil {
		return trace.Wrap(err)
	}

	response, err := client.SSHAgentLogin(ctx, client.SSHLoginDirect{
		SSHLogin: client.SSHLogin{
			ProxyAddr:         c.clusterClient.WebProxyAddr,
			PubKey:            key.Pub,
			TTL:               c.clusterClient.KeyTTL,
			Insecure:          c.clusterClient.InsecureSkipVerify,
			Compatibility:     c.clusterClient.CertificateFormat,
			KubernetesCluster: c.clusterClient.KubernetesCluster,
		},
		User:     user,
		Password: password,
		OTPToken: otpToken,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	if err := c.processAuthResponse(ctx, key, response); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (c *Cluster) processAuthResponse(ctx context.Context, key *client.Key, response *auth.SSHLoginResponse) error {
	// Check that a host certificate for at least one cluster was returned.
	if len(response.HostSigners) == 0 {
		return trace.BadParameter("bad response from the server: expected at least one certificate, got 0")
	}

	// extract the new certificate out of the response
	key.Cert = response.Cert
	key.TLSCert = response.TLSCert
	key.TrustedCA = response.HostSigners
	key.Username = response.Username

	if c.clusterClient.KubernetesCluster != "" {
		key.KubeTLSCerts[c.clusterClient.KubernetesCluster] = response.TLSCert
	}
	if c.clusterClient.DatabaseService != "" {
		key.DBTLSCerts[c.clusterClient.DatabaseService] = response.TLSCert
	}

	// Store the requested cluster name in the key.
	key.ClusterName = c.clusterClient.SiteName
	if key.ClusterName == "" {
		rootClusterName := key.TrustedCA[0].ClusterName
		key.ClusterName = rootClusterName
		c.clusterClient.SiteName = rootClusterName
	}

	// update username before updating the profile
	c.clusterClient.LocalAgent().UpdateUsername(response.Username)
	c.clusterClient.Username = response.Username

	if err := c.clusterClient.ActivateKey(ctx, key); err != nil {
		return trace.Wrap(err)
	}

	if err := c.clusterClient.SaveProfile(c.dir, true); err != nil {
		return trace.Wrap(err)
	}

	// update status with new certificate information
	status, err := client.StatusFromFile(c.dir, key.ProxyHost)
	if err != nil {
		return trace.Wrap(err)
	}

	c.status = *status

	return nil
}
