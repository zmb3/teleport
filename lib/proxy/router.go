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

package proxy

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/exp/slices"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/observability/tracing"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/teleagent"
	"github.com/zmb3/teleport/lib/utils"
)

type serverResolverFn = func(ctx context.Context, host, port string, site site) (types.Server, error)

// SiteGetter provides access to connected local or remote sites
type SiteGetter interface {
	// GetSite returns the site matching the provided clusterName
	GetSite(clusterName string) (reversetunnel.RemoteSite, error)
}

// RemoteClusterGetter provides access to remote cluster resources
type RemoteClusterGetter interface {
	// GetRemoteCluster returns a remote cluster by name
	GetRemoteCluster(clusterName string) (types.RemoteCluster, error)
}

// RouterConfig contains all the dependencies required
// by the Router
type RouterConfig struct {
	// ClusterName indicates which cluster the router is for
	ClusterName string
	// Log is the logger to use
	Log *logrus.Entry
	// AccessPoint is the proxy cache
	RemoteClusterGetter RemoteClusterGetter
	// SiteGetter allows looking up sites
	SiteGetter SiteGetter
	// TracerProvider allows tracers to be created
	TracerProvider oteltrace.TracerProvider

	// serverResolver is used to resolve hosts, used by tests
	serverResolver serverResolverFn
}

// CheckAndSetDefaults ensures the required items were populated
func (c *RouterConfig) CheckAndSetDefaults() error {
	if c.Log == nil {
		c.Log = logrus.WithField(trace.Component, "Router")
	}

	if c.ClusterName == "" {
		return trace.BadParameter("ClusterName must be provided")
	}

	if c.RemoteClusterGetter == nil {
		return trace.BadParameter("RemoteClusterGetter must be provided")
	}

	if c.SiteGetter == nil {
		return trace.BadParameter("SiteGetter must be provided")
	}

	if c.TracerProvider == nil {
		c.TracerProvider = tracing.DefaultProvider()
	}

	if c.serverResolver == nil {
		c.serverResolver = getServer
	}

	return nil
}

// Router is used by the proxy to establish connections to both
// nodes and other clusters.
type Router struct {
	clusterName    string
	log            *logrus.Entry
	clusterGetter  RemoteClusterGetter
	localSite      reversetunnel.RemoteSite
	siteGetter     SiteGetter
	tracer         oteltrace.Tracer
	serverResolver serverResolverFn
}

// NewRouter creates and returns a Router that is populated
// from the provided RouterConfig.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	localSite, err := cfg.SiteGetter.GetSite(cfg.ClusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &Router{
		clusterName:    cfg.ClusterName,
		log:            cfg.Log,
		clusterGetter:  cfg.RemoteClusterGetter,
		localSite:      localSite,
		siteGetter:     cfg.SiteGetter,
		tracer:         cfg.TracerProvider.Tracer("Router"),
		serverResolver: cfg.serverResolver,
	}, nil

}

// DialHost dials the node that matches the provided host, port and cluster. If no matching node
// is found an error is returned. If more than one matching node is found and the cluster networking
// configuration is not set to route to the most recent an error is returned.
func (r *Router) DialHost(ctx context.Context, from net.Addr, host, port, clusterName string, accessChecker services.AccessChecker, agentGetter teleagent.Getter) (net.Conn, error) {
	ctx, span := r.tracer.Start(
		ctx,
		"router/DialHost",
		oteltrace.WithAttributes(
			attribute.String("host", host),
			attribute.String("port", port),
			attribute.String("site", clusterName),
		),
	)
	defer span.End()

	site := r.localSite
	if clusterName != r.clusterName {
		remoteSite, err := r.getRemoteCluster(ctx, clusterName, accessChecker)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		site = remoteSite
	}

	span.AddEvent("looking up server")
	target, err := r.serverResolver(ctx, host, port, remoteSite{site})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	span.AddEvent("retrieved target server")

	principals := []string{host}

	var (
		serverID   string
		serverAddr string
		proxyIDs   []string
	)
	if target != nil {
		proxyIDs = target.GetProxyIDs()
		serverID = fmt.Sprintf("%v.%v", target.GetName(), clusterName)

		// add hostUUID.cluster to the principals
		principals = append(principals, serverID)

		// add ip if it exists to the principals
		serverAddr = target.GetAddr()

		switch {
		case serverAddr != "":
			h, _, err := net.SplitHostPort(serverAddr)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			principals = append(principals, h)
		case serverAddr == "" && target.GetUseTunnel():
			serverAddr = reversetunnel.LocalNode
		}
	} else {
		if port == "" || port == "0" {
			port = strconv.Itoa(defaults.SSHServerListenPort)
		}

		serverAddr = net.JoinHostPort(host, port)
		r.log.Warnf("server lookup failed: using default=%v", serverAddr)
	}

	conn, err := site.Dial(reversetunnel.DialParams{
		From:         from,
		To:           &utils.NetAddr{AddrNetwork: "tcp", Addr: serverAddr},
		GetUserAgent: agentGetter,
		Address:      host,
		ServerID:     serverID,
		ProxyIDs:     proxyIDs,
		Principals:   principals,
		ConnType:     types.NodeTunnel,
	})

	return conn, trace.Wrap(err)
}

