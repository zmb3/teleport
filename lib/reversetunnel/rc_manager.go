/*
Copyright 2020 Gravitational, Inc.

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

package reversetunnel

import (
	"context"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	apitypes "github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/utils"
)

// RemoteClusterTunnelManager manages AgentPools for trusted (remote) clusters. It
// polls the auth server for ReverseTunnel resources and spins AgentPools for
// each one as needed.
//
// Note: ReverseTunnel resources on the auth server represent the desired
// tunnels, not actually active ones.
type RemoteClusterTunnelManager struct {
	cfg RemoteClusterTunnelManagerConfig

	mu      sync.Mutex
	pools   map[remoteClusterKey]*AgentPool
	stopRun func()

	newAgentPool func(ctx context.Context, cfg RemoteClusterTunnelManagerConfig, cluster, addr string) (*AgentPool, error)
}

type remoteClusterKey struct {
	cluster string
	addr    string
}

// RemoteClusterTunnelManagerConfig is a bundle of parameters used by a
// RemoteClusterTunnelManager.
type RemoteClusterTunnelManagerConfig struct {
	// AuthClient is client to the auth server.
	AuthClient auth.ClientI
	// AccessPoint is a lightweight access point that can optionally cache some
	// values.
	AccessPoint auth.ProxyAccessPoint
	// HostSigners is a signer for the host private key.
	HostSigner ssh.Signer
	// HostUUID is a unique ID of this host
	HostUUID string
	// LocalCluster is a cluster name this client is a member of.
	LocalCluster string
	// Local ReverseTunnelServer to reach other cluster members connecting to
	// this proxy over a tunnel.
	ReverseTunnelServer Server
	// Clock is a mock-able clock.
	Clock clockwork.Clock
	// KubeDialAddr is an optional address of a local kubernetes proxy.
	KubeDialAddr utils.NetAddr
	// FIPS indicates if Teleport was started in FIPS mode.
	FIPS bool
	// Log is the logger
	Log logrus.FieldLogger
	// LocalAuthAddresses is a list of auth servers to use when dialing back to
	// the local cluster.
	LocalAuthAddresses []string
}

func (c *RemoteClusterTunnelManagerConfig) CheckAndSetDefaults() error {
	if c.AuthClient == nil {
		return trace.BadParameter("missing AuthClient in RemoteClusterTunnelManagerConfig")
	}
	if c.AccessPoint == nil {
		return trace.BadParameter("missing AccessPoint in RemoteClusterTunnelManagerConfig")
	}
	if c.HostSigner == nil {
		return trace.BadParameter("missing HostSigner in RemoteClusterTunnelManagerConfig")
	}
	if c.HostUUID == "" {
		return trace.BadParameter("missing HostUUID in RemoteClusterTunnelManagerConfig")
	}
	if c.LocalCluster == "" {
		return trace.BadParameter("missing LocalCluster in RemoteClusterTunnelManagerConfig")
	}
	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}
	if c.Log == nil {
		c.Log = logrus.New()
	}

	return nil
}

// NewRemoteClusterTunnelManager creates a new stopped tunnel manager with
// the provided config. Call Run() to start the manager.
func NewRemoteClusterTunnelManager(cfg RemoteClusterTunnelManagerConfig) (*RemoteClusterTunnelManager, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	w := &RemoteClusterTunnelManager{
		cfg:          cfg,
		pools:        make(map[remoteClusterKey]*AgentPool),
		newAgentPool: realNewAgentPool,
	}
	return w, nil
}

// Close cleans up all outbound tunnels and stops the manager.
func (w *RemoteClusterTunnelManager) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k, pool := range w.pools {
		pool.Stop()
		delete(w.pools, k)
	}
	if w.stopRun != nil {
		w.stopRun()
		w.stopRun = nil
	}

	return nil
}

// Run runs the manager polling loop. Run is blocking, start it in a goroutine.
func (w *RemoteClusterTunnelManager) Run(ctx context.Context) {
	w.mu.Lock()
	ctx, w.stopRun = context.WithCancel(ctx)
	w.mu.Unlock()

	if err := w.Sync(ctx); err != nil {
		w.cfg.Log.Warningf("Failed to sync reverse tunnels: %v.", err)
	}

	ticker := time.NewTicker(defaults.ResyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.cfg.Log.Debugf("Closing.")
			return
		case <-ticker.C:
			if err := w.Sync(ctx); err != nil {
				w.cfg.Log.Warningf("Failed to sync reverse tunnels: %v.", err)
				continue
			}
		}
	}
}

// Sync does a one-time sync of trusted clusters with running agent pools.
// Non-test code should use Run() instead.
func (w *RemoteClusterTunnelManager) Sync(ctx context.Context) error {
	// Fetch desired reverse tunnels and convert them to a set of
	// remoteClusterKeys.
	wantTunnels, err := w.cfg.AuthClient.GetReverseTunnels(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	wantClusters := make(map[remoteClusterKey]bool, len(wantTunnels))
	for _, tun := range wantTunnels {
		for _, addr := range tun.GetDialAddrs() {
			wantClusters[remoteClusterKey{cluster: tun.GetClusterName(), addr: addr}] = true
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Delete pools that are no longer needed.
	for k, pool := range w.pools {
		if wantClusters[k] {
			continue
		}
		pool.Stop()
		trustedClustersStats.DeleteLabelValues(pool.Cluster)
		delete(w.pools, k)
	}

	// Start pools that were added since last sync.
	var errs []error
	for k := range wantClusters {
		if _, ok := w.pools[k]; ok {
			continue
		}

		trustedClustersStats.WithLabelValues(k.cluster).Set(0)
		pool, err := w.newAgentPool(ctx, w.cfg, k.cluster, k.addr)
		if err != nil {
			errs = append(errs, trace.Wrap(err))
			continue
		}
		w.pools[k] = pool
	}
	return trace.NewAggregate(errs...)
}

func realNewAgentPool(ctx context.Context, cfg RemoteClusterTunnelManagerConfig, cluster, addr string) (*AgentPool, error) {
	pool, err := NewAgentPool(ctx, AgentPoolConfig{
		// Configs for our cluster.
		Client:              cfg.AuthClient,
		AccessPoint:         cfg.AccessPoint,
		HostSigner:          cfg.HostSigner,
		HostUUID:            cfg.HostUUID,
		LocalCluster:        cfg.LocalCluster,
		Clock:               cfg.Clock,
		KubeDialAddr:        cfg.KubeDialAddr,
		ReverseTunnelServer: cfg.ReverseTunnelServer,
		FIPS:                cfg.FIPS,
		LocalAuthAddresses:  cfg.LocalAuthAddresses,
		// RemoteClusterManager only runs on proxies.
		Component: teleport.ComponentProxy,

		// Configs for remote cluster.
		Cluster:         cluster,
		Resolver:        StaticResolver(addr, apitypes.ProxyListenerMode_Separate),
		IsRemoteCluster: true,
	})
	if err != nil {
		return nil, trace.Wrap(err, "failed creating reverse tunnel pool for remote cluster %q at address %q: %v", cluster, addr, err)
	}

	if err := pool.Start(); err != nil {
		cfg.Log.WithError(err).Error("Failed to start agent pool")
	}

	return pool, nil
}

// Counts returns the number of tunnels for each remote cluster.
func (w *RemoteClusterTunnelManager) Counts() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := make(map[string]int, len(w.pools))
	for n, p := range w.pools {
		counts[n.cluster] += p.Count()
	}
	return counts
}
