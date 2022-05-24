/*
Copyright 2021 Gravitational, Inc.

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

package clusters

import (
	"context"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	// "github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"

	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
)

// Cluster describes user settings and access to various resources.
type Cluster struct {
	// URI is the cluster URI
	URI uri.ResourceURI
	// Name is the cluster name
	Name string

	// Log is a component logger
	Log logrus.FieldLogger
	// dir is the directory where cluster certificates are stored
	dir string
	// Status is the cluster status
	status client.ProfileStatus
	// client is the cluster Teleport client
	clusterClient *client.TeleportClient
	// clock is a clock for time-related operations
	clock clockwork.Clock
}

// TODO: Move this to a separate file.
type ClusterWithChannel struct {
	*Cluster

	outgoingClusterEventsC chan<- struct{}
}

func (c *Cluster) AttachChannel(outgoingClusterEventsC chan<- struct{}) ClusterWithChannel {
	return ClusterWithChannel{
		Cluster:                c,
		outgoingClusterEventsC: outgoingClusterEventsC,
	}
}

// Connected indicates if connection to the cluster can be established
func (c *Cluster) Connected() bool {
	return c.status.Name != "" && !c.status.IsExpired(c.clock)
}

// GetRoles returns currently logged-in user roles
func (c *Cluster) GetRoles(ctx context.Context) ([]*types.Role, error) {
	proxyClient, err := c.clusterClient.ConnectToProxy(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer proxyClient.Close()

	roles := []*types.Role{}
	for _, name := range c.status.Roles {
		role, err := proxyClient.GetRole(ctx, name)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		roles = append(roles, &role)
	}

	return roles, nil
}

// GetLoggedInUser returns currently logged-in user
func (c *Cluster) GetLoggedInUser() LoggedInUser {
	return LoggedInUser{
		Name:      c.status.Username,
		SSHLogins: c.status.Logins,
		Roles:     c.status.Roles,
	}
}

// GetActualName returns name of the cluster taken from the key
// (see an explanation for the field `actual_name` in cluster.proto)
func (c *Cluster) GetActualName() string {
	return c.clusterClient.SiteName
}

// GetProxyHost returns proxy address (host:port) of the cluster
func (c *Cluster) GetProxyHost() string {
	return c.status.ProxyURL.Host
}

// LoggedInUser is the currently logged-in user
type LoggedInUser struct {
	// Name is the user name
	Name string
	// SSHLogins is the user sshlogins
	SSHLogins []string
	// Roles is the user roles
	Roles []string
}

func (c *ClusterWithChannel) RetryWithRelogin(ctx context.Context, fn func() error) error {
	// err := fn()
	// if err == nil {
	// 	return nil
	// }

	// if !utils.IsHandshakeFailedError(err) && !utils.IsCertExpiredError(err) && !trace.IsBadParameter(err) && !trace.IsTrustError(err) {
	// 	return trace.Wrap(err)
	// }

	// c.Log.Debugf("Activating relogin on %v.", err)
	c.Log.Debugf("Activating relogin")

	// TODO: Notify Connect over stream about expired cert.
	c.outgoingClusterEventsC <- struct{}{}
	// TODO: Wait for certs to get refreshed.
	//       When a signal arrives that certs are refreshed, we should re-check if certs are indeed
	//       fresh. ~If not, then we should perhaps write to the stream and hang again. This way we can
	//       handle concurrent requests for different clusters.~ This will not work because we don't
	//       know which cluster was that canceled login attempt related to.
	//
	// TODO: Handle cancellation.
	//       Wait for certs to get refreshed or for the login attempt to be aborted.
	// TODO: For now, try a select solution that either waits for ctx.Done() or checks every 500ms if
	// the certs are okay.
	//
	// Do we need to unblock everything at once? If we do so, then all of those goroutines will
	// connect to the proxy at the same time. OTOH there shouldn't be that many of them.
	// https://github.com/golang/go/issues/21165#issuecomment-1063820049

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

	}

	return fn()
}