// getRemoteCluster looks up the provided clusterName to determine if a remote site exists with
// that name and determines if the user has access to it.
func (r *Router) getRemoteCluster(ctx context.Context, clusterName string, checker services.AccessChecker) (reversetunnel.RemoteSite, error) {
	_, span := r.tracer.Start(
		ctx,
		"router/getRemoteCluster",
		oteltrace.WithAttributes(
			attribute.String("site", clusterName),
		),
	)
	defer span.End()

	site, err := r.siteGetter.GetSite(clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	rc, err := r.clusterGetter.GetRemoteCluster(clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := checker.CheckAccessToRemoteCluster(rc); err != nil {
		return nil, utils.OpaqueAccessDenied(err)
	}

	return site, nil
}

// site is the minimum interface needed to match servers
// for a reversetunnel.RemoteSite. It makes testing easier.
type site interface {
	GetNodes(fn func(n services.Node) bool) ([]types.Server, error)
	GetClusterNetworkingConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterNetworkingConfig, error)
}

// remoteSite is a site implementation that wraps
// a reversetunnel.RemoteSite
type remoteSite struct {
	site reversetunnel.RemoteSite
}

// GetNodes uses the wrapped sites NodeWatcher to filter nodes
func (r remoteSite) GetNodes(fn func(n services.Node) bool) ([]types.Server, error) {
	watcher, err := r.site.NodeWatcher()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return watcher.GetNodes(fn), nil
}

// GetClusterNetworkingConfig uses the wrapped sites cache to retrieve the ClusterNetworkingConfig
func (r remoteSite) GetClusterNetworkingConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterNetworkingConfig, error) {
	ap, err := r.site.CachingAccessPoint()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cfg, err := ap.GetClusterNetworkingConfig(ctx, opts...)
	return cfg, trace.Wrap(err)
}

// getServer attempts to locate a node matching the provided host and port in
// the provided site.
func getServer(ctx context.Context, host, port string, site site) (types.Server, error) {
	if site == nil {
		return nil, trace.BadParameter("invalid remote site provided")
	}

	strategy := types.RoutingStrategy_UNAMBIGUOUS_MATCH
	if cfg, err := site.GetClusterNetworkingConfig(ctx); err == nil {
		strategy = cfg.GetRoutingStrategy()
	}

	_, err := uuid.Parse(host)
	dialByID := err == nil || utils.IsEC2NodeID(host)

	ips, _ := net.LookupHost(host)

	var unambiguousIDMatch bool
	matches, err := site.GetNodes(func(server services.Node) bool {
		if unambiguousIDMatch {
			return false
		}

		// if host is a UUID or EC2 ID match only
		// by server name and treat matches as unambiguous
		if dialByID && server.GetName() == host {
			unambiguousIDMatch = true
			return true
		}

		// if the server has connected over a reverse tunnel
		// then match only by hostname
		if server.GetUseTunnel() {
			return host == server.GetHostname()
		}

		ip, nodePort, err := net.SplitHostPort(server.GetAddr())
		if err != nil {
			return false
		}

		if (host == ip || host == server.GetHostname() || slices.Contains(ips, ip)) &&
			(port == "" || port == "0" || port == nodePort) {
			return true
		}

		return false
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var server types.Server
	switch {
	case strategy == types.RoutingStrategy_MOST_RECENT:
		for _, m := range matches {
			if server == nil || m.Expiry().After(server.Expiry()) {
				server = m
			}
		}
	case len(matches) > 1:
		return nil, trace.NotFound(teleport.NodeIsAmbiguous)
	case len(matches) == 1:
		server = matches[0]
	}

	if dialByID && server == nil {
		idType := "UUID"
		if utils.IsEC2NodeID(host) {
			idType = "EC2"
		}

		return nil, trace.NotFound("unable to locate node matching %s-like target %s", idType, host)
	}

	return server, nil

}

// DialSite establishes a connection to the auth server in the provided
// cluster. If the clusterName is an empty string then a connection to
// the local auth server will be established.
func (r *Router) DialSite(ctx context.Context, clusterName string) (net.Conn, error) {
	_, span := r.tracer.Start(
		ctx,
		"router/DialSite",
		oteltrace.WithAttributes(
			attribute.String("site", clusterName),
		),
	)
	defer span.End()

	// default to local cluster if one wasn't provided
	if clusterName == "" {
		clusterName = r.clusterName
	}

	// dial the local auth server
	if clusterName == r.clusterName {
		conn, err := r.localSite.DialAuthServer()
		return conn, trace.Wrap(err)
	}

	// lookup the site and dial its auth server
	site, err := r.siteGetter.GetSite(clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	conn, err := site.DialAuthServer()
	return conn, trace.Wrap(err)
}
