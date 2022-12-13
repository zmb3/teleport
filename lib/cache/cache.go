/*
Copyright 2018-2019 Gravitational, Inc.

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

package cache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/client/proto"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/retryutils"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/observability/metrics"
	"github.com/zmb3/teleport/lib/observability/tracing"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/services/local"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/interval"
)

var (
	cacheEventsReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: teleport.MetricNamespace,
			Name:      teleport.MetricCacheEventsReceived,
			Help:      "Number of events received by a Teleport service cache. Teleport's Auth Service, Proxy Service, and other services cache incoming events related to their service.",
		},
		[]string{teleport.TagCacheComponent},
	)
	cacheStaleEventsReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: teleport.MetricNamespace,
			Name:      teleport.MetricStaleCacheEventsReceived,
			Help:      "Number of stale events received by a Teleport service cache. A high percentage of stale events can indicate a degraded backend.",
		},
		[]string{teleport.TagCacheComponent},
	)

	cacheCollectors = []prometheus.Collector{cacheEventsReceived, cacheStaleEventsReceived}
)

// ForAuth sets up watch configuration for the auth server
func ForAuth(cfg Config) Config {
	cfg.target = "auth"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: true},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindStaticTokens},
		{Kind: types.KindToken},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace},
		{Kind: types.KindNode},
		{Kind: types.KindProxy},
		{Kind: types.KindAuthServer},
		{Kind: types.KindReverseTunnel},
		{Kind: types.KindTunnelConnection},
		{Kind: types.KindAccessRequest},
		{Kind: types.KindAppServer},
		{Kind: types.KindApp},
		{Kind: types.KindAppServer, Version: types.V2},
		{Kind: types.KindWebSession, SubKind: types.KindSnowflakeSession},
		{Kind: types.KindWebSession, SubKind: types.KindAppSession},
		{Kind: types.KindWebSession, SubKind: types.KindWebSession},
		{Kind: types.KindWebToken},
		{Kind: types.KindRemoteCluster},
		{Kind: types.KindKubeService},
		{Kind: types.KindDatabaseServer},
		{Kind: types.KindDatabase},
		{Kind: types.KindNetworkRestrictions},
		{Kind: types.KindLock},
		{Kind: types.KindWindowsDesktopService},
		{Kind: types.KindWindowsDesktop},
		{Kind: types.KindKubeServer},
		{Kind: types.KindInstaller},
		{Kind: types.KindKubernetesCluster},
	}
	cfg.QueueSize = defaults.AuthQueueSize
	return cfg
}

// ForProxy sets up watch configuration for proxy
func ForProxy(cfg Config) Config {
	cfg.target = "proxy"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace},
		{Kind: types.KindNode},
		{Kind: types.KindProxy},
		{Kind: types.KindAuthServer},
		{Kind: types.KindReverseTunnel},
		{Kind: types.KindTunnelConnection},
		{Kind: types.KindAppServer},
		{Kind: types.KindAppServer, Version: types.V2},
		{Kind: types.KindApp},
		{Kind: types.KindWebSession, SubKind: types.KindSnowflakeSession},
		{Kind: types.KindWebSession, SubKind: types.KindAppSession},
		{Kind: types.KindWebSession, SubKind: types.KindWebSession},
		{Kind: types.KindWebToken},
		{Kind: types.KindRemoteCluster},
		{Kind: types.KindKubeService},
		{Kind: types.KindDatabaseServer},
		{Kind: types.KindDatabase},
		{Kind: types.KindWindowsDesktopService},
		{Kind: types.KindWindowsDesktop},
		{Kind: types.KindKubeServer},
		{Kind: types.KindInstaller},
		{Kind: types.KindKubernetesCluster},
	}
	cfg.QueueSize = defaults.ProxyQueueSize
	return cfg
}

// ForRemoteProxy sets up watch configuration for remote proxies.
func ForRemoteProxy(cfg Config) Config {
	cfg.target = "remote-proxy"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace},
		{Kind: types.KindNode},
		{Kind: types.KindProxy},
		{Kind: types.KindAuthServer},
		{Kind: types.KindReverseTunnel},
		{Kind: types.KindTunnelConnection},
		{Kind: types.KindAppServer},
		{Kind: types.KindAppServer, Version: types.V2},
		{Kind: types.KindRemoteCluster},
		{Kind: types.KindKubeService},
		{Kind: types.KindDatabaseServer},
		{Kind: types.KindKubeServer},
	}
	cfg.QueueSize = defaults.ProxyQueueSize
	return cfg
}

// ForOldRemoteProxy sets up watch configuration for older remote proxies.
// The Watches defined here are a copy of those defined in ForRemoteProxy in the v10 branch.
func ForOldRemoteProxy(cfg Config) Config {
	cfg.target = "remote-proxy-old"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace},
		{Kind: types.KindNode},
		{Kind: types.KindProxy},
		{Kind: types.KindAuthServer},
		{Kind: types.KindReverseTunnel},
		{Kind: types.KindTunnelConnection},
		{Kind: types.KindAppServer},
		{Kind: types.KindAppServer, Version: types.V2},
		{Kind: types.KindRemoteCluster},
		{Kind: types.KindKubeService},
		{Kind: types.KindDatabaseServer},
	}
	cfg.QueueSize = defaults.ProxyQueueSize
	return cfg
}

// ForNode sets up watch configuration for node
func ForNode(cfg Config) Config {
	var caFilter map[string]string
	if cfg.ClusterConfig != nil {
		clusterName, err := cfg.ClusterConfig.GetClusterName()
		if err == nil {
			caFilter = types.CertAuthorityFilter{
				types.HostCA: clusterName.GetClusterName(),
				types.UserCA: types.Wildcard,
			}.IntoMap()
		}
	}
	cfg.target = "node"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, Filter: caFilter},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindRole},
		// Node only needs to "know" about default
		// namespace events to avoid matching too much
		// data about other namespaces or node events
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindNetworkRestrictions},
	}

	cfg.QueueSize = defaults.NodeQueueSize
	return cfg
}

// ForKubernetes sets up watch configuration for a kubernetes service.
func ForKubernetes(cfg Config) Config {
	cfg.target = "kube"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindKubeService},
		{Kind: types.KindKubeServer},
		{Kind: types.KindKubernetesCluster},
	}
	cfg.QueueSize = defaults.KubernetesQueueSize
	return cfg
}

// ForApps sets up watch configuration for apps.
func ForApps(cfg Config) Config {
	cfg.target = "apps"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindProxy},
		// Applications only need to "know" about default namespace events to avoid
		// matching too much data about other namespaces or events.
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindApp},
	}
	cfg.QueueSize = defaults.AppsQueueSize
	return cfg
}

// ForDatabases sets up watch configuration for database proxy servers.
func ForDatabases(cfg Config) Config {
	cfg.target = "db"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindProxy},
		// Databases only need to "know" about default namespace events to
		// avoid matching too much data about other namespaces or events.
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindDatabase},
	}
	cfg.QueueSize = defaults.DatabasesQueueSize
	return cfg
}

// ForWindowsDesktop sets up watch configuration for a Windows desktop service.
func ForWindowsDesktop(cfg Config) Config {
	cfg.target = "windows_desktop"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindClusterAuditConfig},
		{Kind: types.KindClusterNetworkingConfig},
		{Kind: types.KindClusterAuthPreference},
		{Kind: types.KindSessionRecordingConfig},
		{Kind: types.KindUser},
		{Kind: types.KindRole},
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindWindowsDesktopService},
		{Kind: types.KindWindowsDesktop},
	}
	cfg.QueueSize = defaults.WindowsDesktopQueueSize
	return cfg
}

// ForDiscovery sets up watch configuration for discovery servers.
func ForDiscovery(cfg Config) Config {
	cfg.target = "discovery"
	cfg.Watches = []types.WatchKind{
		{Kind: types.KindCertAuthority, LoadSecrets: false},
		{Kind: types.KindClusterName},
		{Kind: types.KindNamespace, Name: apidefaults.Namespace},
		{Kind: types.KindNode},
		{Kind: types.KindKubernetesCluster},
	}
	cfg.QueueSize = defaults.DiscoveryQueueSize
	return cfg
}

// SetupConfigFn is a function that sets up configuration
// for cache
type SetupConfigFn func(c Config) Config

// Cache implements auth.Cache interface and remembers
// the previously returned upstream value for each API call.
//
// This which can be used if the upstream AccessPoint goes offline
type Cache struct {
	Config

	// Entry is a logging entry
	Logger *log.Entry

	// rw is used to prevent reads of invalid cache states.  From a
	// memory-safety perspective, this RWMutex is just used to protect
	// the `ok` field.  *However*, cache reads must hold the read lock
	// for the duration of the read, not just when checking the `ok`
	// field.  Since the write lock must be held in order to modify
	// the `ok` field, this serves to ensure that all in-progress reads
	// complete *before* a reset can begin.
	rw sync.RWMutex
	// ok indicates whether the cache is in a valid state for reads.
	// If `ok` is `false`, reads are forwarded directly to the backend.
	ok bool

	// generation is a counter that is incremented each time a healthy
	// state is established.  A generation of zero means that a healthy
	// state was never established.  Note that a generation of zero does
	// not preclude `ok` being true in the case that we have loaded a
	// previously healthy state from the backend.
	generation atomic.Uint64

	// initOnce protects initC and initErr.
	initOnce sync.Once
	// initC is closed on the first attempt to initialize the
	// cache, whether or not it is successful.  Once initC
	// has returned, initErr is safe to read.
	initC chan struct{}
	// initErr is set if the first attempt to initialize the cache
	// fails.
	initErr error

	// ctx is a cache exit context
	ctx context.Context
	// cancel triggers exit context closure
	cancel context.CancelFunc

	// collections is a map of registered collections by resource Kind/SubKind
	collections map[resourceKind]collection

	// fnCache is used to perform short ttl-based caching of the results of
	// regularly called methods.
	fnCache *utils.FnCache

	trustCache            services.Trust
	clusterConfigCache    services.ClusterConfiguration
	provisionerCache      services.Provisioner
	usersCache            services.UsersService
	accessCache           services.Access
	dynamicAccessCache    services.DynamicAccessExt
	presenceCache         services.Presence
	restrictionsCache     services.Restrictions
	appsCache             services.Apps
	kubernetesCache       services.Kubernetes
	databasesCache        services.Databases
	appSessionCache       services.AppSession
	snowflakeSessionCache services.SnowflakeSession
	webSessionCache       types.WebSessionInterface
	webTokenCache         types.WebTokenInterface
	windowsDesktopsCache  services.WindowsDesktops
	eventsFanout          *services.FanoutSet

	// closed indicates that the cache has been closed
	closed atomic.Bool
}

func (c *Cache) setInitError(err error) {
	c.initOnce.Do(func() {
		c.initErr = err
		close(c.initC)
	})
}

// setReadOK updates Cache.ok, which determines whether the
// cache is accessible for reads.
func (c *Cache) setReadOK(ok bool) {
	if c.neverOK {
		// we are running inside of a test where the cache
		// needs to pretend that it never becomes healthy.
		return
	}
	if ok == c.getReadOK() {
		return
	}
	c.rw.Lock()
	defer c.rw.Unlock()
	c.ok = ok
}

func (c *Cache) getReadOK() (ok bool) {
	c.rw.RLock()
	defer c.rw.RUnlock()
	return c.ok
}

// read acquires the cache read lock and selects the appropriate
// target for read operations.  The returned guard *must* be
// released to prevent deadlocks.
func (c *Cache) read() (readGuard, error) {
	if c.closed.Load() {
		return readGuard{}, trace.Errorf("cache is closed")
	}
	c.rw.RLock()
	if c.ok {
		return readGuard{
			trust:            c.trustCache,
			clusterConfig:    c.clusterConfigCache,
			provisioner:      c.provisionerCache,
			users:            c.usersCache,
			access:           c.accessCache,
			dynamicAccess:    c.dynamicAccessCache,
			presence:         c.presenceCache,
			restrictions:     c.restrictionsCache,
			apps:             c.appsCache,
			kubernetes:       c.kubernetesCache,
			databases:        c.databasesCache,
			appSession:       c.appSessionCache,
			snowflakeSession: c.snowflakeSessionCache,
			webSession:       c.webSessionCache,
			webToken:         c.webTokenCache,
			release:          c.rw.RUnlock,
			windowsDesktops:  c.windowsDesktopsCache,
		}, nil
	}
	c.rw.RUnlock()
	return readGuard{
		trust:            c.Config.Trust,
		clusterConfig:    c.Config.ClusterConfig,
		provisioner:      c.Config.Provisioner,
		users:            c.Config.Users,
		access:           c.Config.Access,
		dynamicAccess:    c.Config.DynamicAccess,
		presence:         c.Config.Presence,
		restrictions:     c.Config.Restrictions,
		apps:             c.Config.Apps,
		kubernetes:       c.Config.Kubernetes,
		databases:        c.Config.Databases,
		appSession:       c.Config.AppSession,
		snowflakeSession: c.Config.SnowflakeSession,
		webSession:       c.Config.WebSession,
		webToken:         c.Config.WebToken,
		windowsDesktops:  c.Config.WindowsDesktops,
		release:          nil,
	}, nil
}

// readGuard holds references to a "backend".  if the referenced
// backed is the cache, then readGuard also holds the release
// function for the read lock, and ensures that it is not
// double-called.
type readGuard struct {
	trust            services.Trust
	clusterConfig    services.ClusterConfiguration
	provisioner      services.Provisioner
	users            services.UsersService
	access           services.Access
	dynamicAccess    services.DynamicAccessCore
	presence         services.Presence
	appSession       services.AppSession
	snowflakeSession services.SnowflakeSession
	restrictions     services.Restrictions
	apps             services.Apps
	kubernetes       services.Kubernetes
	databases        services.Databases
	webSession       types.WebSessionInterface
	webToken         types.WebTokenInterface
	windowsDesktops  services.WindowsDesktops
	release          func()
	released         bool
}

// Release releases the read lock if it is held.  This method
// can be called multiple times, but is not thread-safe.
func (r *readGuard) Release() {
	if r.release != nil && !r.released {
		r.release()
		r.released = true
	}
}

// IsCacheRead checks if this readGuard holds a cache reference.
func (r *readGuard) IsCacheRead() bool {
	return r.release != nil
}

// Config defines cache configuration parameters
type Config struct {
	// target is an identifying string that allows errors to
	// indicate the target presets used (e.g. "auth").
	target string
	// Context is context for parent operations
	Context context.Context
	// Watches provides a list of resources
	// for the cache to watch
	Watches []types.WatchKind
	// Events provides events watchers
	Events types.Events
	// Trust is a service providing information about certificate
	// authorities
	Trust services.Trust
	// ClusterConfig is a cluster configuration service
	ClusterConfig services.ClusterConfiguration
	// Provisioner is a provisioning service
	Provisioner services.Provisioner
	// Users is a users service
	Users services.UsersService
	// Access is an access service
	Access services.Access
	// DynamicAccess is a dynamic access service
	DynamicAccess services.DynamicAccessCore
	// Presence is a presence service
	Presence services.Presence
	// Restrictions is a restrictions service
	Restrictions services.Restrictions
	// Apps is an apps service.
	Apps services.Apps
	// Kubernetes is an kubernetes service.
	Kubernetes services.Kubernetes
	// Databases is a databases service.
	Databases services.Databases
	// SnowflakeSession holds Snowflake sessions.
	SnowflakeSession services.SnowflakeSession
	// AppSession holds application sessions.
	AppSession services.AppSession
	// WebSession holds regular web sessions.
	WebSession types.WebSessionInterface
	// WebToken holds web tokens.
	WebToken types.WebTokenInterface
	// WindowsDesktops is a windows desktop service.
	WindowsDesktops services.WindowsDesktops
	// Backend is a backend for local cache
	Backend backend.Backend
	// MaxRetryPeriod is the maximum period between cache retries on failures
	MaxRetryPeriod time.Duration
	// WatcherInitTimeout is the maximum acceptable delay for an
	// OpInit after a watcher has been started (default=1m).
	WatcherInitTimeout time.Duration
	// CacheInitTimeout is the maximum amount of time that cache.New
	// will block, waiting for initialization (default=20s).
	CacheInitTimeout time.Duration
	// RelativeExpiryCheckInterval determines how often the cache performs special
	// "relative expiration" checks which are used to compensate for real backends
	// that have suffer from overly lazy ttl'ing of resources.
	RelativeExpiryCheckInterval time.Duration
	// RelativeExpiryLimit determines the maximum number of nodes that may be
	// removed during relative expiration.
	RelativeExpiryLimit int
	// EventsC is a channel for event notifications,
	// used in tests
	EventsC chan Event
	// Clock can be set to control time,
	// uses runtime clock by default
	Clock clockwork.Clock
	// Component is a component used in logs
	Component string
	// MetricComponent is a component used in metrics
	MetricComponent string
	// QueueSize is a desired queue Size
	QueueSize int
	// neverOK is used in tests to create a cache that appears to never
	// becomes healthy, meaning that it will always end up hitting the
	// real backend and the ttl cache.
	neverOK bool
	// Tracer is used to create spans
	Tracer oteltrace.Tracer
	// Unstarted indicates that the cache should not be started during New. The
	// cache is usable before it's started, but it will always hit the backend.
	Unstarted bool
}

// CheckAndSetDefaults checks parameters and sets default values
func (c *Config) CheckAndSetDefaults() error {
	if c.Events == nil {
		return trace.BadParameter("missing Events parameter")
	}
	if c.Backend == nil {
		return trace.BadParameter("missing Backend parameter")
	}
	if c.Context == nil {
		c.Context = context.Background()
	}
	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}
	if c.MaxRetryPeriod == 0 {
		c.MaxRetryPeriod = defaults.MaxWatcherBackoff
	}
	if c.WatcherInitTimeout == 0 {
		c.WatcherInitTimeout = time.Minute
	}
	if c.CacheInitTimeout == 0 {
		c.CacheInitTimeout = time.Second * 20
	}
	if c.RelativeExpiryCheckInterval == 0 {
		c.RelativeExpiryCheckInterval = apidefaults.ServerKeepAliveTTL() + 5*time.Second
	}
	if c.RelativeExpiryLimit == 0 {
		c.RelativeExpiryLimit = 2000
	}
	if c.Component == "" {
		c.Component = teleport.ComponentCache
	}
	if c.Tracer == nil {
		c.Tracer = tracing.NoopTracer(c.Component)
	}
	return nil
}

// Event is event used in tests
type Event struct {
	// Type is event type
	Type string
	// Event is event processed
	// by the event cycle
	Event types.Event
}

const (
	// EventProcessed is emitted whenever event is processed
	EventProcessed = "event_processed"
	// WatcherStarted is emitted when a new event watcher is started
	WatcherStarted = "watcher_started"
	// WatcherFailed is emitted when event watcher has failed
	WatcherFailed = "watcher_failed"
	// Reloading is emitted when an error occurred watching events
	// and the cache is waiting to create a new watcher
	Reloading = "reloading_cache"
	// RelativeExpiry notifies that relative expiry operations have
	// been run.
	RelativeExpiry = "relative_expiry"
)

// New creates a new instance of Cache
func New(config Config) (*Cache, error) {
	if err := metrics.RegisterPrometheusCollectors(cacheCollectors...); err != nil {
		return nil, trace.Wrap(err)
	}
	if err := config.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	clusterConfigCache, err := local.NewClusterConfigurationService(config.Backend)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ctx, cancel := context.WithCancel(config.Context)
	fnCache, err := utils.NewFnCache(utils.FnCacheConfig{
		TTL:     time.Second,
		Clock:   config.Clock,
		Context: ctx,
	})
	if err != nil {
		cancel()
		return nil, trace.Wrap(err)
	}

	cs := &Cache{
		ctx:                   ctx,
		cancel:                cancel,
		Config:                config,
		initC:                 make(chan struct{}),
		fnCache:               fnCache,
		trustCache:            local.NewCAService(config.Backend),
		clusterConfigCache:    clusterConfigCache,
		provisionerCache:      local.NewProvisioningService(config.Backend),
		usersCache:            local.NewIdentityService(config.Backend),
		accessCache:           local.NewAccessService(config.Backend),
		dynamicAccessCache:    local.NewDynamicAccessService(config.Backend),
		presenceCache:         local.NewPresenceService(config.Backend),
		restrictionsCache:     local.NewRestrictionsService(config.Backend),
		appsCache:             local.NewAppService(config.Backend),
		kubernetesCache:       local.NewKubernetesService(config.Backend),
		databasesCache:        local.NewDatabasesService(config.Backend),
		appSessionCache:       local.NewIdentityService(config.Backend),
		snowflakeSessionCache: local.NewIdentityService(config.Backend),
		webSessionCache:       local.NewIdentityService(config.Backend).WebSessions(),
		webTokenCache:         local.NewIdentityService(config.Backend).WebTokens(),
		windowsDesktopsCache:  local.NewWindowsDesktopService(config.Backend),
		eventsFanout:          services.NewFanoutSet(),
		Logger: log.WithFields(log.Fields{
			trace.Component: config.Component,
		}),
	}
	collections, err := setupCollections(cs, config.Watches)
	if err != nil {
		cs.Close()
		return nil, trace.Wrap(err)
	}
	cs.collections = collections

	if config.Unstarted {
		return cs, nil
	}

	if err := cs.Start(); err != nil {
		cs.Close()
		return nil, trace.Wrap(err)
	}

	return cs, nil
}

// Start the cache. Should only be called once.
func (c *Cache) Start() error {
	retry, err := retryutils.NewLinear(retryutils.LinearConfig{
		First:  utils.HalfJitter(c.MaxRetryPeriod / 10),
		Step:   c.MaxRetryPeriod / 5,
		Max:    c.MaxRetryPeriod,
		Jitter: retryutils.NewHalfJitter(),
		Clock:  c.Clock,
	})
	if err != nil {
		c.Close()
		return trace.Wrap(err)
	}

	go c.update(c.ctx, retry)

	select {
	case <-c.initC:
		if c.initErr == nil {
			c.Logger.Infof("Cache %q first init succeeded.", c.Config.target)
		} else {
			c.Logger.WithError(c.initErr).Warnf("Cache %q first init failed, continuing re-init attempts in background.", c.Config.target)
		}
	case <-c.ctx.Done():
		c.Close()
		return trace.Wrap(c.ctx.Err(), "context closed during cache init")
	case <-time.After(c.Config.CacheInitTimeout):
		c.Logger.Warn("Cache init is taking too long, will continue in background.")
	}
	return nil
}

// NewWatcher returns a new event watcher. In case of a cache
// this watcher will return events as seen by the cache,
// not the backend. This feature allows auth server
// to handle subscribers connected to the in-memory caches
// instead of reading from the backend.
func (c *Cache) NewWatcher(ctx context.Context, watch types.Watch) (types.Watcher, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/NewWatcher")
	defer span.End()
Outer:
	for _, requested := range watch.Kinds {
		for _, configured := range c.Config.Watches {
			if requested.Kind == configured.Kind {
				continue Outer
			}
		}
		return nil, trace.BadParameter("cache %q does not support watching resource %q", c.Config.target, requested.Kind)
	}
	return c.eventsFanout.NewWatcher(ctx, watch)
}

func (c *Cache) update(ctx context.Context, retry retryutils.Retry) {
	defer func() {
		c.Logger.Debug("Cache is closing, returning from update loop.")
		// ensure that close operations have been run
		c.Close()
	}()
	timer := time.NewTimer(c.Config.WatcherInitTimeout)
	for {
		err := c.fetchAndWatch(ctx, retry, timer)
		c.setInitError(err)
		if c.isClosing() {
			return
		}
		if err != nil {
			c.Logger.WithError(err).Warn("Re-init the cache on error")
		}

		// events cache should be closed as well
		c.Logger.Debug("Reloading cache.")

		c.notify(ctx, Event{Type: Reloading, Event: types.Event{
			Resource: &types.ResourceHeader{
				Kind: retry.Duration().String(),
			},
		}})

		startedWaiting := c.Clock.Now()
		select {
		case t := <-retry.After():
			c.Logger.Debugf("Initiating new watch after waiting %v.", t.Sub(startedWaiting))
			retry.Inc()
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Cache) notify(ctx context.Context, event Event) {
	if c.EventsC == nil {
		return
	}
	select {
	case c.EventsC <- event:
		return
	case <-ctx.Done():
		return
	}
}

// fetchAndWatch keeps cache up to date by replaying
// events and syncing local cache storage.
//
// Here are some thoughts on consistency in face of errors:
//
// 1. Every client is connected to the database fan-out
// system. This system creates a buffered channel for every
// client and tracks the channel overflow. Thanks to channels every client gets its
// own unique iterator over the event stream. If client loses connection
// or fails to keep up with the stream, the server will terminate
// the channel and client will have to re-initialize.
//
// 2. Replays of stale events. Etcd provides a strong
// mechanism to track the versions of the storage - revisions
// of every operation that are uniquely numbered and monotonically
// and consistently ordered thanks to Raft. Unfortunately, DynamoDB
// does not provide such a mechanism for its event system, so
// some tradeoffs have to be made:
//
//	a. We assume that events are ordered in regards to the
//	individual key operations which is the guarantees both Etcd and DynamoDB
//	provide.
//	b. Thanks to the init event sent by the server on a successful connect,
//	and guarantees 1 and 2a, client assumes that once it connects and receives an event,
//	it will not miss any events, however it can receive stale events.
//	Event could be stale, if it relates to a change that happened before
//	the version read by client from the database, for example,
//	given the event stream: 1. Update a=1 2. Delete a 3. Put a = 2
//	Client could have subscribed before event 1 happened,
//	read the value a=2 and then received events 1 and 2 and 3.
//	The cache will replay all events 1, 2 and 3 and end up in the correct
//	state 3. If we had a consistent revision number, we could
//	have skipped 1 and 2, but in the absence of such mechanism in Dynamo
//	we assume that this cache will eventually end up in a correct state
//	potentially lagging behind the state of the database.
func (c *Cache) fetchAndWatch(ctx context.Context, retry retryutils.Retry, timer *time.Timer) error {
	watcher, err := c.Events.NewWatcher(c.ctx, types.Watch{
		QueueSize:       c.QueueSize,
		Name:            c.Component,
		Kinds:           c.watchKinds(),
		MetricComponent: c.MetricComponent,
	})
	if err != nil {
		c.notify(c.ctx, Event{Type: WatcherFailed})
		return trace.Wrap(err)
	}
	defer watcher.Close()

	// ensure that the timer is stopped and drained
	timer.Stop()
	select {
	case <-timer.C:
	default:
	}
	// set timer to watcher init timeout
	timer.Reset(c.Config.WatcherInitTimeout)

	// before fetch, make sure watcher is synced by receiving init event,
	// to avoid the scenario:
	// 1. Cache process:   w = NewWatcher()
	// 2. Cache process:   c.fetch()
	// 3. Backend process: addItem()
	// 4. Cache process:   <- w.Events()
	//
	// If there is a way that NewWatcher() on line 1 could
	// return without subscription established first,
	// Code line 3 could execute and line 4 could miss event,
	// wrapping up without of sync replica.
	// To avoid this, before doing fetch,
	// cache process makes sure the connection is established
	// by receiving init event first.
	select {
	case <-watcher.Done():
		return trace.ConnectionProblem(watcher.Error(), "watcher is closed: %v", watcher.Error())
	case <-c.ctx.Done():
		return trace.ConnectionProblem(c.ctx.Err(), "context is closing")
	case event := <-watcher.Events():
		if event.Type != types.OpInit {
			return trace.BadParameter("expected init event, got %v instead", event.Type)
		}
	case <-timer.C:
		return trace.ConnectionProblem(nil, "timeout waiting for watcher init")
	}
	apply, err := c.fetch(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	// apply will mutate cache, and possibly leave it in an invalid state
	// if an error occurs, so ensure that cache is not read.
	c.setReadOK(false)
	err = apply(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	// apply was successful; cache is now readable.
	c.generation.Add(1)
	c.setReadOK(true)
	c.setInitError(nil)

	// watchers have been queuing up since the last time
	// the cache was in a healthy state; broadcast OpInit.
	// It is very important that OpInit is not broadcast until
	// after we've placed the cache into a readable state.  This ensures
	// that any derivative caches do not perform their fetch operations
	// until this cache has finished its apply operations.
	c.eventsFanout.SetInit()
	defer c.eventsFanout.Reset()

	retry.Reset()

	// only enable relative node expiry if the cache is configured
	// to watch for types.KindNode
	relativeExpiryInterval := interval.NewNoop()
	for _, watch := range c.Config.Watches {
		if watch.Kind == types.KindNode {
			relativeExpiryInterval = interval.New(interval.Config{
				Duration:      c.Config.RelativeExpiryCheckInterval,
				FirstDuration: utils.HalfJitter(c.Config.RelativeExpiryCheckInterval),
				Jitter:        retryutils.NewSeventhJitter(),
			})
			break
		}
	}
	defer relativeExpiryInterval.Stop()

	c.notify(c.ctx, Event{Type: WatcherStarted})

	var lastStalenessWarning time.Time
	var staleEventCount int
	for {
		select {
		case <-watcher.Done():
			return trace.ConnectionProblem(watcher.Error(), "watcher is closed: %v", watcher.Error())
		case <-c.ctx.Done():
			return trace.ConnectionProblem(c.ctx.Err(), "context is closing")
		case <-relativeExpiryInterval.Next():
			if err := c.performRelativeNodeExpiry(ctx); err != nil {
				return trace.Wrap(err)
			}
			c.notify(ctx, Event{Type: RelativeExpiry})
		case event := <-watcher.Events():
			// check for expired resources in OpPut events and log them periodically. stale OpPut events
			// may be an indicator of poor performance, and can lead to confusing and inconsistent state
			// as the cache may prune items that aught to exist.
			//
			// NOTE: The inconsistent state mentioned above is a symptom of a deeper issue with the cache
			// design.  The cache should not expire individual items.  It should instead rely on OpDelete events
			// from backend expires. As soon as the cache has expired at least one item, it is no longer
			// a faithful representation of a real backend state, since it is 'anticipating' a change in
			// backend state that may or may not have actually happened.  Instead, it aught to serve the
			// most recent internally-consistent "view" of the backend, and individual consumers should
			// determine if the resources they are handling are sufficiently fresh.  Resource-level expiry
			// is a convenience/cleanup feature and aught not be relied upon for meaningful logic anyhow.
			// If we need to protect against a stale cache, we aught to invalidate the cache in its entirety, rather
			// than pruning the resources that we think *might* have been removed from the real backend.
			// TODO(fspmarshall): ^^^
			//
			cacheEventsReceived.WithLabelValues(c.target).Inc()
			if event.Type == types.OpPut && !event.Resource.Expiry().IsZero() {
				if now := c.Clock.Now(); now.After(event.Resource.Expiry()) {
					cacheStaleEventsReceived.WithLabelValues(c.target).Inc()
					staleEventCount++
					if now.After(lastStalenessWarning.Add(time.Minute)) {
						kind := event.Resource.GetKind()
						if sk := event.Resource.GetSubKind(); sk != "" {
							kind = fmt.Sprintf("%s/%s", kind, sk)
						}
						c.Logger.WithField("last_kind", kind).Warnf("Encountered %d stale event(s), may indicate degraded backend or event system performance.", staleEventCount)
						lastStalenessWarning = now
						staleEventCount = 0
					}
				}
			}

			err = c.processEvent(ctx, event, true)
			if err != nil {
				return trace.Wrap(err)
			}
			c.notify(c.ctx, Event{Event: event, Type: EventProcessed})
		}
	}
}

// performRelativeNodeExpiry performs a special kind of active expiration where we remove nodes
// which are clearly stale relative to their more recently heartbeated counterparts as well as
// the current time. This strategy lets us side-step issues of clock drift or general cache
// staleness by only removing items which are stale from within the cache's own "frame of
// reference".
//
// to better understand why we use this expiry strategy, it's important to understand the two
// distinct scenarios that we're trying to accommodate:
//
// 1. Expiry events are being emitted very lazily by the real backend (*hours* after the time
// at which the resource was supposed to expire).
//
// 2. The event stream itself is stale (i.e. all events show up late, not just expiry events).
//
// In the first scenario, removing items from the cache after they have passed some designated
// threshold of staleness seems reasonable.  In the second scenario, your best option is to
// faithfully serve the delayed, but internally consistent, view created by the event stream and
// not expire any items.
//
// Relative expiry is the compromise between the two above scenarios. We calculate a staleness
// threshold after which items would be removed, but we calculate it relative to the most recent
// expiry *or* the current time, depending on which is earlier. The result is that nodes are
// removed only if they are both stale from the perspective of the current clock, *and* stale
// relative to our current view of the world.
//
// *note*: this function is only sane to call when the cache and event stream are healthy, and
// cannot run concurrently with event processing.
func (c *Cache) performRelativeNodeExpiry(ctx context.Context) error {
	// TODO(fspmarshall): Start using dynamic value once it is implemented.
	gracePeriod := apidefaults.ServerAnnounceTTL

	// latestExp will be the value that we choose to consider the most recent "expired"
	// timestamp.  This will either end up being the most recently seen node expiry, or
	// the current time (whichever is earlier).
	var latestExp time.Time

	nodes, err := c.GetNodes(ctx, apidefaults.Namespace)
	if err != nil {
		return trace.Wrap(err)
	}

	// iterate nodes and determine the most recent expiration value.
	for _, node := range nodes {
		if node.Expiry().IsZero() {
			continue
		}

		if node.Expiry().After(latestExp) || latestExp.IsZero() {
			// this node's expiry is more recent than the previously
			// recorded value.
			latestExp = node.Expiry()
		}
	}

	if latestExp.IsZero() {
		return nil
	}

	// if the most recent expiration value is still in the future, we use the current time
	// as the most recent expiration value instead. Unless the event stream is unhealthy, or
	// all nodes were recently removed, this should always be true.
	if now := c.Clock.Now(); latestExp.After(now) {
		latestExp = now
	}

	// we subtract gracePeriod from our most recent expiry value to get the retention
	// threshold. nodes which expired earlier than the retention threshold will be
	// removed, as we expect well-behaved backends to have emitted an expiry event
	// within the grace period.
	retentionThreshold := latestExp.Add(-gracePeriod)

	var removed int
	for _, node := range nodes {
		if node.Expiry().IsZero() || node.Expiry().After(retentionThreshold) {
			continue
		}

		// remove the node locally without emitting an event, other caches will
		// eventually remove the same node when they run their expiry logic.
		if err := c.processEvent(ctx, types.Event{
			Type: types.OpDelete,
			Resource: &types.ResourceHeader{
				Kind:     types.KindNode,
				Metadata: node.GetMetadata(),
			},
		}, false); err != nil {
			return trace.Wrap(err)
		}

		// high churn rates can cause purging a very large number of nodes
		// per interval, limit to a sane number such that we don't overwhelm
		// things or get too far out of sync with other caches.
		if removed++; removed >= c.Config.RelativeExpiryLimit {
			break
		}
	}

	if removed > 0 {
		c.Logger.Debugf("Removed %d nodes via relative expiry (retentionThreshold=%s).", removed, retentionThreshold)
	}

	return nil
}

func (c *Cache) watchKinds() []types.WatchKind {
	out := make([]types.WatchKind, 0, len(c.collections))
	for _, collection := range c.collections {
		out = append(out, collection.watchKind())
	}
	return out
}

// isClosing checks if the cache has begun closing.
func (c *Cache) isClosing() bool {
	if c.closed.Load() {
		// closing due to Close being called
		return true
	}

	select {
	case <-c.ctx.Done():
		// closing due to context cancellation
		return true
	default:
		// not closing
		return false
	}
}

// Close closes all outstanding and active cache operations
func (c *Cache) Close() error {
	c.closed.Store(true)
	c.cancel()
	c.eventsFanout.Close()
	return nil
}

// applyFn applies the fetched resources for a
// particular collection
type applyFn func(ctx context.Context) error

// tracedApplyFn wraps an apply function with a span that is
// a child of the provided parent span. Since the context provided
// to the applyFn won't be from fetch, we need to manually link
// the spans.
func tracedApplyFn(parent oteltrace.Span, tracer oteltrace.Tracer, kind resourceKind, f applyFn) applyFn {
	return func(ctx context.Context) (err error) {
		ctx, span := tracer.Start(
			oteltrace.ContextWithSpan(ctx, parent),
			fmt.Sprintf("cache/apply/%s", kind.String()),
			oteltrace.WithAttributes(
				attribute.String("version", kind.version),
			),
		)

		defer func() {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()
		}()

		return f(ctx)
	}
}

// fetchLimit determines the parallelism of the
// fetch operations based on the target. Both the
// auth and proxy caches are permitted to run parallel
// fetches for resources, while all other targets are
// throttled to limit load spiking during a mass
// restart of nodes
func fetchLimit(target string) int {
	switch target {
	case "auth", "proxy":
		return 5
	}

	return 1
}

func (c *Cache) fetch(ctx context.Context) (fn applyFn, err error) {
	ctx, fetchSpan := c.Tracer.Start(ctx, "cache/fetch", oteltrace.WithAttributes(attribute.String("target", c.target)))
	defer func() {
		if err != nil {
			fetchSpan.RecordError(err)
			fetchSpan.SetStatus(codes.Error, err.Error())
		}
		fetchSpan.End()
	}()

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchLimit(c.target))
	applyfns := make([]applyFn, len(c.collections))
	i := 0
	for kind, collection := range c.collections {
		kind, collection := kind, collection
		ii := i
		i++

		g.Go(func() (err error) {
			ctx, span := c.Tracer.Start(
				ctx,
				fmt.Sprintf("cache/fetch/%s", kind.String()),
				oteltrace.WithAttributes(
					attribute.String("target", c.target),
				),
			)
			defer func() {
				if err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
				}
				span.End()
			}()

			applyfn, err := collection.fetch(ctx)
			if err != nil {
				return trace.Wrap(err, "failed to fetch resource: %q", kind)
			}

			applyfns[ii] = tracedApplyFn(fetchSpan, c.Tracer, kind, applyfn)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, trace.Wrap(err)
	}

	return func(ctx context.Context) error {
		for _, applyfn := range applyfns {
			if err := applyfn(ctx); err != nil {
				return trace.Wrap(err)
			}
		}
		return nil
	}, nil
}

// processEvent hands the event off to the appropriate collection for processing. Any
// resources which were not registered are ignored. If processing completed successfully
// and emit is true the event will be emitted via the fanout.
func (c *Cache) processEvent(ctx context.Context, event types.Event, emit bool) error {
	resourceKind := resourceKindFromResource(event.Resource)
	collection, ok := c.collections[resourceKind]
	if !ok {
		c.Logger.Warnf("Skipping unsupported event %v/%v", event.Resource.GetKind(), event.Resource.GetSubKind())
		return nil
	}
	if err := collection.processEvent(ctx, event); err != nil {
		return trace.Wrap(err)
	}
	if emit {
		c.eventsFanout.Emit(event)
	}
	return nil
}

type getCertAuthorityCacheKey struct {
	id types.CertAuthID
}

var _ map[getCertAuthorityCacheKey]struct{} // compile-time hashability check

// GetCertAuthority returns certificate authority by given id. Parameter loadSigningKeys
// controls if signing keys are loaded
func (c *Cache) GetCertAuthority(ctx context.Context, id types.CertAuthID, loadSigningKeys bool, opts ...services.MarshalOption) (types.CertAuthority, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetCertAuthority")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	if !rg.IsCacheRead() && !loadSigningKeys {
		cachedCA, err := utils.FnCacheGet(ctx, c.fnCache, getCertAuthorityCacheKey{id}, func(ctx context.Context) (types.CertAuthority, error) {
			ca, err := rg.trust.GetCertAuthority(ctx, id, loadSigningKeys, opts...)
			return ca, err
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return cachedCA.Clone(), nil
	}

	ca, err := rg.trust.GetCertAuthority(ctx, id, loadSigningKeys, opts...)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because method is never used
		// in construction of derivative caches.
		if ca, err := c.Config.Trust.GetCertAuthority(ctx, id, loadSigningKeys, opts...); err == nil {
			return ca, nil
		}
	}
	return ca, trace.Wrap(err)
}

type getCertAuthoritiesCacheKey struct {
	caType types.CertAuthType
}

var _ map[getCertAuthoritiesCacheKey]struct{} // compile-time hashability check

// GetCertAuthorities returns a list of authorities of a given type
// loadSigningKeys controls whether signing keys should be loaded or not
func (c *Cache) GetCertAuthorities(ctx context.Context, caType types.CertAuthType, loadSigningKeys bool, opts ...services.MarshalOption) ([]types.CertAuthority, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetCertAuthorities")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() && !loadSigningKeys {
		cachedCAs, err := utils.FnCacheGet(ctx, c.fnCache, getCertAuthoritiesCacheKey{caType}, func(ctx context.Context) ([]types.CertAuthority, error) {
			cas, err := rg.trust.GetCertAuthorities(ctx, caType, loadSigningKeys, opts...)
			return cas, trace.Wrap(err)
		})
		if err != nil || cachedCAs == nil {
			return nil, trace.Wrap(err)
		}
		cas := make([]types.CertAuthority, 0, len(cachedCAs))
		for _, ca := range cachedCAs {
			cas = append(cas, ca.Clone())
		}
		return cas, nil
	}
	return rg.trust.GetCertAuthorities(ctx, caType, loadSigningKeys, opts...)
}

// GetStaticTokens gets the list of static tokens used to provision nodes.
func (c *Cache) GetStaticTokens() (types.StaticTokens, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetStaticTokens")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.clusterConfig.GetStaticTokens()
}

// GetTokens returns all active (non-expired) provisioning tokens
func (c *Cache) GetTokens(ctx context.Context) ([]types.ProvisionToken, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetTokens")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.provisioner.GetTokens(ctx)
}

// GetToken finds and returns token by ID
func (c *Cache) GetToken(ctx context.Context, name string) (types.ProvisionToken, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetToken")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	token, err := rg.provisioner.GetToken(ctx, name)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because method is never used
		// in construction of derivative caches.
		if token, err := c.Config.Provisioner.GetToken(ctx, name); err == nil {
			return token, nil
		}
	}
	return token, trace.Wrap(err)
}

type clusterConfigCacheKey struct {
	kind string
}

var _ map[clusterConfigCacheKey]struct{} // compile-time hashability check

// GetClusterAuditConfig gets ClusterAuditConfig from the backend.
func (c *Cache) GetClusterAuditConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterAuditConfig, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetClusterAuditConfig")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() {
		cachedCfg, err := utils.FnCacheGet(ctx, c.fnCache, clusterConfigCacheKey{"audit"}, func(ctx context.Context) (types.ClusterAuditConfig, error) {
			cfg, err := rg.clusterConfig.GetClusterAuditConfig(ctx, opts...)
			return cfg, err
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return cachedCfg.Clone(), nil
	}
	return rg.clusterConfig.GetClusterAuditConfig(ctx, opts...)
}

// GetClusterNetworkingConfig gets ClusterNetworkingConfig from the backend.
func (c *Cache) GetClusterNetworkingConfig(ctx context.Context, opts ...services.MarshalOption) (types.ClusterNetworkingConfig, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetClusterNetworkingConfig")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() {
		cachedCfg, err := utils.FnCacheGet(ctx, c.fnCache, clusterConfigCacheKey{"networking"}, func(ctx context.Context) (types.ClusterNetworkingConfig, error) {
			cfg, err := rg.clusterConfig.GetClusterNetworkingConfig(ctx, opts...)
			return cfg, err
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return cachedCfg.Clone(), nil
	}
	return rg.clusterConfig.GetClusterNetworkingConfig(ctx, opts...)
}

// GetClusterName gets the name of the cluster from the backend.
func (c *Cache) GetClusterName(opts ...services.MarshalOption) (types.ClusterName, error) {
	ctx, span := c.Tracer.Start(context.TODO(), "cache/GetClusterName")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() {
		cachedName, err := utils.FnCacheGet(ctx, c.fnCache, clusterConfigCacheKey{"name"}, func(ctx context.Context) (types.ClusterName, error) {
			cfg, err := rg.clusterConfig.GetClusterName(opts...)
			return cfg, err
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return cachedName.Clone(), nil
	}
	return rg.clusterConfig.GetClusterName(opts...)
}

// GetInstaller gets the installer script resource for the cluster
func (c *Cache) GetInstaller(ctx context.Context, name string) (types.Installer, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetInstaller")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	inst, err := rg.clusterConfig.GetInstaller(ctx, name)
	return inst, trace.Wrap(err)
}

// GetInstallers gets all the installer script resources for the cluster
func (c *Cache) GetInstallers(ctx context.Context) ([]types.Installer, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetInstallers")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	inst, err := rg.clusterConfig.GetInstallers(ctx)
	return inst, trace.Wrap(err)
}

// GetRoles is a part of auth.Cache implementation
func (c *Cache) GetRoles(ctx context.Context) ([]types.Role, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetRoles")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.access.GetRoles(ctx)
}

// GetRole is a part of auth.Cache implementation
func (c *Cache) GetRole(ctx context.Context, name string) (types.Role, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetRole")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	role, err := rg.access.GetRole(ctx, name)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because method is never used
		// in construction of derivative caches.
		if role, err := c.Config.Access.GetRole(ctx, name); err == nil {
			return role, nil
		}
	}
	return role, err
}

// GetNamespace returns namespace
func (c *Cache) GetNamespace(name string) (*types.Namespace, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetNamespace")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetNamespace(name)
}

// GetNamespaces is a part of auth.Cache implementation
func (c *Cache) GetNamespaces() ([]types.Namespace, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetNamespaces")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetNamespaces()
}

// GetNode finds and returns a node by name and namespace.
func (c *Cache) GetNode(ctx context.Context, namespace, name string) (types.Server, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetNode")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetNode(ctx, namespace, name)
}

type getNodesCacheKey struct {
	namespace string
}

var _ map[getNodesCacheKey]struct{} // compile-time hashability check

// GetNodes is a part of auth.Cache implementation
func (c *Cache) GetNodes(ctx context.Context, namespace string) ([]types.Server, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetNodes")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	if !rg.IsCacheRead() {
		cachedNodes, err := c.getNodesWithTTLCache(ctx, rg, namespace)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		nodes := make([]types.Server, 0, len(cachedNodes))
		for _, node := range cachedNodes {
			nodes = append(nodes, node.DeepCopy())
		}
		return nodes, nil
	}

	return rg.presence.GetNodes(ctx, namespace)
}

// getNodesWithTTLCache implements TTL-based caching for the GetNodes endpoint.  All nodes that will be returned from the caching layer
// must be cloned to avoid concurrent modification.
func (c *Cache) getNodesWithTTLCache(ctx context.Context, rg readGuard, namespace string, opts ...services.MarshalOption) ([]types.Server, error) {
	cachedNodes, err := utils.FnCacheGet(ctx, c.fnCache, getNodesCacheKey{namespace}, func(ctx context.Context) ([]types.Server, error) {
		nodes, err := rg.presence.GetNodes(ctx, namespace)
		return nodes, err
	})

	return cachedNodes, trace.Wrap(err)
}

// GetAuthServers returns a list of registered servers
func (c *Cache) GetAuthServers() ([]types.Server, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetAuthServers")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetAuthServers()
}

// GetReverseTunnels is a part of auth.Cache implementation
func (c *Cache) GetReverseTunnels(ctx context.Context, opts ...services.MarshalOption) ([]types.ReverseTunnel, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetReverseTunnels")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetReverseTunnels(ctx, opts...)
}

// GetProxies is a part of auth.Cache implementation
func (c *Cache) GetProxies() ([]types.Server, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetProxies")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetProxies()
}

type remoteClustersCacheKey struct {
	name string
}

var _ map[remoteClustersCacheKey]struct{} // compile-time hashability check

// GetRemoteClusters returns a list of remote clusters
func (c *Cache) GetRemoteClusters(opts ...services.MarshalOption) ([]types.RemoteCluster, error) {
	ctx, span := c.Tracer.Start(context.TODO(), "cache/GetRemoteClusters")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() {
		cachedRemotes, err := utils.FnCacheGet(ctx, c.fnCache, remoteClustersCacheKey{}, func(ctx context.Context) ([]types.RemoteCluster, error) {
			remotes, err := rg.presence.GetRemoteClusters(opts...)
			return remotes, err
		})
		if err != nil || cachedRemotes == nil {
			return nil, trace.Wrap(err)
		}

		remotes := make([]types.RemoteCluster, 0, len(cachedRemotes))
		for _, remote := range cachedRemotes {
			remotes = append(remotes, remote.Clone())
		}
		return remotes, nil
	}
	return rg.presence.GetRemoteClusters(opts...)
}

// GetRemoteCluster returns a remote cluster by name
func (c *Cache) GetRemoteCluster(clusterName string) (types.RemoteCluster, error) {
	ctx, span := c.Tracer.Start(context.TODO(), "cache/GetRemoteCluster")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	if !rg.IsCacheRead() {
		cachedRemote, err := utils.FnCacheGet(ctx, c.fnCache, remoteClustersCacheKey{clusterName}, func(ctx context.Context) (types.RemoteCluster, error) {
			remote, err := rg.presence.GetRemoteCluster(clusterName)
			return remote, err
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}

		return cachedRemote.Clone(), nil
	}
	rc, err := rg.presence.GetRemoteCluster(clusterName)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because this method is never used
		// in construction of derivative caches.
		if rc, err := c.Config.Presence.GetRemoteCluster(clusterName); err == nil {
			return rc, nil
		}
	}
	return rc, trace.Wrap(err)
}

// GetUser is a part of auth.Cache implementation.
func (c *Cache) GetUser(name string, withSecrets bool) (user types.User, err error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetUser")
	defer span.End()

	if withSecrets { // cache never tracks user secrets
		return c.Config.Users.GetUser(name, withSecrets)
	}
	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	user, err = rg.users.GetUser(name, withSecrets)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because method is never used
		// in construction of derivative caches.
		if user, err := c.Config.Users.GetUser(name, withSecrets); err == nil {
			return user, nil
		}
	}
	return user, trace.Wrap(err)
}

// GetUsers is a part of auth.Cache implementation
func (c *Cache) GetUsers(withSecrets bool) (users []types.User, err error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetUsers")
	defer span.End()

	if withSecrets { // cache never tracks user secrets
		return c.Users.GetUsers(withSecrets)
	}
	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.users.GetUsers(withSecrets)
}

// GetTunnelConnections is a part of auth.Cache implementation
func (c *Cache) GetTunnelConnections(clusterName string, opts ...services.MarshalOption) ([]types.TunnelConnection, error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetTunnelConnections")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetTunnelConnections(clusterName, opts...)
}

// GetAllTunnelConnections is a part of auth.Cache implementation
func (c *Cache) GetAllTunnelConnections(opts ...services.MarshalOption) (conns []types.TunnelConnection, err error) {
	_, span := c.Tracer.Start(context.TODO(), "cache/GetAllTunnelConnections")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetAllTunnelConnections(opts...)
}

// GetKubeServices is a part of auth.Cache implementation
//
// DELETE IN 12.0.0 Deprecated, use GetKubernetesServers.
func (c *Cache) GetKubeServices(ctx context.Context) ([]types.Server, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetKubeServices")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetKubeServices(ctx)
}

// GetKubernetesServers is a part of auth.Cache implementation
func (c *Cache) GetKubernetesServers(ctx context.Context) ([]types.KubeServer, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetKubernetesServers")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetKubernetesServers(ctx)
}

// GetApplicationServers returns all registered application servers.
func (c *Cache) GetApplicationServers(ctx context.Context, namespace string) ([]types.AppServer, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetApplicationServers")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetApplicationServers(ctx, namespace)
}

// GetKubernetesClusters returns all kubernetes cluster resources.
func (c *Cache) GetKubernetesClusters(ctx context.Context) ([]types.KubeCluster, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetKubernetesClusters")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.kubernetes.GetKubernetesClusters(ctx)
}

// GetKubernetesCluster returns the specified kubernetes cluster resource.
func (c *Cache) GetKubernetesCluster(ctx context.Context, name string) (types.KubeCluster, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetKubernetesCluster")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.kubernetes.GetKubernetesCluster(ctx, name)
}

// GetApps returns all application resources.
func (c *Cache) GetApps(ctx context.Context) ([]types.Application, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetApps")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.apps.GetApps(ctx)
}

// GetApp returns the specified application resource.
func (c *Cache) GetApp(ctx context.Context, name string) (types.Application, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetApp")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.apps.GetApp(ctx, name)
}

// GetAppSession gets an application web session.
func (c *Cache) GetAppSession(ctx context.Context, req types.GetAppSessionRequest) (types.WebSession, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetAppSession")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.appSession.GetAppSession(ctx, req)
}

// GetSnowflakeSession gets Snowflake web session.
func (c *Cache) GetSnowflakeSession(ctx context.Context, req types.GetSnowflakeSessionRequest) (types.WebSession, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetSnowflakeSession")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.snowflakeSession.GetSnowflakeSession(ctx, req)
}

// GetDatabaseServers returns all registered database proxy servers.
func (c *Cache) GetDatabaseServers(ctx context.Context, namespace string, opts ...services.MarshalOption) ([]types.DatabaseServer, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetDatabaseServers")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetDatabaseServers(ctx, namespace, opts...)
}

// GetDatabases returns all database resources.
func (c *Cache) GetDatabases(ctx context.Context) ([]types.Database, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetDatabases")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.databases.GetDatabases(ctx)
}

// GetDatabase returns the specified database resource.
func (c *Cache) GetDatabase(ctx context.Context, name string) (types.Database, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetDatabase")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.databases.GetDatabase(ctx, name)
}

// GetWebSession gets a regular web session.
func (c *Cache) GetWebSession(ctx context.Context, req types.GetWebSessionRequest) (types.WebSession, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetWebSession")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.webSession.Get(ctx, req)
}

// GetWebToken gets a web token.
func (c *Cache) GetWebToken(ctx context.Context, req types.GetWebTokenRequest) (types.WebToken, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetWebToken")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.webToken.Get(ctx, req)
}

// GetAuthPreference gets the cluster authentication config.
func (c *Cache) GetAuthPreference(ctx context.Context) (types.AuthPreference, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetAuthPreference")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.clusterConfig.GetAuthPreference(ctx)
}

// GetSessionRecordingConfig gets session recording configuration.
func (c *Cache) GetSessionRecordingConfig(ctx context.Context, opts ...services.MarshalOption) (types.SessionRecordingConfig, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetSessionRecordingConfig")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.clusterConfig.GetSessionRecordingConfig(ctx, opts...)
}

// GetNetworkRestrictions gets the network restrictions.
func (c *Cache) GetNetworkRestrictions(ctx context.Context) (types.NetworkRestrictions, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetNetworkRestrictions")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	return rg.restrictions.GetNetworkRestrictions(ctx)
}

// GetLock gets a lock by name.
func (c *Cache) GetLock(ctx context.Context, name string) (types.Lock, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetLock")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	lock, err := rg.access.GetLock(ctx, name)
	if trace.IsNotFound(err) && rg.IsCacheRead() {
		// release read lock early
		rg.Release()
		// fallback is sane because method is never used
		// in construction of derivative caches.
		if lock, err := c.Config.Access.GetLock(ctx, name); err == nil {
			return lock, nil
		}
	}
	return lock, trace.Wrap(err)
}

// GetLocks gets all/in-force locks that match at least one of the targets
// when specified.
func (c *Cache) GetLocks(ctx context.Context, inForceOnly bool, targets ...types.LockTarget) ([]types.Lock, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetLocks")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.access.GetLocks(ctx, inForceOnly, targets...)
}

// GetWindowsDesktopServices returns all registered Windows desktop services.
func (c *Cache) GetWindowsDesktopServices(ctx context.Context) ([]types.WindowsDesktopService, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetWindowsDesktopServices")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetWindowsDesktopServices(ctx)
}

// GetWindowsDesktopService returns a registered Windows desktop service by name.
func (c *Cache) GetWindowsDesktopService(ctx context.Context, name string) (types.WindowsDesktopService, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetWindowsDesktopService")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.presence.GetWindowsDesktopService(ctx, name)
}

// GetWindowsDesktops returns all registered Windows desktop hosts.
func (c *Cache) GetWindowsDesktops(ctx context.Context, filter types.WindowsDesktopFilter) ([]types.WindowsDesktop, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/GetWindowsDesktops")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.windowsDesktops.GetWindowsDesktops(ctx, filter)
}

// ListWindowsDesktops returns all registered Windows desktop hosts.
func (c *Cache) ListWindowsDesktops(ctx context.Context, req types.ListWindowsDesktopsRequest) (*types.ListWindowsDesktopsResponse, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/ListWindowsDesktops")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.windowsDesktops.ListWindowsDesktops(ctx, req)
}

// ListWindowsDesktopServices returns all registered Windows desktop hosts.
func (c *Cache) ListWindowsDesktopServices(ctx context.Context, req types.ListWindowsDesktopServicesRequest) (*types.ListWindowsDesktopServicesResponse, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/ListWindowsDesktopServices")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()
	return rg.windowsDesktops.ListWindowsDesktopServices(ctx, req)
}

// ListResources is a part of auth.Cache implementation
func (c *Cache) ListResources(ctx context.Context, req proto.ListResourcesRequest) (*types.ListResourcesResponse, error) {
	ctx, span := c.Tracer.Start(ctx, "cache/ListResources")
	defer span.End()

	rg, err := c.read()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer rg.Release()

	// Cache is not healthy, but right now, only `Node` kind has an
	// implementation that falls back to TTL cache.
	if !rg.IsCacheRead() && req.ResourceType == types.KindNode {
		return c.listResourcesFromTTLCache(ctx, rg, req)
	}

	return rg.presence.ListResources(ctx, req)
}

// listResourcesFromTTLCache used when the cache is not healthy. It takes advantage
// of TTL-based caching rather than caching individual page calls (very messy).
// It relies on caching the result of the `GetXXXs` endpoint and then "faking"
// pagination.
//
// Since TTLCaching falls back to retrieving all resources upfront, we also support
// sorting.
//
// NOTE: currently only types.KindNode supports TTL caching.
func (c *Cache) listResourcesFromTTLCache(ctx context.Context, rg readGuard, req proto.ListResourcesRequest) (*types.ListResourcesResponse, error) {
	var resources []types.ResourceWithLabels
	switch req.ResourceType {
	case types.KindNode:
		// Retrieve all nodes.
		cachedNodes, err := c.getNodesWithTTLCache(ctx, rg, req.Namespace)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		// Nodes returned from the TTL caching layer
		// must be cloned to avoid concurrent modification.
		clonedNodes := make([]types.Server, 0, len(cachedNodes))
		for _, node := range cachedNodes {
			clonedNodes = append(clonedNodes, node.DeepCopy())
		}

		servers := types.Servers(clonedNodes)
		if err := servers.SortByCustom(req.SortBy); err != nil {
			return nil, trace.Wrap(err)
		}

		resources = servers.AsResources()

	default:
		return nil, trace.NotImplemented("resource type %q does not support TTL caching", req.ResourceType)
	}

	return local.FakePaginate(resources, req)
}
