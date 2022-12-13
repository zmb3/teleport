/*
Copyright 2015-2019 Gravitational, Inc.

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

// Package auth implements certificate signing authority and access control server
// Authority server is composed of several parts:
//
// * Authority server itself that implements signing and acl logic
// * HTTP server wrapper for authority server
// * HTTP client wrapper
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"math/big"
	insecurerand "math/rand"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/jose"
	"github.com/coreos/go-oidc/oauth2"
	"github.com/coreos/go-oidc/oidc"
	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus"
	saml2 "github.com/russellhaering/gosaml2"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/constants"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/api/types/wrappers"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/api/utils/retryutils"
	"github.com/zmb3/teleport/lib/auth/keystore"
	"github.com/zmb3/teleport/lib/auth/native"
	wanlib "github.com/zmb3/teleport/lib/auth/webauthn"
	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/circleci"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/githubactions"
	"github.com/zmb3/teleport/lib/inventory"
	kubeutils "github.com/zmb3/teleport/lib/kube/utils"
	"github.com/zmb3/teleport/lib/limiter"
	"github.com/zmb3/teleport/lib/modules"
	"github.com/zmb3/teleport/lib/observability/metrics"
	"github.com/zmb3/teleport/lib/observability/tracing"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/services/local"
	"github.com/zmb3/teleport/lib/session"
	"github.com/zmb3/teleport/lib/srv/db/common/role"
	"github.com/zmb3/teleport/lib/sshca"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/interval"
	vc "github.com/zmb3/teleport/lib/versioncontrol"
	"github.com/zmb3/teleport/lib/versioncontrol/github"
)

const (
	ErrFieldKeyUserMaxedAttempts = "maxed-attempts"

	// MaxFailedAttemptsErrMsg is a user friendly error message that tells a user that they are locked.
	MaxFailedAttemptsErrMsg = "too many incorrect attempts, please try again later"
)

const (
	// githubCacheTimeout is how long Github org entries are cached.
	githubCacheTimeout = time.Hour
)

var ErrRequiresEnterprise = services.ErrRequiresEnterprise

// ServerOption allows setting options as functional arguments to Server
type ServerOption func(*Server) error

// NewServer creates and configures a new Server instance
func NewServer(cfg *InitConfig, opts ...ServerOption) (*Server, error) {
	err := metrics.RegisterPrometheusCollectors(prometheusCollectors...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if cfg.Trust == nil {
		cfg.Trust = local.NewCAService(cfg.Backend)
	}
	if cfg.Presence == nil {
		cfg.Presence = local.NewPresenceService(cfg.Backend)
	}
	if cfg.Provisioner == nil {
		cfg.Provisioner = local.NewProvisioningService(cfg.Backend)
	}
	if cfg.Identity == nil {
		cfg.Identity = local.NewIdentityService(cfg.Backend)
	}
	if cfg.Access == nil {
		cfg.Access = local.NewAccessService(cfg.Backend)
	}
	if cfg.DynamicAccessExt == nil {
		cfg.DynamicAccessExt = local.NewDynamicAccessService(cfg.Backend)
	}
	if cfg.ClusterConfiguration == nil {
		clusterConfig, err := local.NewClusterConfigurationService(cfg.Backend)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		cfg.ClusterConfiguration = clusterConfig
	}
	if cfg.Restrictions == nil {
		cfg.Restrictions = local.NewRestrictionsService(cfg.Backend)
	}
	if cfg.Apps == nil {
		cfg.Apps = local.NewAppService(cfg.Backend)
	}
	if cfg.Databases == nil {
		cfg.Databases = local.NewDatabasesService(cfg.Backend)
	}
	if cfg.Kubernetes == nil {
		cfg.Kubernetes = local.NewKubernetesService(cfg.Backend)
	}
	if cfg.Status == nil {
		cfg.Status = local.NewStatusService(cfg.Backend)
	}
	if cfg.Events == nil {
		cfg.Events = local.NewEventsService(cfg.Backend)
	}
	if cfg.AuditLog == nil {
		cfg.AuditLog = events.NewDiscardAuditLog()
	}
	if cfg.Emitter == nil {
		cfg.Emitter = events.NewDiscardEmitter()
	}
	if cfg.Streamer == nil {
		cfg.Streamer = events.NewDiscardEmitter()
	}
	if cfg.WindowsDesktops == nil {
		cfg.WindowsDesktops = local.NewWindowsDesktopService(cfg.Backend)
	}
	if cfg.ConnectionsDiagnostic == nil {
		cfg.ConnectionsDiagnostic = local.NewConnectionsDiagnosticService(cfg.Backend)
	}
	if cfg.SessionTrackerService == nil {
		cfg.SessionTrackerService, err = local.NewSessionTrackerService(cfg.Backend)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	if cfg.Enforcer == nil {
		cfg.Enforcer = local.NewNoopEnforcer()
	}
	if cfg.AssertionReplayService == nil {
		cfg.AssertionReplayService = local.NewAssertionReplayService(cfg.Backend)
	}
	if cfg.TraceClient == nil {
		cfg.TraceClient = tracing.NewNoopClient()
	}
	if cfg.UsageReporter == nil {
		cfg.UsageReporter = local.NewDiscardUsageReporter()
	}

	limiter, err := limiter.NewConnectionsLimiter(limiter.Config{
		MaxConnections: defaults.LimiterMaxConcurrentSignatures,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if cfg.KeyStoreConfig.PKCS11 != (keystore.PKCS11Config{}) {
		if !modules.GetModules().Features().HSM {
			return nil, fmt.Errorf("PKCS11 HSM support requires a license with the HSM feature enabled: %w", ErrRequiresEnterprise)
		}
		cfg.KeyStoreConfig.PKCS11.HostUUID = cfg.HostUUID
	} else if cfg.KeyStoreConfig.GCPKMS != (keystore.GCPKMSConfig{}) {
		if !modules.GetModules().Features().HSM {
			return nil, fmt.Errorf("Google Cloud KMS support requires a license with the HSM feature enabled: %w", ErrRequiresEnterprise)
		}
		cfg.KeyStoreConfig.GCPKMS.HostUUID = cfg.HostUUID
	} else {
		native.PrecomputeKeys()
		cfg.KeyStoreConfig.Software.RSAKeyPairSource = native.GenerateKeyPair
	}
	cfg.KeyStoreConfig.Logger = log
	keyStore, err := keystore.NewManager(context.Background(), cfg.KeyStoreConfig)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	services := &Services{
		Trust:                 cfg.Trust,
		Presence:              cfg.Presence,
		Provisioner:           cfg.Provisioner,
		Identity:              cfg.Identity,
		Access:                cfg.Access,
		DynamicAccessExt:      cfg.DynamicAccessExt,
		ClusterConfiguration:  cfg.ClusterConfiguration,
		Restrictions:          cfg.Restrictions,
		Apps:                  cfg.Apps,
		Kubernetes:            cfg.Kubernetes,
		Databases:             cfg.Databases,
		IAuditLog:             cfg.AuditLog,
		Events:                cfg.Events,
		WindowsDesktops:       cfg.WindowsDesktops,
		SessionTrackerService: cfg.SessionTrackerService,
		Enforcer:              cfg.Enforcer,
		ConnectionsDiagnostic: cfg.ConnectionsDiagnostic,
		StatusInternal:        cfg.Status,
		UsageReporter:         cfg.UsageReporter,
	}

	closeCtx, cancelFunc := context.WithCancel(context.TODO())
	as := Server{
		bk:              cfg.Backend,
		limiter:         limiter,
		Authority:       cfg.Authority,
		AuthServiceName: cfg.AuthServiceName,
		ServerID:        cfg.HostUUID,
		oidcClients:     make(map[string]*oidcClient),
		samlProviders:   make(map[string]*samlProvider),
		githubClients:   make(map[string]*githubClient),
		cancelFunc:      cancelFunc,
		closeCtx:        closeCtx,
		emitter:         cfg.Emitter,
		streamer:        cfg.Streamer,
		unstable:        local.NewUnstableService(cfg.Backend, cfg.AssertionReplayService),
		Services:        services,
		Cache:           services,
		keyStore:        keyStore,
		getClaimsFun:    getClaims,
		inventory:       inventory.NewController(cfg.Presence),
		traceClient:     cfg.TraceClient,
		fips:            cfg.FIPS,
		loadAllCAs:      cfg.LoadAllCAs,
	}
	for _, o := range opts {
		if err := o(&as); err != nil {
			return nil, trace.Wrap(err)
		}
	}
	if as.clock == nil {
		as.clock = clockwork.NewRealClock()
	}
	as.githubOrgSSOCache, err = utils.NewFnCache(utils.FnCacheConfig{
		TTL: githubCacheTimeout,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if as.ghaIDTokenValidator == nil {
		as.ghaIDTokenValidator = githubactions.NewIDTokenValidator(
			githubactions.IDTokenValidatorConfig{
				Clock: as.clock,
			},
		)
	}
	if as.circleCITokenValidate == nil {
		as.circleCITokenValidate = func(
			ctx context.Context, organizationID, token string,
		) (*circleci.IDTokenClaims, error) {
			return circleci.ValidateToken(
				ctx, as.clock, circleci.IssuerURLTemplate, organizationID, token,
			)
		}
	}

	return &as, nil
}

type Services struct {
	services.Trust
	services.Presence
	services.Provisioner
	services.Identity
	services.Access
	services.DynamicAccessExt
	services.ClusterConfiguration
	services.Restrictions
	services.Apps
	services.Kubernetes
	services.Databases
	services.WindowsDesktops
	services.SessionTrackerService
	services.Enforcer
	services.ConnectionsDiagnostic
	services.StatusInternal
	services.UsageReporter
	types.Events
	events.IAuditLog
}

// GetWebSession returns existing web session described by req.
// Implements ReadAccessPoint
func (r *Services) GetWebSession(ctx context.Context, req types.GetWebSessionRequest) (types.WebSession, error) {
	return r.Identity.WebSessions().Get(ctx, req)
}

// GetWebToken returns existing web token described by req.
// Implements ReadAccessPoint
func (r *Services) GetWebToken(ctx context.Context, req types.GetWebTokenRequest) (types.WebToken, error) {
	return r.Identity.WebTokens().Get(ctx, req)
}

var (
	generateRequestsCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: teleport.MetricGenerateRequests,
			Help: "Number of requests to generate new server keys",
		},
	)
	generateThrottledRequestsCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: teleport.MetricGenerateRequestsThrottled,
			Help: "Number of throttled requests to generate new server keys",
		},
	)
	generateRequestsCurrent = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: teleport.MetricGenerateRequestsCurrent,
			Help: "Number of current generate requests for server keys",
		},
	)
	generateRequestsLatencies = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name: teleport.MetricGenerateRequestsHistogram,
			Help: "Latency for generate requests for server keys",
			// lowest bucket start of upper bound 0.001 sec (1 ms) with factor 2
			// highest bucket start of 0.001 sec * 2^15 == 32.768 sec
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
		},
	)
	// UserLoginCount counts user logins
	UserLoginCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: teleport.MetricUserLoginCount,
			Help: "Number of times there was a user login",
		},
	)

	heartbeatsMissedByAuth = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: teleport.MetricHeartbeatsMissed,
			Help: "Number of heartbeats missed by auth server",
		},
	)

	registeredAgents = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: teleport.MetricNamespace,
			Name:      teleport.MetricRegisteredServers,
			Help:      "The number of Teleport services that are connected to an auth server by version.",
		},
		[]string{teleport.TagVersion},
	)

	prometheusCollectors = []prometheus.Collector{
		generateRequestsCount, generateThrottledRequestsCount,
		generateRequestsCurrent, generateRequestsLatencies, UserLoginCount, heartbeatsMissedByAuth,
		registeredAgents,
	}
)

// Server keeps the cluster together. It acts as a certificate authority (CA) for
// a cluster and:
//   - generates the keypair for the node it's running on
//   - invites other SSH nodes to a cluster, by issuing invite tokens
//   - adds other SSH nodes to a cluster, by checking their token and signing their keys
//   - same for users and their sessions
//   - checks public keys to see if they're signed by it (can be trusted or not)
type Server struct {
	lock sync.RWMutex
	// oidcClients is a map from authID & proxyAddr -> oidcClient
	oidcClients   map[string]*oidcClient
	samlProviders map[string]*samlProvider
	githubClients map[string]*githubClient
	clock         clockwork.Clock
	bk            backend.Backend

	closeCtx   context.Context
	cancelFunc context.CancelFunc

	sshca.Authority

	// AuthServiceName is a human-readable name of this CA. If several Auth services are running
	// (managing multiple teleport clusters) this field is used to tell them apart in UIs
	// It usually defaults to the hostname of the machine the Auth service runs on.
	AuthServiceName string

	// ServerID is the server ID of this auth server.
	ServerID string

	// unstable implements unstable backend methods not suitable
	// for inclusion in Services.
	unstable local.UnstableService

	// Services encapsulate services - provisioner, trust, etc. used by the auth
	// server in a separate structure. Reads through Services hit the backend.
	*Services

	// Cache should either be the same as Services, or a caching layer over it.
	// As it's an interface (and thus directly implementing all of its methods)
	// its embedding takes priority over Services (which only indirectly
	// implements its methods), thus any implemented GetFoo method on both Cache
	// and Services will call the one from Cache. To bypass the cache, call the
	// method on Services instead.
	Cache

	// privateKey is used in tests to use pre-generated private keys
	privateKey []byte

	// cipherSuites is a list of ciphersuites that the auth server supports.
	cipherSuites []uint16

	// limiter limits the number of active connections per client IP.
	limiter *limiter.ConnectionsLimiter

	// Emitter is events emitter, used to submit discrete events
	emitter apievents.Emitter

	// streamer is events sessionstreamer, used to create continuous
	// session related streams
	streamer events.Streamer

	// keyStore manages all CA private keys, which  may or may not be backed by
	// HSMs
	keyStore *keystore.Manager

	// lockWatcher is a lock watcher, used to verify cert generation requests.
	lockWatcher *services.LockWatcher

	// getClaimsFun is used in tests for overriding the implementation of getClaims method used in OIDC.
	getClaimsFun func(closeCtx context.Context, oidcClient *oidc.Client, connector types.OIDCConnector, code string) (jose.Claims, error)

	inventory *inventory.Controller

	// githubOrgSSOCache is used to cache whether Github organizations use
	// external SSO or not.
	githubOrgSSOCache *utils.FnCache

	// traceClient is used to forward spans to the upstream collector for components
	// within the cluster that don't have a direct connection to said collector
	traceClient otlptrace.Client

	// fips means FedRAMP/FIPS 140-2 compliant configuration was requested.
	fips bool

	// ghaIDTokenValidator allows ID tokens from GitHub Actions to be validated
	// by the auth server. It can be overridden for the purpose of tests.
	ghaIDTokenValidator ghaIDTokenValidator

	// circleCITokenValidate allows ID tokens from CircleCI to be validated by
	// the auth server. It can be overridden for the purpose of tests.
	circleCITokenValidate func(ctx context.Context, organizationID, token string) (*circleci.IDTokenClaims, error)

	// loadAllCAs tells tsh to load the host CAs for all clusters when trying to ssh into a node.
	loadAllCAs bool
}

func (a *Server) CloseContext() context.Context {
	return a.closeCtx
}

// SetLockWatcher sets the lock watcher.
func (a *Server) SetLockWatcher(lockWatcher *services.LockWatcher) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.lockWatcher = lockWatcher
}

func (a *Server) checkLockInForce(mode constants.LockingMode, targets []types.LockTarget) error {
	a.lock.RLock()
	defer a.lock.RUnlock()
	if a.lockWatcher == nil {
		return trace.BadParameter("lockWatcher is not set")
	}
	return a.lockWatcher.CheckLockInForce(mode, targets...)
}

// runPeriodicOperations runs some periodic bookkeeping operations
// performed by auth server
func (a *Server) runPeriodicOperations() {
	ctx := context.TODO()
	// run periodic functions with a semi-random period
	// to avoid contention on the database in case if there are multiple
	// auth servers running - so they don't compete trying
	// to update the same resources.
	r := insecurerand.New(insecurerand.NewSource(a.GetClock().Now().UnixNano()))
	period := defaults.HighResPollingPeriod + time.Duration(r.Intn(int(defaults.HighResPollingPeriod/time.Second)))*time.Second
	log.Debugf("Ticking with period: %v.", period)
	a.lock.RLock()
	ticker := a.clock.NewTicker(period)
	a.lock.RUnlock()
	// Create a ticker with jitter
	heartbeatCheckTicker := interval.New(interval.Config{
		Duration: apidefaults.ServerKeepAliveTTL() * 2,
		Jitter:   retryutils.NewSeventhJitter(),
	})
	promTicker := interval.New(interval.Config{
		Duration: defaults.PrometheusScrapeInterval,
		Jitter:   retryutils.NewSeventhJitter(),
	})
	missedKeepAliveCount := 0
	defer ticker.Stop()
	defer heartbeatCheckTicker.Stop()
	defer promTicker.Stop()

	firstReleaseCheck := utils.FullJitter(time.Hour * 6)

	// this environment variable is "unstable" since it will be deprecated
	// by an upcoming tctl command. currently exists for testing purposes only.
	if os.Getenv("TELEPORT_UNSTABLE_VC_SYNC_ON_START") == "yes" {
		firstReleaseCheck = utils.HalfJitter(time.Second * 10)
	}

	// note the use of FullJitter for the releases check interval. this lets us ensure
	// that frequent restarts don't prevent checks from happening despite the infrequent
	// effective check rate.
	releaseCheck := interval.New(interval.Config{
		Duration:      time.Hour * 24,
		FirstDuration: firstReleaseCheck,
		Jitter:        retryutils.NewFullJitter(),
	})

	// more frequent release check that just re-calculates alerts based on previously
	// pulled versioning info.
	localReleaseCheck := interval.New(interval.Config{
		Duration:      time.Minute * 10,
		FirstDuration: utils.HalfJitter(time.Second * 10),
		Jitter:        retryutils.NewHalfJitter(),
	})
	defer releaseCheck.Stop()
	for {
		select {
		case <-a.closeCtx.Done():
			return
		case <-ticker.Chan():
			err := a.autoRotateCertAuthorities(ctx)
			if err != nil {
				if trace.IsCompareFailed(err) {
					log.Debugf("Cert authority has been updated concurrently: %v.", err)
				} else {
					log.Errorf("Failed to perform cert rotation check: %v.", err)
				}
			}
		case <-heartbeatCheckTicker.Next():
			nodes, err := a.GetNodes(ctx, apidefaults.Namespace)
			if err != nil {
				log.Errorf("Failed to load nodes for heartbeat metric calculation: %v", err)
			}
			for _, node := range nodes {
				if services.NodeHasMissedKeepAlives(node) {
					missedKeepAliveCount++
				}
			}
			// Update prometheus gauge
			heartbeatsMissedByAuth.Set(float64(missedKeepAliveCount))
		case <-promTicker.Next():
			a.updateVersionMetrics()
		case <-releaseCheck.Next():
			a.syncReleaseAlerts(ctx, true)
		case <-localReleaseCheck.Next():
			a.syncReleaseAlerts(ctx, false)
		}
	}
}

const (
	releaseAlertID = "upgrade-suggestion"
	secAlertID     = "security-patch-available"
	verInUseLabel  = "teleport.internal/ver-in-use"
)

// syncReleaseAlerts calculates alerts related to new teleport releases. When checkRemote
// is true it pulls the latest release info from github.  Otherwise, it loads the versions used
// for the most recent alerts and re-syncs with latest cluster state.
func (a *Server) syncReleaseAlerts(ctx context.Context, checkRemote bool) {
	log.Debug("Checking for new teleport releases via github api.")

	// NOTE: essentially everything in this function is going to be
	// scrapped/replaced once the inventory and version-control systems
	// are a bit further along.

	current := vc.NewTarget(vc.Normalize(teleport.Version))

	// this environment variable is "unstable" since it will be deprecated
	// by an upcoming tctl command. currently exists for testing purposes only.
	if t := vc.NewTarget(os.Getenv("TELEPORT_UNSTABLE_VC_VERSION")); t.Ok() {
		current = t
	}

	visitor := vc.Visitor{
		Current: current,
	}

	// users cannot upgrade their own auth instances in cloud, so it isn't helpful
	// to generate alerts for releases newer than the current auth server version.
	if modules.GetModules().Features().Cloud {
		visitor.NotNewerThan = current
	}

	var loadFailed bool

	if checkRemote {
		// scrape the github releases API with our visitor
		if err := github.Visit(&visitor); err != nil {
			log.Warnf("Failed to load github releases: %v (this will not impact teleport functionality)", err)
			loadFailed = true
		}
	} else {
		if err := a.visitCachedAlertVersions(ctx, &visitor); err != nil {
			log.Warnf("Failed to load release alert into: %v (this will not impact teleport functionality)", err)
			loadFailed = true
		}
	}

	a.doReleaseAlertSync(ctx, current, visitor, !loadFailed)
}

// visitCachedAlertVersions updates the visitor with targets reconstructed from the metadata
// of existing alerts. This lets us "reevaluate" the alerts based on newer cluster state without
// re-pulling the releases page. Future version of teleport will cache actual full release
// descriptions, rending this unnecessary.
func (a *Server) visitCachedAlertVersions(ctx context.Context, visitor *vc.Visitor) error {
	// reconstruct the target for the "latest stable" alert if it exists.
	alert, err := a.getClusterAlert(ctx, releaseAlertID)
	if err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	if err == nil {
		if t := vc.NewTarget(alert.Metadata.Labels[verInUseLabel]); t.Ok() {
			visitor.Visit(t)
		}
	}

	// reconstruct the target for the "latest sec patch" alert if it exists.
	alert, err = a.getClusterAlert(ctx, secAlertID)
	if err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	if err == nil {
		if t := vc.NewTarget(alert.Metadata.Labels[verInUseLabel], vc.SecurityPatch(true)); t.Ok() {
			visitor.Visit(t)
		}
	}
	return nil
}

func (a *Server) getClusterAlert(ctx context.Context, id string) (types.ClusterAlert, error) {
	alerts, err := a.GetClusterAlerts(ctx, types.GetClusterAlertsRequest{
		AlertID: id,
	})
	if err != nil {
		return types.ClusterAlert{}, trace.Wrap(err)
	}
	if len(alerts) == 0 {
		return types.ClusterAlert{}, trace.NotFound("cluster alert %q not found", id)
	}
	return alerts[0], nil
}

func (a *Server) doReleaseAlertSync(ctx context.Context, current vc.Target, visitor vc.Visitor, cleanup bool) {
	const alertTTL = time.Minute * 30
	// use visitor to find the oldest version among connected instances.
	// TODO(fspmarshall): replace this check as soon as we have a backend inventory repr. using
	// connected instances is a poor approximation and may lead to missed notifications if auth
	// server is up to date, but instances not connected to this auth need update.
	var instanceVisitor vc.Visitor
	a.inventory.Iter(func(handle inventory.UpstreamHandle) {
		v := vc.Normalize(handle.Hello().Version)
		instanceVisitor.Visit(vc.NewTarget(v))
	})

	// build the general alert msg meant for broader consumption
	msg, verInUse := makeUpgradeSuggestionMsg(visitor, current, instanceVisitor.Oldest())

	if msg != "" {
		alert, err := types.NewClusterAlert(
			releaseAlertID,
			msg,
			// Defaulting to "low" severity level. We may want to make this dynamic
			// in the future depending on the distance from up-to-date.
			types.WithAlertSeverity(types.AlertSeverity_LOW),
			types.WithAlertLabel(types.AlertOnLogin, "yes"),
			types.WithAlertLabel(types.AlertPermitAll, "yes"),
			types.WithAlertLabel(verInUseLabel, verInUse),
			types.WithAlertExpires(a.clock.Now().Add(alertTTL)),
		)
		if err != nil {
			log.Warnf("Failed to build %s alert: %v (this is a bug)", releaseAlertID, err)
			return
		}
		if err := a.UpsertClusterAlert(ctx, alert); err != nil {
			log.Warnf("Failed to set %s alert: %v", releaseAlertID, err)
			return
		}
	} else if cleanup {
		log.Debugf("Cluster appears up to date, clearing %s alert.", releaseAlertID)
		err := a.DeleteClusterAlert(ctx, releaseAlertID)
		if err != nil && !trace.IsNotFound(err) {
			log.Warnf("Failed to delete %s alert: %v", releaseAlertID, err)
		}
	}

	if sp := visitor.NewestSecurityPatch(); sp.Ok() && sp.NewerThan(current) {
		// explicit security patch alerts have a more limited audience, so we generate
		// them as their own separate alert.
		log.Warnf("A newer security patch has been detected. current=%s, patch=%s", current.Version(), sp.Version())
		secMsg := fmt.Sprintf("A security patch is available for Teleport. Please upgrade your Cluster to %s or newer.", sp.Version())

		alert, err := types.NewClusterAlert(
			secAlertID,
			secMsg,
			types.WithAlertLabel(types.AlertOnLogin, "yes"),
			// TODO(fspmarshall): permit alert to be shown to those with inventory management
			// permissions once we have RBAC around that. For now, token:write is a decent
			// approximation and will ensure that alerts are shown to the editor role.
			types.WithAlertLabel(types.AlertVerbPermit, fmt.Sprintf("%s:%s", types.KindToken, types.VerbCreate)),
			// hide the normal upgrade alert for users who can see this alert in order to
			// improve its visibility and reduce clutter.
			types.WithAlertLabel(types.AlertSupersedes, releaseAlertID),
			types.WithAlertSeverity(types.AlertSeverity_HIGH),
			types.WithAlertLabel(verInUseLabel, sp.Version()),
			types.WithAlertExpires(a.clock.Now().Add(alertTTL)),
		)
		if err != nil {
			log.Warnf("Failed to build %s alert: %v (this is a bug)", secAlertID, err)
			return
		}

		if err := a.UpsertClusterAlert(ctx, alert); err != nil {
			log.Warnf("Failed to set %s alert: %v", secAlertID, err)
			return
		}
	} else if cleanup {
		err := a.DeleteClusterAlert(ctx, secAlertID)
		if err != nil && !trace.IsNotFound(err) {
			log.Warnf("Failed to delete %s alert: %v", secAlertID, err)
		}
	}
}

// makeUpgradeSuggestionMsg generates an upgrade suggestion alert msg if one is
// needed (returns "" if everything looks up to date).
func makeUpgradeSuggestionMsg(visitor vc.Visitor, current, oldestInstance vc.Target) (msg string, ver string) {
	if next := visitor.NextMajor(); next.Ok() {
		// at least one stable release exists for the next major version
		log.Debugf("Generating alert msg for next major version. current=%s, next=%s", current.Version(), next.Version())
		return fmt.Sprintf("The next major version of Teleport is %s. Please consider upgrading your Cluster.", next.Major()), next.Version()
	}

	if nc := visitor.NewestCurrent(); nc.Ok() && nc.NewerThan(current) {
		// newer release of the currently running major version is available
		log.Debugf("Generating alert msg for new minor or patch release. current=%s, newest=%s", current.Version(), nc.Version())
		return fmt.Sprintf("Teleport %s is now available, please consider upgrading your Cluster.", nc.Version()), nc.Version()
	}

	if oldestInstance.Ok() && current.NewerThan(oldestInstance) {
		// at least one connected instance is older than this auth server
		return "Some Agents within this Cluster are running an older version of Teleport.  Please consider upgrading them.", current.Version()
	}

	return "", ""
}

// updateVersionMetrics leverages the inventory control stream to report the versions of
// all instances that are connected to a single auth server via prometheus metrics. To
// get an accurate representation of versions in an entire cluster the metric must be aggregated
// with all auth instances.
func (a *Server) updateVersionMetrics() {
	versionCount := make(map[string]int)

	// record versions for all connected resources
	a.inventory.Iter(func(handle inventory.UpstreamHandle) {
		versionCount[handle.Hello().Version]++
	})

	// record version for **THIS** auth server
	versionCount[teleport.Version]++

	// reset the gauges so that any versions that fall off are removed from exported metrics
	registeredAgents.Reset()
	for version, count := range versionCount {
		registeredAgents.WithLabelValues(version).Set(float64(count))
	}
}

func (a *Server) Close() error {
	a.cancelFunc()

	var errs []error

	if err := a.inventory.Close(); err != nil {
		errs = append(errs, err)
	}

	if a.bk != nil {
		if err := a.bk.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return trace.NewAggregate(errs...)
}

func (a *Server) GetClock() clockwork.Clock {
	a.lock.RLock()
	defer a.lock.RUnlock()
	return a.clock
}

// SetClock sets clock, used in tests
func (a *Server) SetClock(clock clockwork.Clock) {
	a.lock.Lock()
	defer a.lock.Unlock()
	a.clock = clock
}

// SetAuditLog sets the server's audit log
func (a *Server) SetAuditLog(auditLog events.IAuditLog) {
	a.Services.IAuditLog = auditLog
}

func (a *Server) SetEmitter(emitter apievents.Emitter) {
	a.emitter = emitter
}

// SetEnforcer sets the server's enforce service
func (a *Server) SetEnforcer(enforcer services.Enforcer) {
	a.Services.Enforcer = enforcer
}

// SetUsageReporter sets the server's usage reporter
func (a *Server) SetUsageReporter(reporter services.UsageReporter) {
	a.Services.UsageReporter = reporter
}

// GetDomainName returns the domain name that identifies this authority server.
// Also known as "cluster name"
func (a *Server) GetDomainName() (string, error) {
	clusterName, err := a.GetClusterName()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return clusterName.GetClusterName(), nil
}

// GetClusterCACert returns the PEM-encoded TLS certs for the local cluster. If
// the cluster has multiple TLS certs, they will all be concatenated.
func (a *Server) GetClusterCACert(ctx context.Context) (*proto.GetClusterCACertResponse, error) {
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Extract the TLS CA for this cluster.
	hostCA, err := a.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.HostCA,
		DomainName: clusterName.GetClusterName(),
	}, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certs := services.GetTLSCerts(hostCA)
	if len(certs) < 1 {
		return nil, trace.NotFound("no tls certs found in host CA")
	}
	allCerts := bytes.Join(certs, []byte("\n"))

	return &proto.GetClusterCACertResponse{
		TLSCA: allCerts,
	}, nil
}

// GenerateHostCert uses the private key of the CA to sign the public key of the host
// (along with meta data like host ID, node name, roles, and ttl) to generate a host certificate.
func (a *Server) GenerateHostCert(ctx context.Context, hostPublicKey []byte, hostID, nodeName string, principals []string, clusterName string, role types.SystemRole, ttl time.Duration) ([]byte, error) {
	domainName, err := a.GetDomainName()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// get the certificate authority that will be signing the public key of the host
	ca, err := a.Services.GetCertAuthority(context.TODO(), types.CertAuthID{
		Type:       types.HostCA,
		DomainName: domainName,
	}, true)
	if err != nil {
		return nil, trace.BadParameter("failed to load host CA for %q: %v", domainName, err)
	}

	caSigner, err := a.keyStore.GetSSHSigner(ctx, ca)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// create and sign!
	return a.generateHostCert(services.HostCertParams{
		CASigner:      caSigner,
		PublicHostKey: hostPublicKey,
		HostID:        hostID,
		NodeName:      nodeName,
		Principals:    principals,
		ClusterName:   clusterName,
		Role:          role,
		TTL:           ttl,
	})
}

func (a *Server) generateHostCert(p services.HostCertParams) ([]byte, error) {
	authPref, err := a.GetAuthPreference(context.TODO())
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if p.Role == types.RoleNode {
		if lockErr := a.checkLockInForce(authPref.GetLockingMode(),
			[]types.LockTarget{{Node: p.HostID}, {Node: HostFQDN(p.HostID, p.ClusterName)}},
		); lockErr != nil {
			return nil, trace.Wrap(lockErr)
		}
	}
	return a.Authority.GenerateHostCert(p)
}

// GetKeyStore returns the KeyStore used by the auth server
func (a *Server) GetKeyStore() *keystore.Manager {
	return a.keyStore
}

type certRequest struct {
	// user is a user to generate certificate for
	user types.User
	// impersonator is a user who generates the certificate,
	// is set when different from the user in the certificate
	impersonator string
	// checker is used to perform RBAC checks.
	checker services.AccessChecker
	// ttl is Duration of the certificate
	ttl time.Duration
	// publicKey is RSA public key in authorized_keys format
	publicKey []byte
	// compatibility is compatibility mode
	compatibility string
	// overrideRoleTTL is used for requests when the requested TTL should not be
	// adjusted based off the role of the user. This is used by tctl to allow
	// creating long lived user certs.
	overrideRoleTTL bool
	// usage is a list of acceptable usages to be encoded in X509 certificate,
	// is used to limit ways the certificate can be used, for example
	// the cert can be only used against kubernetes endpoint, and not auth endpoint,
	// no usage means unrestricted (to keep backwards compatibility)
	usage []string
	// routeToCluster is an optional teleport cluster name to route the
	// certificate requests to, this teleport cluster name will be used to
	// route the requests to in case of kubernetes
	routeToCluster string
	// kubernetesCluster specifies the target kubernetes cluster for TLS
	// identities. This can be empty on older Teleport clients.
	kubernetesCluster string
	// traits hold claim data used to populate a role at runtime.
	traits wrappers.Traits
	// activeRequests tracks privilege escalation requests applied
	// during the construction of the certificate.
	activeRequests services.RequestIDs
	// appSessionID is the session ID of the application session.
	appSessionID string
	// appPublicAddr is the public address of the application.
	appPublicAddr string
	// appClusterName is the name of the cluster this application is in.
	appClusterName string
	// appName is the name of the application to generate cert for.
	appName string
	// awsRoleARN is the role ARN to generate certificate for.
	awsRoleARN string
	// dbService identifies the name of the database service requests will
	// be routed to.
	dbService string
	// dbProtocol specifies the protocol of the database a certificate will
	// be issued for.
	dbProtocol string
	// dbUser is the optional database user which, if provided, will be used
	// as a default username.
	dbUser string
	// dbName is the optional database name which, if provided, will be used
	// as a default database.
	dbName string
	// mfaVerified is the UUID of an MFA device when this certRequest was
	// created immediately after an MFA check.
	mfaVerified string
	// previousIdentityExpires is the expiry time of the identity/cert that this
	// identity/cert was derived from. It is used to determine a session's hard
	// deadline in cases where both require_session_mfa and disconnect_expired_cert
	// are enabled. See https://github.com/gravitational/teleport/issues/18544.
	previousIdentityExpires time.Time
	// clientIP is an IP of the client requesting the certificate.
	clientIP string
	// sourceIP is an IP this certificate should be pinned to
	sourceIP string
	// disallowReissue flags that a cert should not be allowed to issue future
	// certificates.
	disallowReissue bool
	// renewable indicates that the certificate can be renewed,
	// having its TTL increased
	renewable bool
	// includeHostCA indicates that host CA certs should be included in the
	// returned certs
	includeHostCA bool
	// generation indicates the number of times this certificate has been
	// renewed.
	generation uint64
	// connectionDiagnosticID contains the ID of the ConnectionDiagnostic.
	// The Node/Agent will append connection traces to this instance.
	connectionDiagnosticID string
	// attestationStatement is an attestation statement associated with the given public key.
	attestationStatement *keys.AttestationStatement
}

// check verifies the cert request is valid.
func (r *certRequest) check() error {
	if r.user == nil {
		return trace.BadParameter("missing parameter user")
	}
	if r.checker == nil {
		return trace.BadParameter("missing parameter checker")
	}

	// When generating certificate for MongoDB access, database username must
	// be encoded into it. This is required to be able to tell which database
	// user to authenticate the connection as.
	if r.dbProtocol == defaults.ProtocolMongoDB {
		if r.dbUser == "" {
			return trace.BadParameter("must provide database user name to generate certificate for database %q", r.dbService)
		}
	}
	return nil
}

type certRequestOption func(*certRequest)

func certRequestMFAVerified(mfaID string) certRequestOption {
	return func(r *certRequest) { r.mfaVerified = mfaID }
}

func certRequestPreviousIdentityExpires(previousIdentityExpires time.Time) certRequestOption {
	return func(r *certRequest) { r.previousIdentityExpires = previousIdentityExpires }
}

func certRequestClientIP(ip string) certRequestOption {
	return func(r *certRequest) { r.clientIP = ip }
}

// GenerateUserTestCerts is used to generate user certificate, used internally for tests
func (a *Server) GenerateUserTestCerts(key []byte, username string, ttl time.Duration, compatibility, routeToCluster, sourceIP string) ([]byte, []byte, error) {
	user, err := a.GetUser(username, false)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	accessInfo := services.AccessInfoFromUser(user)
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	checker, err := services.NewAccessChecker(accessInfo, clusterName.GetClusterName(), a)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	certs, err := a.generateUserCert(certRequest{
		user:           user,
		ttl:            ttl,
		compatibility:  compatibility,
		publicKey:      key,
		routeToCluster: routeToCluster,
		checker:        checker,
		traits:         user.GetTraits(),
		sourceIP:       sourceIP,
	})
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	return certs.SSH, certs.TLS, nil
}

// AppTestCertRequest combines parameters for generating a test app access cert.
type AppTestCertRequest struct {
	// PublicKey is the public key to sign.
	PublicKey []byte
	// Username is the Teleport user name to sign certificate for.
	Username string
	// TTL is the test certificate validity period.
	TTL time.Duration
	// PublicAddr is the application public address. Used for routing.
	PublicAddr string
	// ClusterName is the name of the cluster application resides in. Used for routing.
	ClusterName string
	// SessionID is the optional session ID to encode. Used for routing.
	SessionID string
	// AWSRoleARN is optional AWS role ARN a user wants to assume to encode.
	AWSRoleARN string
}

// GenerateUserAppTestCert generates an application specific certificate, used
// internally for tests.
func (a *Server) GenerateUserAppTestCert(req AppTestCertRequest) ([]byte, error) {
	user, err := a.GetUser(req.Username, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	accessInfo := services.AccessInfoFromUser(user)
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	checker, err := services.NewAccessChecker(accessInfo, clusterName.GetClusterName(), a)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	certs, err := a.generateUserCert(certRequest{
		user:      user,
		publicKey: req.PublicKey,
		checker:   checker,
		ttl:       req.TTL,
		// Set the login to be a random string. Application certificates are never
		// used to log into servers but SSH certificate generation code requires a
		// principal be in the certificate.
		traits: wrappers.Traits(map[string][]string{
			constants.TraitLogins: {uuid.New().String()},
		}),
		// Only allow this certificate to be used for applications.
		usage: []string{teleport.UsageAppsOnly},
		// Add in the application routing information.
		appSessionID:   sessionID,
		appPublicAddr:  req.PublicAddr,
		appClusterName: req.ClusterName,
		awsRoleARN:     req.AWSRoleARN,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return certs.TLS, nil
}

// DatabaseTestCertRequest combines parameters for generating test database
// access certificate.
type DatabaseTestCertRequest struct {
	// PublicKey is the public key to sign.
	PublicKey []byte
	// Cluster is the Teleport cluster name.
	Cluster string
	// Username is the Teleport username.
	Username string
	// RouteToDatabase contains database routing information.
	RouteToDatabase tlsca.RouteToDatabase
}

// GenerateDatabaseTestCert generates a database access certificate for the
// provided parameters. Used only internally in tests.
func (a *Server) GenerateDatabaseTestCert(req DatabaseTestCertRequest) ([]byte, error) {
	user, err := a.GetUser(req.Username, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	accessInfo := services.AccessInfoFromUser(user)
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	checker, err := services.NewAccessChecker(accessInfo, clusterName.GetClusterName(), a)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certs, err := a.generateUserCert(certRequest{
		user:      user,
		publicKey: req.PublicKey,
		checker:   checker,
		ttl:       time.Hour,
		traits: map[string][]string{
			constants.TraitLogins: {req.Username},
		},
		routeToCluster: req.Cluster,
		dbService:      req.RouteToDatabase.ServiceName,
		dbProtocol:     req.RouteToDatabase.Protocol,
		dbUser:         req.RouteToDatabase.Username,
		dbName:         req.RouteToDatabase.Database,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return certs.TLS, nil
}

// generateUserCert generates user certificates
func (a *Server) generateUserCert(req certRequest) (*proto.Certs, error) {
	ctx := context.TODO()
	err := req.check()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if len(req.checker.GetAllowedResourceIDs()) > 0 && modules.GetModules().BuildType() != modules.BuildEnterprise {
		return nil, fmt.Errorf("Resource Access Requests: %w", ErrRequiresEnterprise)
	}

	// Reject the cert request if there is a matching lock in force.
	authPref, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	lockingMode := req.checker.LockingMode(authPref.GetLockingMode())
	lockTargets := []types.LockTarget{
		{User: req.user.GetName()},
		{MFADevice: req.mfaVerified},
	}
	lockTargets = append(lockTargets,
		services.RolesToLockTargets(req.checker.RoleNames())...,
	)
	lockTargets = append(lockTargets,
		services.AccessRequestsToLockTargets(req.activeRequests.AccessRequests)...,
	)
	if err := a.checkLockInForce(lockingMode, lockTargets); err != nil {
		return nil, trace.Wrap(err)
	}

	// reuse the same RSA keys for SSH and TLS keys
	cryptoPubKey, err := sshutils.CryptoPublicKey(req.publicKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// extract the passed in certificate format. if nothing was passed in, fetch
	// the certificate format from the role.
	certificateFormat, err := utils.CheckCertificateFormatFlag(req.compatibility)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if certificateFormat == teleport.CertificateFormatUnspecified {
		certificateFormat = req.checker.CertificateFormat()
	}

	var sessionTTL time.Duration
	var allowedLogins []string

	// If the role TTL is ignored, do not restrict session TTL and allowed logins.
	// The only caller setting this parameter should be "tctl auth sign".
	// Otherwise, set the session TTL to the smallest of all roles and
	// then only grant access to allowed logins based on that.
	if req.overrideRoleTTL {
		// Take whatever was passed in. Pass in 0 to CheckLoginDuration so all
		// logins are returned for the role set.
		sessionTTL = req.ttl
		allowedLogins, err = req.checker.CheckLoginDuration(0)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		// Adjust session TTL to the smaller of two values: the session TTL
		// requested in tsh or the session TTL for the role.
		sessionTTL = req.checker.AdjustSessionTTL(req.ttl)

		// Return a list of logins that meet the session TTL limit. This means if
		// the requested session TTL is larger than the max session TTL for a login,
		// that login will not be included in the list of allowed logins.
		allowedLogins, err = req.checker.CheckLoginDuration(sessionTTL)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// verify that the required private key policy for the requesting identity
	// is met by the provided attestation statement.
	requiredKeyPolicy := req.checker.PrivateKeyPolicy(authPref.GetPrivateKeyPolicy())
	attestedKeyPolicy, err := modules.GetModules().AttestHardwareKey(ctx, a, requiredKeyPolicy, req.attestationStatement, cryptoPubKey, sessionTTL)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clusterName, err := a.GetDomainName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if req.routeToCluster == "" {
		req.routeToCluster = clusterName
	}
	if req.routeToCluster != clusterName {
		// Authorize access to a remote cluster.
		rc, err := a.GetRemoteCluster(req.routeToCluster)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if err := req.checker.CheckAccessToRemoteCluster(rc); err != nil {
			if trace.IsAccessDenied(err) {
				return nil, trace.NotFound("remote cluster %q not found", req.routeToCluster)
			}
			return nil, trace.Wrap(err)
		}
	}

	userCA, err := a.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.UserCA,
		DomainName: clusterName,
	}, true)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	caSigner, err := a.keyStore.GetSSHSigner(ctx, userCA)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Add the special join-only principal used for joining sessions.
	// All users have access to this and join RBAC rules are checked after the connection is established.
	allowedLogins = append(allowedLogins, teleport.SSHSessionJoinPrincipal)

	requestedResourcesStr, err := types.ResourceIDsToString(req.checker.GetAllowedResourceIDs())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	params := services.UserCertParams{
		CASigner:                caSigner,
		PublicUserKey:           req.publicKey,
		Username:                req.user.GetName(),
		Impersonator:            req.impersonator,
		AllowedLogins:           allowedLogins,
		TTL:                     sessionTTL,
		Roles:                   req.checker.RoleNames(),
		CertificateFormat:       certificateFormat,
		PermitPortForwarding:    req.checker.CanPortForward(),
		PermitAgentForwarding:   req.checker.CanForwardAgents(),
		PermitX11Forwarding:     req.checker.PermitX11Forwarding(),
		RouteToCluster:          req.routeToCluster,
		Traits:                  req.traits,
		ActiveRequests:          req.activeRequests,
		MFAVerified:             req.mfaVerified,
		PreviousIdentityExpires: req.previousIdentityExpires,
		ClientIP:                req.clientIP,
		DisallowReissue:         req.disallowReissue,
		Renewable:               req.renewable,
		Generation:              req.generation,
		CertificateExtensions:   req.checker.CertificateExtensions(),
		AllowedResourceIDs:      requestedResourcesStr,
		SourceIP:                req.sourceIP,
		ConnectionDiagnosticID:  req.connectionDiagnosticID,
		PrivateKeyPolicy:        attestedKeyPolicy,
	}
	sshCert, err := a.Authority.GenerateUserCert(params)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	kubeGroups, kubeUsers, err := req.checker.CheckKubeGroupsAndUsers(sessionTTL, req.overrideRoleTTL)
	// NotFound errors are acceptable - this user may have no k8s access
	// granted and that shouldn't prevent us from issuing a TLS cert.
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}
	// Only validate/default kubernetes cluster name for the current teleport
	// cluster. If this cert is targeting a trusted teleport cluster, leave all
	// the kubernetes cluster validation up to them.
	if req.routeToCluster == clusterName {
		req.kubernetesCluster, err = kubeutils.CheckOrSetKubeCluster(a.closeCtx, a, req.kubernetesCluster, clusterName)
		if err != nil {
			if !trace.IsNotFound(err) {
				return nil, trace.Wrap(err)
			}
			log.Debug("Failed setting default kubernetes cluster for user login (user did not provide a cluster); leaving KubernetesCluster extension in the TLS certificate empty")
		}
	}

	// See which database names and users this user is allowed to use.
	dbNames, dbUsers, err := req.checker.CheckDatabaseNamesAndUsers(sessionTTL, req.overrideRoleTTL)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}

	// See which AWS role ARNs this user is allowed to assume.
	roleARNs, err := req.checker.CheckAWSRoleARNs(sessionTTL, req.overrideRoleTTL)
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}

	// generate TLS certificate
	cert, signer, err := a.keyStore.GetTLSCertAndSigner(ctx, userCA)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsAuthority, err := tlsca.FromCertAndSigner(cert, signer)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	identity := tlsca.Identity{
		Username:          req.user.GetName(),
		Impersonator:      req.impersonator,
		Groups:            req.checker.RoleNames(),
		Principals:        allowedLogins,
		Usage:             req.usage,
		RouteToCluster:    req.routeToCluster,
		KubernetesCluster: req.kubernetesCluster,
		Traits:            req.traits,
		KubernetesGroups:  kubeGroups,
		KubernetesUsers:   kubeUsers,
		RouteToApp: tlsca.RouteToApp{
			SessionID:   req.appSessionID,
			PublicAddr:  req.appPublicAddr,
			ClusterName: req.appClusterName,
			Name:        req.appName,
			AWSRoleARN:  req.awsRoleARN,
		},
		TeleportCluster: clusterName,
		RouteToDatabase: tlsca.RouteToDatabase{
			ServiceName: req.dbService,
			Protocol:    req.dbProtocol,
			Username:    req.dbUser,
			Database:    req.dbName,
		},
		DatabaseNames:           dbNames,
		DatabaseUsers:           dbUsers,
		MFAVerified:             req.mfaVerified,
		PreviousIdentityExpires: req.previousIdentityExpires,
		ClientIP:                req.clientIP,
		AWSRoleARNs:             roleARNs,
		ActiveRequests:          req.activeRequests.AccessRequests,
		DisallowReissue:         req.disallowReissue,
		Renewable:               req.renewable,
		Generation:              req.generation,
		AllowedResourceIDs:      req.checker.GetAllowedResourceIDs(),
		PrivateKeyPolicy:        attestedKeyPolicy,
	}
	subject, err := identity.Subject()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certRequest := tlsca.CertificateRequest{
		Clock:     a.clock,
		PublicKey: cryptoPubKey,
		Subject:   subject,
		NotAfter:  a.clock.Now().UTC().Add(sessionTTL),
	}
	tlsCert, err := tlsAuthority.GenerateCertificate(certRequest)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	eventIdentity := identity.GetEventIdentity()
	eventIdentity.Expires = certRequest.NotAfter
	if err := a.emitter.EmitAuditEvent(a.closeCtx, &apievents.CertificateCreate{
		Metadata: apievents.Metadata{
			Type: events.CertificateCreateEvent,
			Code: events.CertificateCreateCode,
		},
		CertificateType: events.CertificateTypeUser,
		Identity:        &eventIdentity,
	}); err != nil {
		log.WithError(err).Warn("Failed to emit certificate create event.")
	}

	// create certs struct to return to user
	certs := &proto.Certs{
		SSH: sshCert,
		TLS: tlsCert,
	}

	// always include user CA TLS and SSH certs
	cas := []types.CertAuthority{userCA}

	// also include host CA certs if requested
	if req.includeHostCA {
		hostCA, err := a.GetCertAuthority(ctx, types.CertAuthID{
			Type:       types.HostCA,
			DomainName: clusterName,
		}, false)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		cas = append(cas, hostCA)
	}

	for _, ca := range cas {
		certs.TLSCACerts = append(certs.TLSCACerts, services.GetTLSCerts(ca)...)
		certs.SSHCACerts = append(certs.SSHCACerts, services.GetSSHCheckingKeys(ca)...)
	}

	return certs, nil
}

// WithUserLock executes function authenticateFn that performs user authentication
// if authenticateFn returns non nil error, the login attempt will be logged in as failed.
// The only exception to this rule is ConnectionProblemError, in case if it occurs
// access will be denied, but login attempt will not be recorded
// this is done to avoid potential user lockouts due to backend failures
// In case if user exceeds defaults.MaxLoginAttempts
// the user account will be locked for defaults.AccountLockInterval
func (a *Server) WithUserLock(username string, authenticateFn func() error) error {
	user, err := a.Services.GetUser(username, false)
	if err != nil {
		if trace.IsNotFound(err) {
			// If user is not found, still call authenticateFn. It should
			// always return an error. This prevents username oracles and
			// timing attacks.
			return authenticateFn()
		}
		return trace.Wrap(err)
	}
	status := user.GetStatus()
	if status.IsLocked {
		if status.RecoveryAttemptLockExpires.After(a.clock.Now().UTC()) {
			log.Debugf("%v exceeds %v failed account recovery attempts, locked until %v",
				user.GetName(), defaults.MaxAccountRecoveryAttempts, apiutils.HumanTimeFormat(status.RecoveryAttemptLockExpires))

			err := trace.AccessDenied(MaxFailedAttemptsErrMsg)
			err.AddField(ErrFieldKeyUserMaxedAttempts, true)
			return err
		}
		if status.LockExpires.After(a.clock.Now().UTC()) {
			log.Debugf("%v exceeds %v failed login attempts, locked until %v",
				user.GetName(), defaults.MaxLoginAttempts, apiutils.HumanTimeFormat(status.LockExpires))

			err := trace.AccessDenied(MaxFailedAttemptsErrMsg)
			err.AddField(ErrFieldKeyUserMaxedAttempts, true)
			return err
		}
	}
	fnErr := authenticateFn()
	if fnErr == nil {
		// upon successful login, reset the failed attempt counter
		err = a.DeleteUserLoginAttempts(username)
		if !trace.IsNotFound(err) {
			return trace.Wrap(err)
		}

		return nil
	}
	// do not lock user in case if DB is flaky or down
	if trace.IsConnectionProblem(err) {
		return trace.Wrap(fnErr)
	}
	// log failed attempt and possibly lock user
	attempt := services.LoginAttempt{Time: a.clock.Now().UTC(), Success: false}
	err = a.AddUserLoginAttempt(username, attempt, defaults.AttemptTTL)
	if err != nil {
		log.Error(trace.DebugReport(err))
		return trace.Wrap(fnErr)
	}
	loginAttempts, err := a.GetUserLoginAttempts(username)
	if err != nil {
		log.Error(trace.DebugReport(err))
		return trace.Wrap(fnErr)
	}
	if !services.LastFailed(defaults.MaxLoginAttempts, loginAttempts) {
		log.Debugf("%v user has less than %v failed login attempts", username, defaults.MaxLoginAttempts)
		return trace.Wrap(fnErr)
	}
	lockUntil := a.clock.Now().UTC().Add(defaults.AccountLockInterval)
	log.Debug(fmt.Sprintf("%v exceeds %v failed login attempts, locked until %v",
		username, defaults.MaxLoginAttempts, apiutils.HumanTimeFormat(lockUntil)))
	user.SetLocked(lockUntil, "user has exceeded maximum failed login attempts")
	err = a.UpsertUser(user)
	if err != nil {
		log.Error(trace.DebugReport(err))
		return trace.Wrap(fnErr)
	}

	retErr := trace.AccessDenied(MaxFailedAttemptsErrMsg)
	retErr.AddField(ErrFieldKeyUserMaxedAttempts, true)
	return retErr
}

// PreAuthenticatedSignIn is for MFA authentication methods where the password
// is already checked before issuing the second factor challenge
func (a *Server) PreAuthenticatedSignIn(ctx context.Context, user string, identity tlsca.Identity) (types.WebSession, error) {
	accessInfo, err := services.AccessInfoFromLocalIdentity(identity, a)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sess, err := a.NewWebSession(ctx, types.NewWebSessionRequest{
		User:                 user,
		Roles:                accessInfo.Roles,
		Traits:               accessInfo.Traits,
		AccessRequests:       identity.ActiveRequests,
		RequestedResourceIDs: accessInfo.AllowedResourceIDs,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.upsertWebSession(ctx, user, sess); err != nil {
		return nil, trace.Wrap(err)
	}
	return sess.WithoutSecrets(), nil
}

// CreateAuthenticateChallenge implements AuthService.CreateAuthenticateChallenge.
func (a *Server) CreateAuthenticateChallenge(ctx context.Context, req *proto.CreateAuthenticateChallengeRequest) (*proto.MFAAuthenticateChallenge, error) {
	var username string
	var passwordless bool

	switch req.GetRequest().(type) {
	case *proto.CreateAuthenticateChallengeRequest_UserCredentials:
		username = req.GetUserCredentials().GetUsername()

		if err := a.WithUserLock(username, func() error {
			return a.checkPasswordWOToken(username, req.GetUserCredentials().GetPassword())
		}); err != nil {
			return nil, trace.Wrap(err)
		}

	case *proto.CreateAuthenticateChallengeRequest_RecoveryStartTokenID:
		token, err := a.GetUserToken(ctx, req.GetRecoveryStartTokenID())
		if err != nil {
			log.Error(trace.DebugReport(err))
			return nil, trace.AccessDenied("invalid token")
		}

		if err := a.verifyUserToken(token, UserTokenTypeRecoveryStart); err != nil {
			return nil, trace.Wrap(err)
		}

		username = token.GetUser()

	case *proto.CreateAuthenticateChallengeRequest_Passwordless:
		passwordless = true // Allows empty username.

	default: // unset or CreateAuthenticateChallengeRequest_ContextUser.
		var err error
		username, err = GetClientUsername(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	challenges, err := a.mfaAuthChallenge(ctx, username, passwordless)
	if err != nil {
		log.Error(trace.DebugReport(err))
		return nil, trace.AccessDenied("unable to create MFA challenges")
	}
	return challenges, nil
}

// CreateRegisterChallenge implements AuthService.CreateRegisterChallenge.
func (a *Server) CreateRegisterChallenge(ctx context.Context, req *proto.CreateRegisterChallengeRequest) (*proto.MFARegisterChallenge, error) {
	token, err := a.GetUserToken(ctx, req.GetTokenID())
	if err != nil {
		log.Error(trace.DebugReport(err))
		return nil, trace.AccessDenied("invalid token")
	}

	allowedTokenTypes := []string{
		UserTokenTypePrivilege,
		UserTokenTypePrivilegeException,
		UserTokenTypeResetPassword,
		UserTokenTypeResetPasswordInvite,
		UserTokenTypeRecoveryApproved,
	}

	if err := a.verifyUserToken(token, allowedTokenTypes...); err != nil {
		return nil, trace.AccessDenied("invalid token")
	}

	regChal, err := a.createRegisterChallenge(ctx, &newRegisterChallengeRequest{
		username:    token.GetUser(),
		token:       token,
		deviceType:  req.GetDeviceType(),
		deviceUsage: req.GetDeviceUsage(),
	})

	return regChal, trace.Wrap(err)
}

type newRegisterChallengeRequest struct {
	username    string
	deviceType  proto.DeviceType
	deviceUsage proto.DeviceUsage

	// token is a user token resource.
	// It is used as following:
	//  - TOTP:
	//    - create a UserTokenSecrets resource
	//    - store by token's ID using Server's IdentityService.
	//  - MFA:
	//    - store challenge by the token's ID
	//    - store by token's ID using Server's IdentityService.
	// This field can be empty to use storage overrides.
	token types.UserToken

	// webIdentityOverride is an optional RegistrationIdentity override to be used
	// to store webauthn challenge. A common override is decorating the regular
	// Identity with an in-memory SessionData storage.
	// Defaults to the Server's IdentityService.
	webIdentityOverride wanlib.RegistrationIdentity
}

func (a *Server) createRegisterChallenge(ctx context.Context, req *newRegisterChallengeRequest) (*proto.MFARegisterChallenge, error) {
	switch req.deviceType {
	case proto.DeviceType_DEVICE_TYPE_TOTP:
		otpKey, otpOpts, err := a.newTOTPKey(req.username)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		challenge := &proto.TOTPRegisterChallenge{
			Secret:        otpKey.Secret(),
			Issuer:        otpKey.Issuer(),
			PeriodSeconds: uint32(otpOpts.Period),
			Algorithm:     otpOpts.Algorithm.String(),
			Digits:        uint32(otpOpts.Digits.Length()),
			Account:       otpKey.AccountName(),
		}

		if req.token != nil {
			secrets, err := a.createTOTPUserTokenSecrets(ctx, req.token, otpKey)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			challenge.QRCode = secrets.GetQRCode()
		}

		return &proto.MFARegisterChallenge{Request: &proto.MFARegisterChallenge_TOTP{TOTP: challenge}}, nil

	case proto.DeviceType_DEVICE_TYPE_WEBAUTHN:
		cap, err := a.GetAuthPreference(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		webConfig, err := cap.GetWebauthn()
		if err != nil {
			return nil, trace.Wrap(err)
		}

		identity := req.webIdentityOverride
		if identity == nil {
			identity = a.Services
		}

		webRegistration := &wanlib.RegistrationFlow{
			Webauthn: webConfig,
			Identity: identity,
		}

		passwordless := req.deviceUsage == proto.DeviceUsage_DEVICE_USAGE_PASSWORDLESS
		credentialCreation, err := webRegistration.Begin(ctx, req.username, passwordless)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		return &proto.MFARegisterChallenge{Request: &proto.MFARegisterChallenge_Webauthn{
			Webauthn: wanlib.CredentialCreationToProto(credentialCreation),
		}}, nil

	default:
		return nil, trace.BadParameter("MFA device type %q unsupported", req.deviceType.String())
	}
}

// GetMFADevices returns all mfa devices for the user defined in the token or the user defined in context.
func (a *Server) GetMFADevices(ctx context.Context, req *proto.GetMFADevicesRequest) (*proto.GetMFADevicesResponse, error) {
	var username string

	if req.GetTokenID() != "" {
		token, err := a.GetUserToken(ctx, req.GetTokenID())
		if err != nil {
			log.Error(trace.DebugReport(err))
			return nil, trace.AccessDenied("invalid token")
		}

		if err := a.verifyUserToken(token, UserTokenTypeRecoveryApproved); err != nil {
			return nil, trace.Wrap(err)
		}

		username = token.GetUser()
	}

	if username == "" {
		var err error
		username, err = GetClientUsername(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	devs, err := a.Services.GetMFADevices(ctx, username, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &proto.GetMFADevicesResponse{
		Devices: devs,
	}, nil
}

// DeleteMFADeviceSync implements AuthService.DeleteMFADeviceSync.
func (a *Server) DeleteMFADeviceSync(ctx context.Context, req *proto.DeleteMFADeviceSyncRequest) error {
	token, err := a.GetUserToken(ctx, req.GetTokenID())
	if err != nil {
		log.Error(trace.DebugReport(err))
		return trace.AccessDenied("invalid token")
	}

	if err := a.verifyUserToken(token, UserTokenTypeRecoveryApproved, UserTokenTypePrivilege); err != nil {
		return trace.Wrap(err)
	}

	_, err = a.deleteMFADeviceSafely(ctx, token.GetUser(), req.GetDeviceName())
	return trace.Wrap(err)
}

// deleteMFADeviceSafely deletes the user's mfa device while preventing users from deleting their last device
// for clusters that require second factors, which prevents users from being locked out of their account.
func (a *Server) deleteMFADeviceSafely(ctx context.Context, user, deviceName string) (*types.MFADevice, error) {
	devs, err := a.Services.GetMFADevices(ctx, user, true)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	authPref, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	kindToSF := map[string]constants.SecondFactorType{
		fmt.Sprintf("%T", &types.MFADevice_Totp{}):     constants.SecondFactorOTP,
		fmt.Sprintf("%T", &types.MFADevice_U2F{}):      constants.SecondFactorWebauthn,
		fmt.Sprintf("%T", &types.MFADevice_Webauthn{}): constants.SecondFactorWebauthn,
	}
	sfToCount := make(map[constants.SecondFactorType]int)
	var knownDevices int
	var deviceToDelete *types.MFADevice

	// Find the device to delete and count devices.
	for _, d := range devs {
		// Match device by name or ID.
		if d.GetName() == deviceName || d.Id == deviceName {
			deviceToDelete = d
		}

		sf, ok := kindToSF[fmt.Sprintf("%T", d.Device)]
		switch {
		case !ok && d == deviceToDelete:
			return nil, trace.NotImplemented("cannot delete device of type %T", d.Device)
		case !ok:
			log.Warnf("Ignoring unknown device with type %T in deletion.", d.Device)
			continue
		}

		sfToCount[sf]++
		knownDevices++
	}
	if deviceToDelete == nil {
		return nil, trace.NotFound("MFA device %q does not exist", deviceName)
	}

	// Prevent users from deleting their last device for clusters that require second factors.
	const minDevices = 2
	switch sf := authPref.GetSecondFactor(); sf {
	case constants.SecondFactorOff, constants.SecondFactorOptional: // MFA is not required, allow deletion
	case constants.SecondFactorOn:
		if knownDevices < minDevices {
			return nil, trace.BadParameter(
				"cannot delete the last MFA device for this user; add a replacement device first to avoid getting locked out")
		}
	case constants.SecondFactorOTP, constants.SecondFactorWebauthn:
		if sfToCount[sf] < minDevices {
			return nil, trace.BadParameter(
				"cannot delete the last %s device for this user; add a replacement device first to avoid getting locked out", sf)
		}
	default:
		return nil, trace.BadParameter("unexpected second factor type: %s", sf)
	}

	if err := a.DeleteMFADevice(ctx, user, deviceToDelete.Id); err != nil {
		return nil, trace.Wrap(err)
	}

	// Emit deleted event.
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.MFADeviceDelete{
		Metadata: apievents.Metadata{
			Type:        events.MFADeviceDeleteEvent,
			Code:        events.MFADeviceDeleteEventCode,
			ClusterName: clusterName.GetClusterName(),
		},
		UserMetadata: apievents.UserMetadata{
			User: user,
		},
		MFADeviceMetadata: mfaDeviceEventMetadata(deviceToDelete),
	}); err != nil {
		return nil, trace.Wrap(err)
	}
	return deviceToDelete, nil
}

// AddMFADeviceSync implements AuthService.AddMFADeviceSync.
func (a *Server) AddMFADeviceSync(ctx context.Context, req *proto.AddMFADeviceSyncRequest) (*proto.AddMFADeviceSyncResponse, error) {
	privilegeToken, err := a.GetUserToken(ctx, req.GetTokenID())
	if err != nil {
		log.Error(trace.DebugReport(err))
		return nil, trace.AccessDenied("invalid token")
	}

	if err := a.verifyUserToken(privilegeToken, UserTokenTypePrivilege, UserTokenTypePrivilegeException); err != nil {
		return nil, trace.Wrap(err)
	}

	dev, err := a.verifyMFARespAndAddDevice(ctx, &newMFADeviceFields{
		username:      privilegeToken.GetUser(),
		newDeviceName: req.GetNewDeviceName(),
		tokenID:       privilegeToken.GetName(),
		deviceResp:    req.GetNewMFAResponse(),
		deviceUsage:   req.DeviceUsage,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &proto.AddMFADeviceSyncResponse{Device: dev}, nil
}

type newMFADeviceFields struct {
	username      string
	newDeviceName string
	// tokenID is the ID of a reset/invite/recovery token.
	// It is used as following:
	//  - TOTP:
	//    - look up TOTP secret stored by token ID
	//  - MFA:
	//    - look up challenge stored by token ID
	// This field can be empty to use storage overrides.
	tokenID string
	// totpSecret is a secret shared by client and server to generate totp codes.
	// Field can be empty to get secret by "tokenID".
	totpSecret string

	// webIdentityOverride is an optional RegistrationIdentity override to be used
	// for device registration. A common override is decorating the regular
	// Identity with an in-memory SessionData storage.
	// Defaults to the Server's IdentityService.
	webIdentityOverride wanlib.RegistrationIdentity
	// deviceResp is the register response from the new device.
	deviceResp *proto.MFARegisterResponse
	// deviceUsage describes the intended usage of the new device.
	deviceUsage proto.DeviceUsage
}

// verifyMFARespAndAddDevice validates MFA register response and on success adds the new MFA device.
func (a *Server) verifyMFARespAndAddDevice(ctx context.Context, req *newMFADeviceFields) (*types.MFADevice, error) {
	cap, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if cap.GetSecondFactor() == constants.SecondFactorOff {
		return nil, trace.BadParameter("second factor disabled by cluster configuration")
	}

	var dev *types.MFADevice
	switch req.deviceResp.GetResponse().(type) {
	case *proto.MFARegisterResponse_TOTP:
		dev, err = a.registerTOTPDevice(ctx, req.deviceResp, req)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	case *proto.MFARegisterResponse_Webauthn:
		dev, err = a.registerWebauthnDevice(ctx, req.deviceResp, req)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	default:
		return nil, trace.BadParameter("MFARegisterResponse is an unknown response type %T", req.deviceResp.Response)
	}

	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.MFADeviceAdd{
		Metadata: apievents.Metadata{
			Type:        events.MFADeviceAddEvent,
			Code:        events.MFADeviceAddEventCode,
			ClusterName: clusterName.GetClusterName(),
		},
		UserMetadata: apievents.UserMetadata{
			User: req.username,
		},
		MFADeviceMetadata: mfaDeviceEventMetadata(dev),
	}); err != nil {
		log.WithError(err).Warn("Failed to emit add mfa device event.")
	}

	return dev, nil
}

func (a *Server) registerTOTPDevice(ctx context.Context, regResp *proto.MFARegisterResponse, req *newMFADeviceFields) (*types.MFADevice, error) {
	cap, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if !cap.IsSecondFactorTOTPAllowed() {
		return nil, trace.BadParameter("second factor TOTP not allowed by cluster")
	}

	var secret string
	switch {
	case req.tokenID != "":
		secrets, err := a.GetUserTokenSecrets(ctx, req.tokenID)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		secret = secrets.GetOTPKey()
	case req.totpSecret != "":
		secret = req.totpSecret
	default:
		return nil, trace.BadParameter("missing TOTP secret")
	}

	dev, err := services.NewTOTPDevice(req.newDeviceName, secret, a.clock.Now())
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.checkTOTP(ctx, req.username, regResp.GetTOTP().GetCode(), dev); err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.UpsertMFADevice(ctx, req.username, dev); err != nil {
		return nil, trace.Wrap(err)
	}
	return dev, nil
}

func (a *Server) registerWebauthnDevice(ctx context.Context, regResp *proto.MFARegisterResponse, req *newMFADeviceFields) (*types.MFADevice, error) {
	cap, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if !cap.IsSecondFactorWebauthnAllowed() {
		return nil, trace.BadParameter("second factor webauthn not allowed by cluster")
	}

	webConfig, err := cap.GetWebauthn()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	identity := req.webIdentityOverride // Override Identity, if supplied.
	if identity == nil {
		identity = a.Services
	}
	webRegistration := &wanlib.RegistrationFlow{
		Webauthn: webConfig,
		Identity: identity,
	}
	// Finish upserts the device on success.
	dev, err := webRegistration.Finish(ctx, wanlib.RegisterResponse{
		User:             req.username,
		DeviceName:       req.newDeviceName,
		CreationResponse: wanlib.CredentialCreationResponseFromProto(regResp.GetWebauthn()),
		Passwordless:     req.deviceUsage == proto.DeviceUsage_DEVICE_USAGE_PASSWORDLESS,
	})
	return dev, trace.Wrap(err)
}

// GetWebSession returns existing web session described by req. Explicitly
// delegating to Services as it's directly implemented by Cache as well.
func (a *Server) GetWebSession(ctx context.Context, req types.GetWebSessionRequest) (types.WebSession, error) {
	return a.Services.GetWebSession(ctx, req)
}

// GetWebToken returns existing web token described by req. Explicitly
// delegating to Services as it's directly implemented by Cache as well.
func (a *Server) GetWebToken(ctx context.Context, req types.GetWebTokenRequest) (types.WebToken, error) {
	return a.Services.GetWebToken(ctx, req)
}

// ExtendWebSession creates a new web session for a user based on a valid previous (current) session.
//
// If there is an approved access request, additional roles are appended to the roles that were
// extracted from identity. The new session expiration time will not exceed the expiration time
// of the previous session.
//
// If there is a switchback request, the roles will switchback to user's default roles and
// the expiration time is derived from users recently logged in time.
func (a *Server) ExtendWebSession(ctx context.Context, req WebSessionReq, identity tlsca.Identity) (types.WebSession, error) {
	prevSession, err := a.GetWebSession(ctx, types.GetWebSessionRequest{
		User:      req.User,
		SessionID: req.PrevSessionID,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// consider absolute expiry time that may be set for this session
	// by some external identity service, so we can not renew this session
	// anymore without extra logic for renewal with external OIDC provider
	expiresAt := prevSession.GetExpiryTime()
	if !expiresAt.IsZero() && expiresAt.Before(a.clock.Now().UTC()) {
		return nil, trace.NotFound("web session has expired")
	}

	accessInfo, err := services.AccessInfoFromLocalIdentity(identity, a)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	roles := accessInfo.Roles
	traits := accessInfo.Traits
	allowedResourceIDs := accessInfo.AllowedResourceIDs
	accessRequests := identity.ActiveRequests

	if req.ReloadUser {
		// We don't call from the cache layer because we want to
		// retrieve the recently updated user. Otherwise the cache
		// returns stale data.
		user, err := a.Identity.GetUser(req.User, false)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		traits = user.GetTraits()

	} else if req.AccessRequestID != "" {
		accessRequest, err := a.getValidatedAccessRequest(ctx, req.User, req.AccessRequestID)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		roles = append(roles, accessRequest.GetRoles()...)
		roles = apiutils.Deduplicate(roles)
		accessRequests = apiutils.Deduplicate(append(accessRequests, req.AccessRequestID))

		if len(accessRequest.GetRequestedResourceIDs()) > 0 {
			// There's not a consistent way to merge multiple resource access
			// requests, a user may be able to request access to different resources
			// with different roles which should not overlap.
			if len(allowedResourceIDs) > 0 {
				return nil, trace.BadParameter("user is already logged in with a resource access request, cannot assume another")
			}
			allowedResourceIDs = accessRequest.GetRequestedResourceIDs()
		}

		// Let session expire with the shortest expiry time.
		if expiresAt.After(accessRequest.GetAccessExpiry()) {
			expiresAt = accessRequest.GetAccessExpiry()
		}
	} else if req.Switchback {
		if prevSession.GetLoginTime().IsZero() {
			return nil, trace.BadParameter("Unable to switchback, log in time was not recorded.")
		}

		// Get default/static roles.
		user, err := a.GetUser(req.User, false)
		if err != nil {
			return nil, trace.Wrap(err, "failed to switchback")
		}

		// Reset any search-based access requests
		allowedResourceIDs = nil

		// Calculate expiry time.
		roleSet, err := services.FetchRoles(user.GetRoles(), a, user.GetTraits())
		if err != nil {
			return nil, trace.Wrap(err)
		}

		sessionTTL := roleSet.AdjustSessionTTL(apidefaults.CertDuration)

		// Set default roles and expiration.
		expiresAt = prevSession.GetLoginTime().UTC().Add(sessionTTL)
		roles = user.GetRoles()
		accessRequests = nil
	}

	sessionTTL := utils.ToTTL(a.clock, expiresAt)
	sess, err := a.NewWebSession(ctx, types.NewWebSessionRequest{
		User:                 req.User,
		Roles:                roles,
		Traits:               traits,
		SessionTTL:           sessionTTL,
		AccessRequests:       accessRequests,
		RequestedResourceIDs: allowedResourceIDs,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Keep preserving the login time.
	sess.SetLoginTime(prevSession.GetLoginTime())

	sess.SetConsumedAccessRequestID(req.AccessRequestID)

	if err := a.upsertWebSession(ctx, req.User, sess); err != nil {
		return nil, trace.Wrap(err)
	}

	return sess, nil
}

func (a *Server) getValidatedAccessRequest(ctx context.Context, user, accessRequestID string) (types.AccessRequest, error) {
	reqFilter := types.AccessRequestFilter{
		User: user,
		ID:   accessRequestID,
	}

	reqs, err := a.GetAccessRequests(ctx, reqFilter)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if len(reqs) < 1 {
		return nil, trace.NotFound("access request %q not found", accessRequestID)
	}

	req := reqs[0]

	if !req.GetState().IsApproved() {
		if req.GetState().IsDenied() {
			return nil, trace.AccessDenied("access request %q has been denied", accessRequestID)
		}
		return nil, trace.AccessDenied("access request %q is awaiting approval", accessRequestID)
	}

	if err := services.ValidateAccessRequestForUser(ctx, a, req); err != nil {
		return nil, trace.Wrap(err)
	}

	accessExpiry := req.GetAccessExpiry()
	if accessExpiry.Before(a.GetClock().Now()) {
		return nil, trace.BadParameter("access request %q has expired", accessRequestID)
	}

	return req, nil
}

// CreateWebSession creates a new web session for user without any
// checks, is used by admins
func (a *Server) CreateWebSession(ctx context.Context, user string) (types.WebSession, error) {
	u, err := a.GetUser(user, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sess, err := a.NewWebSession(ctx, types.NewWebSessionRequest{
		User:      user,
		Roles:     u.GetRoles(),
		Traits:    u.GetTraits(),
		LoginTime: a.clock.Now().UTC(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.upsertWebSession(ctx, user, sess); err != nil {
		return nil, trace.Wrap(err)
	}
	return sess, nil
}

// GenerateToken generates multi-purpose authentication token.
func (a *Server) GenerateToken(ctx context.Context, req *proto.GenerateTokenRequest) (string, error) {
	ttl := defaults.ProvisioningTokenTTL
	if req.TTL != 0 {
		ttl = req.TTL.Get()
	}
	expires := a.clock.Now().UTC().Add(ttl)

	if req.Token == "" {
		token, err := utils.CryptoRandomHex(TokenLenBytes)
		if err != nil {
			return "", trace.Wrap(err)
		}
		req.Token = token
	}

	token, err := types.NewProvisionToken(req.Token, req.Roles, expires)
	if err != nil {
		return "", trace.Wrap(err)
	}
	if len(req.Labels) != 0 {
		meta := token.GetMetadata()
		meta.Labels = req.Labels
		token.SetMetadata(meta)
	}

	if err := a.UpsertToken(ctx, token); err != nil {
		return "", trace.Wrap(err)
	}

	userMetadata := ClientUserMetadata(ctx)
	for _, role := range req.Roles {
		if role == types.RoleTrustedCluster {
			if err := a.emitter.EmitAuditEvent(ctx, &apievents.TrustedClusterTokenCreate{
				Metadata: apievents.Metadata{
					Type: events.TrustedClusterTokenCreateEvent,
					Code: events.TrustedClusterTokenCreateCode,
				},
				UserMetadata: userMetadata,
			}); err != nil {
				log.WithError(err).Warn("Failed to emit trusted cluster token create event.")
			}
		}
	}

	return req.Token, nil
}

// ExtractHostID returns host id based on the hostname
func ExtractHostID(hostName string, clusterName string) (string, error) {
	suffix := "." + clusterName
	if !strings.HasSuffix(hostName, suffix) {
		return "", trace.BadParameter("expected suffix %q in %q", suffix, hostName)
	}
	return strings.TrimSuffix(hostName, suffix), nil
}

// HostFQDN consists of host UUID and cluster name joined via .
func HostFQDN(hostUUID, clusterName string) string {
	return fmt.Sprintf("%v.%v", hostUUID, clusterName)
}

// GenerateHostCerts generates new host certificates (signed
// by the host certificate authority) for a node.
func (a *Server) GenerateHostCerts(ctx context.Context, req *proto.HostCertsRequest) (*proto.Certs, error) {
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	if err := req.Role.Check(); err != nil {
		return nil, err
	}

	if err := a.limiter.AcquireConnection(req.Role.String()); err != nil {
		generateThrottledRequestsCount.Inc()
		log.Debugf("Node %q [%v] is rate limited: %v.", req.NodeName, req.HostID, req.Role)
		return nil, trace.Wrap(err)
	}
	defer a.limiter.ReleaseConnection(req.Role.String())

	// only observe latencies for non-throttled requests
	start := a.clock.Now()
	defer generateRequestsLatencies.Observe(time.Since(start).Seconds())

	generateRequestsCount.Inc()
	generateRequestsCurrent.Inc()
	defer generateRequestsCurrent.Dec()

	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// If the request contains 0.0.0.0, this implies an advertise IP was not
	// specified on the node. Try and guess what the address by replacing 0.0.0.0
	// with the RemoteAddr as known to the Auth Server.
	if apiutils.SliceContainsStr(req.AdditionalPrincipals, defaults.AnyAddress) {
		remoteHost, err := utils.Host(req.RemoteAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		req.AdditionalPrincipals = utils.ReplaceInSlice(
			req.AdditionalPrincipals,
			defaults.AnyAddress,
			remoteHost)
	}

	if _, _, _, _, err := ssh.ParseAuthorizedKey(req.PublicSSHKey); err != nil {
		return nil, trace.BadParameter("failed to parse SSH public key")
	}
	cryptoPubKey, err := tlsca.ParsePublicKeyPEM(req.PublicTLSKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// get the certificate authority that will be signing the public key of the host,
	client := a.Cache
	if req.NoCache {
		client = a.Services
	}
	ca, err := client.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.HostCA,
		DomainName: clusterName.GetClusterName(),
	}, true)
	if err != nil {
		return nil, trace.BadParameter("failed to load host CA for %q: %v", clusterName.GetClusterName(), err)
	}

	// could be a couple of scenarios, either client data is out of sync,
	// or auth server is out of sync, either way, for now check that
	// cache is out of sync, this will result in higher read rate
	// to the backend, which is a fine tradeoff
	if !req.NoCache && !req.Rotation.IsZero() && !req.Rotation.Matches(ca.GetRotation()) {
		log.Debugf("Client sent rotation state %v, cache state is %v, using state from the DB.", req.Rotation, ca.GetRotation())
		ca, err = a.Services.GetCertAuthority(ctx, types.CertAuthID{
			Type:       types.HostCA,
			DomainName: clusterName.GetClusterName(),
		}, true)
		if err != nil {
			return nil, trace.BadParameter("failed to load host CA for %q: %v", clusterName.GetClusterName(), err)
		}
		if !req.Rotation.Matches(ca.GetRotation()) {
			return nil, trace.BadParameter(""+
				"the client expected state is out of sync, server rotation state: %v, "+
				"client rotation state: %v, re-register the client from scratch to fix the issue.",
				ca.GetRotation(), req.Rotation)
		}
	}

	isAdminRole := req.Role == types.RoleAdmin

	cert, signer, err := a.keyStore.GetTLSCertAndSigner(ctx, ca)
	if trace.IsNotFound(err) && isAdminRole {
		// If there is no local TLS signer found in the host CA ActiveKeys, this
		// auth server may have a newly configured HSM and has only populated
		// local keys in the AdditionalTrustedKeys until the next CA rotation.
		// This is the only case where we should be able to get a signer from
		// AdditionalTrustedKeys but not ActiveKeys.
		cert, signer, err = a.keyStore.GetAdditionalTrustedTLSCertAndSigner(ctx, ca)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsAuthority, err := tlsca.FromCertAndSigner(cert, signer)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	caSigner, err := a.keyStore.GetSSHSigner(ctx, ca)
	if trace.IsNotFound(err) && isAdminRole {
		// If there is no local SSH signer found in the host CA ActiveKeys, this
		// auth server may have a newly configured HSM and has only populated
		// local keys in the AdditionalTrustedKeys until the next CA rotation.
		// This is the only case where we should be able to get a signer from
		// AdditionalTrustedKeys but not ActiveKeys.
		caSigner, err = a.keyStore.GetAdditionalTrustedSSHSigner(ctx, ca)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// generate host SSH certificate
	hostSSHCert, err := a.generateHostCert(services.HostCertParams{
		CASigner:      caSigner,
		PublicHostKey: req.PublicSSHKey,
		HostID:        req.HostID,
		NodeName:      req.NodeName,
		ClusterName:   clusterName.GetClusterName(),
		Role:          req.Role,
		Principals:    req.AdditionalPrincipals,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if req.Role == types.RoleInstance && len(req.SystemRoles) == 0 {
		return nil, trace.BadParameter("cannot generate instance cert with no system roles")
	}

	systemRoles := make([]string, 0, len(req.SystemRoles))
	for _, r := range req.SystemRoles {
		systemRoles = append(systemRoles, string(r))
	}

	// generate host TLS certificate
	identity := tlsca.Identity{
		Username:        HostFQDN(req.HostID, clusterName.GetClusterName()),
		Groups:          []string{req.Role.String()},
		TeleportCluster: clusterName.GetClusterName(),
		SystemRoles:     systemRoles,
	}
	subject, err := identity.Subject()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certRequest := tlsca.CertificateRequest{
		Clock:     a.clock,
		PublicKey: cryptoPubKey,
		Subject:   subject,
		NotAfter:  a.clock.Now().UTC().Add(defaults.CATTL),
		DNSNames:  append([]string{}, req.AdditionalPrincipals...),
	}

	// API requests need to specify a DNS name, which must be present in the certificate's DNS Names.
	// The target DNS is not always known in advance, so we add a default one to all certificates.
	certRequest.DNSNames = append(certRequest.DNSNames, DefaultDNSNamesForRole(req.Role)...)
	// Unlike additional principals, DNS Names is x509 specific and is limited
	// to services with TLS endpoints (e.g. auth, proxies, kubernetes)
	if (types.SystemRoles{req.Role}).IncludeAny(types.RoleAuth, types.RoleAdmin, types.RoleProxy, types.RoleKube, types.RoleWindowsDesktop) {
		certRequest.DNSNames = append(certRequest.DNSNames, req.DNSNames...)
	}
	hostTLSCert, err := tlsAuthority.GenerateCertificate(certRequest)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &proto.Certs{
		SSH:        hostSSHCert,
		TLS:        hostTLSCert,
		TLSCACerts: services.GetTLSCerts(ca),
		SSHCACerts: services.GetSSHCheckingKeys(ca),
	}, nil
}

// UnstableAssertSystemRole is not a stable part of the public API. Used by older
// instances to prove that they hold a given system role.
// DELETE IN: 12.0 (deprecated in v11, but required for back-compat with v10 clients)
func (a *Server) UnstableAssertSystemRole(ctx context.Context, req proto.UnstableSystemRoleAssertion) error {
	return trace.Wrap(a.unstable.AssertSystemRole(ctx, req))
}

func (a *Server) UnstableGetSystemRoleAssertions(ctx context.Context, serverID string, assertionID string) (proto.UnstableSystemRoleAssertionSet, error) {
	set, err := a.unstable.GetSystemRoleAssertions(ctx, serverID, assertionID)
	return set, trace.Wrap(err)
}

func (a *Server) RegisterInventoryControlStream(ics client.UpstreamInventoryControlStream, hello proto.UpstreamInventoryHello) error {
	// upstream hello is pulled and checked at rbac layer. we wait to send the downstream hello until we get here
	// in order to simplify creation of in-memory streams when dealing with local auth (note: in theory we could
	// send hellos simultaneously to slightly improve perf, but there is a potential benefit to having the
	// downstream hello serve double-duty as an indicator of having successfully transitioned the rbac layer).
	downstreamHello := proto.DownstreamInventoryHello{
		Version:  teleport.Version,
		ServerID: a.ServerID,
	}
	if err := ics.Send(a.CloseContext(), downstreamHello); err != nil {
		return trace.Wrap(err)
	}
	a.inventory.RegisterControlStream(ics, hello)
	return nil
}

// MakeLocalInventoryControlStream sets up an in-memory control stream which automatically registers with this auth
// server upon hello exchange.
func (a *Server) MakeLocalInventoryControlStream(opts ...client.ICSPipeOption) client.DownstreamInventoryControlStream {
	upstream, downstream := client.InventoryControlStreamPipe(opts...)
	go func() {
		select {
		case msg := <-upstream.Recv():
			hello, ok := msg.(proto.UpstreamInventoryHello)
			if !ok {
				upstream.CloseWithError(trace.BadParameter("expected upstream hello, got: %T", msg))
				return
			}
			if err := a.RegisterInventoryControlStream(upstream, hello); err != nil {
				upstream.CloseWithError(err)
				return
			}
		case <-upstream.Done():
		case <-a.CloseContext().Done():
			upstream.Close()
		}
	}()
	return downstream
}

func (a *Server) GetInventoryStatus(ctx context.Context, req proto.InventoryStatusRequest) proto.InventoryStatusSummary {
	var rsp proto.InventoryStatusSummary
	if req.Connected {
		a.inventory.Iter(func(handle inventory.UpstreamHandle) {
			rsp.Connected = append(rsp.Connected, handle.Hello())
		})
	}
	return rsp
}

func (a *Server) PingInventory(ctx context.Context, req proto.InventoryPingRequest) (proto.InventoryPingResponse, error) {
	stream, ok := a.inventory.GetControlStream(req.ServerID)
	if !ok {
		return proto.InventoryPingResponse{}, trace.NotFound("no control stream found for server %q", req.ServerID)
	}

	d, err := stream.Ping(ctx)
	if err != nil {
		return proto.InventoryPingResponse{}, trace.Wrap(err)
	}

	return proto.InventoryPingResponse{
		Duration: d,
	}, nil
}

// TokenExpiredOrNotFound is a special message returned by the auth server when provisioning
// tokens are either past their TTL, or could not be found.
const TokenExpiredOrNotFound = "token expired or not found"

// ValidateToken takes a provisioning token value and finds if it's valid. Returns
// a list of roles this token allows its owner to assume and token labels, or an error if the token
// cannot be found.
func (a *Server) ValidateToken(ctx context.Context, token string) (types.ProvisionToken, error) {
	tkns, err := a.GetStaticTokens()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// First check if the token is a static token. If it is, return right away.
	// Static tokens have no expiration.
	for _, st := range tkns.GetStaticTokens() {
		if subtle.ConstantTimeCompare([]byte(st.GetName()), []byte(token)) == 1 {
			return st, nil
		}
	}

	// If it's not a static token, check if it's a ephemeral token in the backend.
	// If a ephemeral token is found, make sure it's still valid.
	tok, err := a.GetToken(ctx, token)
	if err != nil {
		if trace.IsNotFound(err) {
			return nil, trace.AccessDenied(TokenExpiredOrNotFound)
		}
		return nil, trace.Wrap(err)
	}
	if !a.checkTokenTTL(tok) {
		return nil, trace.AccessDenied(TokenExpiredOrNotFound)
	}

	return tok, nil
}

// checkTokenTTL checks if the token is still valid. If it is not, the token
// is removed from the backend and returns false. Otherwise returns true.
func (a *Server) checkTokenTTL(tok types.ProvisionToken) bool {
	ctx := context.TODO()
	now := a.clock.Now().UTC()
	if tok.Expiry().Before(now) {
		err := a.DeleteToken(ctx, tok.GetName())
		if err != nil {
			if !trace.IsNotFound(err) {
				log.Warnf("Unable to delete token from backend: %v.", err)
			}
		}
		return false
	}
	return true
}

func (a *Server) DeleteToken(ctx context.Context, token string) (err error) {
	tkns, err := a.GetStaticTokens()
	if err != nil {
		return trace.Wrap(err)
	}

	// is this a static token?
	for _, st := range tkns.GetStaticTokens() {
		if subtle.ConstantTimeCompare([]byte(st.GetName()), []byte(token)) == 1 {
			return trace.BadParameter("token %s is statically configured and cannot be removed", backend.MaskKeyName(token))
		}
	}
	// Delete a user token.
	if err = a.DeleteUserToken(ctx, token); err == nil {
		return nil
	}
	// delete node token:
	if err = a.Services.DeleteToken(ctx, token); err == nil {
		return nil
	}
	return trace.Wrap(err)
}

// GetTokens returns all tokens (machine provisioning ones and user tokens). Machine
// tokens usually have "node roles", like auth,proxy,node and user invitation tokens have 'signup' role
func (a *Server) GetTokens(ctx context.Context, opts ...services.MarshalOption) (tokens []types.ProvisionToken, err error) {
	// get node tokens:
	tokens, err = a.Services.GetTokens(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// get static tokens:
	tkns, err := a.GetStaticTokens()
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}
	if err == nil {
		tokens = append(tokens, tkns.GetStaticTokens()...)
	}
	// get user tokens:
	userTokens, err := a.GetUserTokens(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// convert user tokens to machine tokens:
	for _, t := range userTokens {
		roles := types.SystemRoles{types.RoleSignup}
		tok, err := types.NewProvisionToken(t.GetName(), roles, t.Expiry())
		if err != nil {
			return nil, trace.Wrap(err)
		}
		tokens = append(tokens, tok)
	}
	return tokens, nil
}

// NewWebSession creates and returns a new web session for the specified request
func (a *Server) NewWebSession(ctx context.Context, req types.NewWebSessionRequest) (types.WebSession, error) {
	user, err := a.GetUser(req.User, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	checker, err := services.NewAccessChecker(&services.AccessInfo{
		Roles:              req.Roles,
		Traits:             req.Traits,
		AllowedResourceIDs: req.RequestedResourceIDs,
	}, clusterName.GetClusterName(), a)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	netCfg, err := a.GetClusterNetworkingConfig(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	priv, pub, err := native.GenerateKeyPair()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sessionTTL := req.SessionTTL
	if sessionTTL == 0 {
		sessionTTL = checker.AdjustSessionTTL(apidefaults.CertDuration)
	}
	certs, err := a.generateUserCert(certRequest{
		user:           user,
		ttl:            sessionTTL,
		publicKey:      pub,
		checker:        checker,
		traits:         req.Traits,
		activeRequests: services.RequestIDs{AccessRequests: req.AccessRequests},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	token, err := utils.CryptoRandomHex(SessionTokenBytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	bearerToken, err := utils.CryptoRandomHex(SessionTokenBytes)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	bearerTokenTTL := utils.MinTTL(sessionTTL, BearerTokenTTL)

	startTime := a.clock.Now()
	if !req.LoginTime.IsZero() {
		startTime = req.LoginTime
	}

	sessionSpec := types.WebSessionSpecV2{
		User:               req.User,
		Priv:               priv,
		Pub:                certs.SSH,
		TLSCert:            certs.TLS,
		Expires:            startTime.UTC().Add(sessionTTL),
		BearerToken:        bearerToken,
		BearerTokenExpires: startTime.UTC().Add(bearerTokenTTL),
		LoginTime:          req.LoginTime,
		IdleTimeout:        types.Duration(netCfg.GetWebIdleTimeout()),
	}
	UserLoginCount.Inc()

	sess, err := types.NewWebSession(token, types.KindWebSession, sessionSpec)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sess, nil
}

// GetWebSessionInfo returns the web session specified with sessionID for the given user.
// The session is stripped of any authentication details.
// Implements auth.WebUIService
func (a *Server) GetWebSessionInfo(ctx context.Context, user, sessionID string) (types.WebSession, error) {
	sess, err := a.GetWebSession(ctx, types.GetWebSessionRequest{User: user, SessionID: sessionID})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sess.WithoutSecrets(), nil
}

func (a *Server) DeleteNamespace(namespace string) error {
	ctx := context.TODO()
	if namespace == apidefaults.Namespace {
		return trace.AccessDenied("can't delete default namespace")
	}
	nodes, err := a.GetNodes(ctx, namespace)
	if err != nil {
		return trace.Wrap(err)
	}
	if len(nodes) != 0 {
		return trace.BadParameter("can't delete namespace %v that has %v registered nodes", namespace, len(nodes))
	}
	return a.Services.DeleteNamespace(namespace)
}

func (a *Server) CreateAccessRequest(ctx context.Context, req types.AccessRequest) error {
	if err := services.ValidateAccessRequestForUser(ctx, a, req,
		// if request is in state pending, variable expansion must be applied
		services.ExpandVars(req.GetState().IsPending()),
	); err != nil {
		return trace.Wrap(err)
	}

	ttl, err := a.calculateMaxAccessTTL(ctx, req)
	if err != nil {
		return trace.Wrap(err)
	}
	now := a.clock.Now().UTC()
	req.SetCreationTime(now)
	exp := now.Add(ttl)
	// Set acccess expiry if an allowable default was not provided.
	if req.GetAccessExpiry().Before(now) || req.GetAccessExpiry().After(exp) {
		req.SetAccessExpiry(exp)
	}
	// By default, resource expiry should match access expiry.
	req.SetExpiry(req.GetAccessExpiry())
	// If the access-request is in a pending state, then the expiry of the underlying resource
	// is capped to to PendingAccessDuration in order to limit orphaned access requests.
	if req.GetState().IsPending() {
		pexp := now.Add(defaults.PendingAccessDuration)
		if pexp.Before(req.Expiry()) {
			req.SetExpiry(pexp)
		}
	}

	if req.GetDryRun() {
		// Made it this far with no errors, return before creating the request
		// if this is a dry run.
		return nil
	}

	if err := a.Services.CreateAccessRequest(ctx, req); err != nil {
		return trace.Wrap(err)
	}
	err = a.emitter.EmitAuditEvent(a.closeCtx, &apievents.AccessRequestCreate{
		Metadata: apievents.Metadata{
			Type: events.AccessRequestCreateEvent,
			Code: events.AccessRequestCreateCode,
		},
		UserMetadata: ClientUserMetadataWithUser(ctx, req.GetUser()),
		ResourceMetadata: apievents.ResourceMetadata{
			Expires: req.GetAccessExpiry(),
		},
		Roles:                req.GetRoles(),
		RequestedResourceIDs: apievents.ResourceIDs(req.GetRequestedResourceIDs()),
		RequestID:            req.GetName(),
		RequestState:         req.GetState().String(),
		Reason:               req.GetRequestReason(),
	})
	if err != nil {
		log.WithError(err).Warn("Failed to emit access request create event.")
	}
	return nil
}

func (a *Server) DeleteAccessRequest(ctx context.Context, name string) error {
	if err := a.Services.DeleteAccessRequest(ctx, name); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.AccessRequestDelete{
		Metadata: apievents.Metadata{
			Type: events.AccessRequestDeleteEvent,
			Code: events.AccessRequestDeleteCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		RequestID:    name,
	}); err != nil {
		log.WithError(err).Warn("Failed to emit access request delete event.")
	}
	return nil
}

func (a *Server) SetAccessRequestState(ctx context.Context, params types.AccessRequestUpdate) error {
	req, err := a.Services.SetAccessRequestState(ctx, params)
	if err != nil {
		return trace.Wrap(err)
	}
	event := &apievents.AccessRequestCreate{
		Metadata: apievents.Metadata{
			Type: events.AccessRequestUpdateEvent,
			Code: events.AccessRequestUpdateCode,
		},
		ResourceMetadata: apievents.ResourceMetadata{
			UpdatedBy: ClientUsername(ctx),
			Expires:   req.GetAccessExpiry(),
		},
		RequestID:    params.RequestID,
		RequestState: params.State.String(),
		Reason:       params.Reason,
		Roles:        params.Roles,
	}

	if delegator := apiutils.GetDelegator(ctx); delegator != "" {
		event.Delegator = delegator
	}

	if len(params.Annotations) > 0 {
		annotations, err := apievents.EncodeMapStrings(params.Annotations)
		if err != nil {
			log.WithError(err).Debugf("Failed to encode access request annotations.")
		} else {
			event.Annotations = annotations
		}
	}
	err = a.emitter.EmitAuditEvent(a.closeCtx, event)
	if err != nil {
		log.WithError(err).Warn("Failed to emit access request update event.")
	}
	return trace.Wrap(err)
}

func (a *Server) SubmitAccessReview(ctx context.Context, params types.AccessReviewSubmission) (types.AccessRequest, error) {
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// set up a checker for the review author
	checker, err := services.NewReviewPermissionChecker(ctx, a, params.Review.Author)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// don't bother continuing if the author has no allow directives
	if !checker.HasAllowDirectives() {
		return nil, trace.AccessDenied("user %q cannot submit reviews", params.Review.Author)
	}

	// final permission checks and review application must be done by the local backend
	// service, as their validity depends upon optimistic locking.
	req, err := a.ApplyAccessReview(ctx, params, checker)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	event := &apievents.AccessRequestCreate{
		Metadata: apievents.Metadata{
			Type:        events.AccessRequestReviewEvent,
			Code:        events.AccessRequestReviewCode,
			ClusterName: clusterName.GetClusterName(),
		},
		ResourceMetadata: apievents.ResourceMetadata{
			Expires: req.GetAccessExpiry(),
		},
		RequestID:     params.RequestID,
		RequestState:  req.GetState().String(),
		ProposedState: params.Review.ProposedState.String(),
		Reason:        params.Review.Reason,
		Reviewer:      params.Review.Author,
	}

	if len(params.Review.Annotations) > 0 {
		annotations, err := apievents.EncodeMapStrings(params.Review.Annotations)
		if err != nil {
			log.WithError(err).Debugf("Failed to encode access request annotations.")
		} else {
			event.Annotations = annotations
		}
	}
	if err := a.emitter.EmitAuditEvent(a.closeCtx, event); err != nil {
		log.WithError(err).Warn("Failed to emit access request update event.")
	}

	return req, nil
}

func (a *Server) GetAccessCapabilities(ctx context.Context, req types.AccessCapabilitiesRequest) (*types.AccessCapabilities, error) {
	caps, err := services.CalculateAccessCapabilities(ctx, a, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return caps, nil
}

// calculateMaxAccessTTL determines the maximum allowable TTL for a given access request
// based on the MaxSessionTTLs of the roles being requested (a access request's life cannot
// exceed the smallest allowable MaxSessionTTL value of the roles that it requests).
func (a *Server) calculateMaxAccessTTL(ctx context.Context, req types.AccessRequest) (time.Duration, error) {
	minTTL := defaults.MaxAccessDuration
	for _, roleName := range req.GetRoles() {
		role, err := a.GetRole(ctx, roleName)
		if err != nil {
			return 0, trace.Wrap(err)
		}
		roleTTL := time.Duration(role.GetOptions().MaxSessionTTL)
		if roleTTL > 0 && roleTTL < minTTL {
			minTTL = roleTTL
		}
	}
	return minTTL, nil
}

// NewKeepAliver returns a new instance of keep aliver
func (a *Server) NewKeepAliver(ctx context.Context) (types.KeepAliver, error) {
	cancelCtx, cancel := context.WithCancel(ctx)
	k := &authKeepAliver{
		a:           a,
		ctx:         cancelCtx,
		cancel:      cancel,
		keepAlivesC: make(chan types.KeepAlive),
	}
	go k.forwardKeepAlives()
	return k, nil
}

// GenerateCertAuthorityCRL generates an empty CRL for the local CA of a given type.
func (a *Server) GenerateCertAuthorityCRL(ctx context.Context, caType types.CertAuthType) ([]byte, error) {
	// Generate a CRL for the current cluster CA.
	clusterName, err := a.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	ca, err := a.GetCertAuthority(ctx, types.CertAuthID{
		Type:       caType,
		DomainName: clusterName.GetClusterName(),
	}, true)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// TODO(awly): this will only create a CRL for an active signer.
	// If there are multiple signers (multiple HSMs), we won't have the full CRL coverage.
	// Generate a CRL per signer and return all of them separately.

	cert, signer, err := a.keyStore.GetTLSCertAndSigner(ctx, ca)
	if trace.IsNotFound(err) {
		// If there is no local TLS signer found in the host CA ActiveKeys, this
		// auth server may have a newly configured HSM and has only populated
		// local keys in the AdditionalTrustedKeys until the next CA rotation.
		// This is the only case where we should be able to get a signer from
		// AdditionalTrustedKeys but not ActiveKeys.
		cert, signer, err = a.keyStore.GetAdditionalTrustedTLSCertAndSigner(ctx, ca)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsAuthority, err := tlsca.FromCertAndSigner(cert, signer)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Empty CRL valid for 1yr.
	template := &x509.RevocationList{
		Number:     big.NewInt(1),
		ThisUpdate: time.Now().Add(-1 * time.Minute), // 1 min in the past to account for clock skew.
		NextUpdate: time.Now().Add(365 * 24 * time.Hour),
	}
	crl, err := x509.CreateRevocationList(rand.Reader, template, tlsAuthority.Cert, tlsAuthority.Signer)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return crl, nil
}

// ErrDone indicates that resource iteration is complete
var ErrDone = errors.New("done iterating")

// IterateResources loads all resources matching the provided request and passes them one by one to the provided
// callback function. To stop iteration callers may return ErrDone from the callback function, which will result in
// a nil return from IterateResources. Any other errors returned from the callback function cause iteration to stop
// and the error to be returned.
func (a *Server) IterateResources(ctx context.Context, req proto.ListResourcesRequest, f func(resource types.ResourceWithLabels) error) error {
	for {
		resp, err := a.ListResources(ctx, req)
		if err != nil {
			return trace.Wrap(err)
		}

		for _, resource := range resp.Resources {
			if err := f(resource); err != nil {
				if errors.Is(err, ErrDone) {
					return nil
				}
				return trace.Wrap(err)
			}
		}

		if resp.NextKey == "" {
			return nil
		}

		req.StartKey = resp.NextKey
	}
}

// CreateAuditStream creates audit event stream
func (a *Server) CreateAuditStream(ctx context.Context, sid session.ID) (apievents.Stream, error) {
	streamer, err := a.modeStreamer(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return streamer.CreateAuditStream(ctx, sid)
}

// ResumeAuditStream resumes the stream that has been created
func (a *Server) ResumeAuditStream(ctx context.Context, sid session.ID, uploadID string) (apievents.Stream, error) {
	streamer, err := a.modeStreamer(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return streamer.ResumeAuditStream(ctx, sid, uploadID)
}

// modeStreamer creates streamer based on the event mode
func (a *Server) modeStreamer(ctx context.Context) (events.Streamer, error) {
	recConfig, err := a.GetSessionRecordingConfig(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// In sync mode, auth server forwards session control to the event log
	// in addition to sending them and data events to the record storage.
	if services.IsRecordSync(recConfig.GetMode()) {
		return events.NewTeeStreamer(a.streamer, a.emitter), nil
	}
	// In async mode, clients submit session control events
	// during the session in addition to writing a local
	// session recording to be uploaded at the end of the session,
	// so forwarding events here will result in duplicate events.
	return a.streamer, nil
}

// CreateApp creates a new application resource.
func (a *Server) CreateApp(ctx context.Context, app types.Application) error {
	if err := a.Services.CreateApp(ctx, app); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.AppCreate{
		Metadata: apievents.Metadata{
			Type: events.AppCreateEvent,
			Code: events.AppCreateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    app.GetName(),
			Expires: app.Expiry(),
		},
		AppMetadata: apievents.AppMetadata{
			AppURI:        app.GetURI(),
			AppPublicAddr: app.GetPublicAddr(),
			AppLabels:     app.GetStaticLabels(),
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit app create event.")
	}
	return nil
}

// UpdateApp updates an existing application resource.
func (a *Server) UpdateApp(ctx context.Context, app types.Application) error {
	if err := a.Services.UpdateApp(ctx, app); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.AppUpdate{
		Metadata: apievents.Metadata{
			Type: events.AppUpdateEvent,
			Code: events.AppUpdateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    app.GetName(),
			Expires: app.Expiry(),
		},
		AppMetadata: apievents.AppMetadata{
			AppURI:        app.GetURI(),
			AppPublicAddr: app.GetPublicAddr(),
			AppLabels:     app.GetStaticLabels(),
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit app update event.")
	}
	return nil
}

// DeleteApp deletes an application resource.
func (a *Server) DeleteApp(ctx context.Context, name string) error {
	if err := a.Services.DeleteApp(ctx, name); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.AppDelete{
		Metadata: apievents.Metadata{
			Type: events.AppDeleteEvent,
			Code: events.AppDeleteCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name: name,
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit app delete event.")
	}
	return nil
}

// CreateSessionTracker creates a tracker resource for an active session.
func (a *Server) CreateSessionTracker(ctx context.Context, tracker types.SessionTracker) (types.SessionTracker, error) {
	// Don't allow sessions that require moderation without the enterprise feature enabled.
	for _, policySet := range tracker.GetHostPolicySets() {
		if len(policySet.RequireSessionJoin) != 0 {
			if modules.GetModules().BuildType() != modules.BuildEnterprise {
				return nil, fmt.Errorf("Moderated Sessions: %w", ErrRequiresEnterprise)
			}
		}
	}

	return a.Services.CreateSessionTracker(ctx, tracker)
}

// CreateDatabase creates a new database resource.
func (a *Server) CreateDatabase(ctx context.Context, database types.Database) error {
	if err := a.Services.CreateDatabase(ctx, database); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.DatabaseCreate{
		Metadata: apievents.Metadata{
			Type: events.DatabaseCreateEvent,
			Code: events.DatabaseCreateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    database.GetName(),
			Expires: database.Expiry(),
		},
		DatabaseMetadata: apievents.DatabaseMetadata{
			DatabaseProtocol:             database.GetProtocol(),
			DatabaseURI:                  database.GetURI(),
			DatabaseLabels:               database.GetStaticLabels(),
			DatabaseAWSRegion:            database.GetAWS().Region,
			DatabaseAWSRedshiftClusterID: database.GetAWS().Redshift.ClusterID,
			DatabaseGCPProjectID:         database.GetGCP().ProjectID,
			DatabaseGCPInstanceID:        database.GetGCP().InstanceID,
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit database create event.")
	}
	return nil
}

// UpdateDatabase updates an existing database resource.
func (a *Server) UpdateDatabase(ctx context.Context, database types.Database) error {
	if err := a.Services.UpdateDatabase(ctx, database); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.DatabaseUpdate{
		Metadata: apievents.Metadata{
			Type: events.DatabaseUpdateEvent,
			Code: events.DatabaseUpdateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    database.GetName(),
			Expires: database.Expiry(),
		},
		DatabaseMetadata: apievents.DatabaseMetadata{
			DatabaseProtocol:             database.GetProtocol(),
			DatabaseURI:                  database.GetURI(),
			DatabaseLabels:               database.GetStaticLabels(),
			DatabaseAWSRegion:            database.GetAWS().Region,
			DatabaseAWSRedshiftClusterID: database.GetAWS().Redshift.ClusterID,
			DatabaseGCPProjectID:         database.GetGCP().ProjectID,
			DatabaseGCPInstanceID:        database.GetGCP().InstanceID,
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit database update event.")
	}
	return nil
}

// DeleteDatabase deletes a database resource.
func (a *Server) DeleteDatabase(ctx context.Context, name string) error {
	if err := a.Services.DeleteDatabase(ctx, name); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.DatabaseDelete{
		Metadata: apievents.Metadata{
			Type: events.DatabaseDeleteEvent,
			Code: events.DatabaseDeleteCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name: name,
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit database delete event.")
	}
	return nil
}

// ListResources returns paginated resources depending on the resource type..
func (a *Server) ListResources(ctx context.Context, req proto.ListResourcesRequest) (*types.ListResourcesResponse, error) {
	// Because WindowsDesktopService does not contain the desktop resources,
	// this is not implemented at the cache level and requires the workaround
	// here in order to support KindWindowsDesktop for ListResources.
	if req.ResourceType == types.KindWindowsDesktop {
		wResp, err := a.ListWindowsDesktops(ctx, types.ListWindowsDesktopsRequest{
			WindowsDesktopFilter: req.WindowsDesktopFilter,
			Limit:                int(req.Limit),
			StartKey:             req.StartKey,
			PredicateExpression:  req.PredicateExpression,
			Labels:               req.Labels,
			SearchKeywords:       req.SearchKeywords,
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return &types.ListResourcesResponse{
			Resources: types.WindowsDesktops(wResp.Desktops).AsResources(),
			NextKey:   wResp.NextKey,
		}, nil
	}
	if req.ResourceType == types.KindWindowsDesktopService {
		wResp, err := a.ListWindowsDesktopServices(ctx, types.ListWindowsDesktopServicesRequest{
			Limit:               int(req.Limit),
			StartKey:            req.StartKey,
			PredicateExpression: req.PredicateExpression,
			Labels:              req.Labels,
			SearchKeywords:      req.SearchKeywords,
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return &types.ListResourcesResponse{
			Resources: types.WindowsDesktopServices(wResp.DesktopServices).AsResources(),
			NextKey:   wResp.NextKey,
		}, nil
	}
	return a.Cache.ListResources(ctx, req)
}

// CreateKubernetesCluster creates a new kubernetes cluster resource.
func (a *Server) CreateKubernetesCluster(ctx context.Context, kubeCluster types.KubeCluster) error {
	if err := a.Services.CreateKubernetesCluster(ctx, kubeCluster); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.KubernetesClusterCreate{
		Metadata: apievents.Metadata{
			Type: events.KubernetesClusterCreateEvent,
			Code: events.KubernetesClusterCreateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    kubeCluster.GetName(),
			Expires: kubeCluster.Expiry(),
		},
		KubeClusterMetadata: apievents.KubeClusterMetadata{
			KubeLabels: kubeCluster.GetStaticLabels(),
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit kube cluster create event.")
	}
	return nil
}

// UpdateKubernetesCluster updates an existing kubernetes cluster resource.
func (a *Server) UpdateKubernetesCluster(ctx context.Context, kubeCluster types.KubeCluster) error {
	if err := a.Kubernetes.UpdateKubernetesCluster(ctx, kubeCluster); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.KubernetesClusterUpdate{
		Metadata: apievents.Metadata{
			Type: events.KubernetesClusterUpdateEvent,
			Code: events.KubernetesClusterUpdateCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name:    kubeCluster.GetName(),
			Expires: kubeCluster.Expiry(),
		},
		KubeClusterMetadata: apievents.KubeClusterMetadata{
			KubeLabels: kubeCluster.GetStaticLabels(),
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit kube cluster update event.")
	}
	return nil
}

// DeleteKubernetesCluster deletes a kubernetes cluster resource.
func (a *Server) DeleteKubernetesCluster(ctx context.Context, name string) error {
	if err := a.Kubernetes.DeleteKubernetesCluster(ctx, name); err != nil {
		return trace.Wrap(err)
	}
	if err := a.emitter.EmitAuditEvent(ctx, &apievents.KubernetesClusterDelete{
		Metadata: apievents.Metadata{
			Type: events.KubernetesClusterDeleteEvent,
			Code: events.KubernetesClusterDeleteCode,
		},
		UserMetadata: ClientUserMetadata(ctx),
		ResourceMetadata: apievents.ResourceMetadata{
			Name: name,
		},
	}); err != nil {
		log.WithError(err).Warn("Failed to emit kube cluster delete event.")
	}
	return nil
}

// SubmitUsageEvent submits an external usage event.
func (a *Server) SubmitUsageEvent(ctx context.Context, req *proto.SubmitUsageEventRequest) error {
	username, err := GetClientUsername(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	event, err := services.ConvertUsageEvent(req.GetEvent(), username)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := a.SubmitAnonymizedUsageEvents(event); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (a *Server) isMFARequired(ctx context.Context, checker services.AccessChecker, req *proto.IsMFARequiredRequest) (*proto.IsMFARequiredResponse, error) {
	pref, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch params := checker.MFAParams(pref.GetRequireMFAType()); params.Required {
	case services.MFARequiredAlways:
		return &proto.IsMFARequiredResponse{Required: true}, nil
	case services.MFARequiredNever:
		return &proto.IsMFARequiredResponse{Required: false}, nil
	}

	var noMFAAccessErr, notFoundErr error
	switch t := req.Target.(type) {
	case *proto.IsMFARequiredRequest_Node:
		if t.Node.Node == "" {
			return nil, trace.BadParameter("empty Node field")
		}
		if t.Node.Login == "" {
			return nil, trace.BadParameter("empty Login field")
		}
		// Find the target node and check whether MFA is required.
		nodes, err := a.GetNodes(ctx, apidefaults.Namespace)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		var matches []types.Server
		for _, n := range nodes {
			// Get the server address without port number.
			addr, _, err := net.SplitHostPort(n.GetAddr())
			if err != nil {
				addr = n.GetAddr()
			}
			// Match NodeName to UUID, hostname or self-reported server address.
			if n.GetName() == t.Node.Node || n.GetHostname() == t.Node.Node || addr == t.Node.Node {
				matches = append(matches, n)
			}
		}
		if len(matches) == 0 {
			// If t.Node.Node is not a known registered node, it may be an
			// unregistered host running OpenSSH with a certificate created via
			// `tctl auth sign`. In these cases, let the user through without
			// extra checks.
			//
			// If t.Node.Node turns out to be an alias for a real node (e.g.
			// private network IP), and MFA check was actually required, the
			// Node itself will check the cert extensions and reject the
			// connection.
			return &proto.IsMFARequiredResponse{Required: false}, nil
		}
		// Check RBAC against all matching nodes and return the first error.
		// If at least one node requires MFA, we'll catch it.
		for _, n := range matches {
			err := checker.CheckAccess(
				n,
				services.AccessMFAParams{},
				services.NewLoginMatcher(t.Node.Login),
			)

			// Ignore other errors; they'll be caught on the real access attempt.
			if err != nil && errors.Is(err, services.ErrSessionMFARequired) {
				noMFAAccessErr = err
				break
			}
		}

	case *proto.IsMFARequiredRequest_KubernetesCluster:
		notFoundErr = trace.NotFound("kubernetes cluster %q not found", t.KubernetesCluster)
		if t.KubernetesCluster == "" {
			return nil, trace.BadParameter("missing KubernetesCluster field in a kubernetes-only UserCertsRequest")
		}
		// Find the target cluster and check whether MFA is required.
		svcs, err := a.GetKubernetesServers(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		var cluster types.KubeCluster
		for _, svc := range svcs {
			kubeCluster := svc.GetCluster()
			if kubeCluster.GetName() == t.KubernetesCluster {
				cluster = kubeCluster
				break
			}
		}
		if cluster == nil {
			return nil, trace.Wrap(notFoundErr)
		}

		noMFAAccessErr = checker.CheckAccess(cluster, services.AccessMFAParams{})

	case *proto.IsMFARequiredRequest_Database:
		notFoundErr = trace.NotFound("database service %q not found", t.Database.ServiceName)
		if t.Database.ServiceName == "" {
			return nil, trace.BadParameter("missing ServiceName field in a database-only UserCertsRequest")
		}
		servers, err := a.GetDatabaseServers(ctx, apidefaults.Namespace)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		var db types.Database
		for _, server := range servers {
			if server.GetDatabase().GetName() == t.Database.ServiceName {
				db = server.GetDatabase()
				break
			}
		}
		if db == nil {
			return nil, trace.Wrap(notFoundErr)
		}

		dbRoleMatchers := role.DatabaseRoleMatchers(
			db.GetProtocol(),
			t.Database.Username,
			t.Database.GetDatabase(),
		)
		noMFAAccessErr = checker.CheckAccess(
			db,
			services.AccessMFAParams{},
			dbRoleMatchers...,
		)
	case *proto.IsMFARequiredRequest_WindowsDesktop:
		desktops, err := a.GetWindowsDesktops(ctx, types.WindowsDesktopFilter{Name: t.WindowsDesktop.GetWindowsDesktop()})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if len(desktops) == 0 {
			return nil, trace.NotFound("windows desktop %q not found", t.WindowsDesktop.GetWindowsDesktop())
		}

		noMFAAccessErr = checker.CheckAccess(desktops[0],
			services.AccessMFAParams{},
			services.NewWindowsLoginMatcher(t.WindowsDesktop.GetLogin()))

	default:
		return nil, trace.BadParameter("unknown Target %T", req.Target)
	}
	// No error means that MFA is not required for this resource by
	// AccessChecker.
	if noMFAAccessErr == nil {
		return &proto.IsMFARequiredResponse{Required: false}, nil
	}
	// Errors other than ErrSessionMFARequired mean something else is wrong,
	// most likely access denied.
	if !errors.Is(noMFAAccessErr, services.ErrSessionMFARequired) {
		if !trace.IsAccessDenied(noMFAAccessErr) {
			log.WithError(noMFAAccessErr).Warn("Could not determine MFA access")
		}

		// Mask the access denied errors by returning false to prevent resource
		// name oracles. Auth will be denied (and generate an audit log entry)
		// when the client attempts to connect.
		return &proto.IsMFARequiredResponse{Required: false}, nil
	}
	// If we reach here, the error from AccessChecker was
	// ErrSessionMFARequired.

	return &proto.IsMFARequiredResponse{Required: true}, nil
}

// mfaAuthChallenge constructs an MFAAuthenticateChallenge for all MFA devices
// registered by the user.
func (a *Server) mfaAuthChallenge(ctx context.Context, user string, passwordless bool) (*proto.MFAAuthenticateChallenge, error) {
	// Check what kind of MFA is enabled.
	apref, err := a.GetAuthPreference(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	enableTOTP := apref.IsSecondFactorTOTPAllowed()
	enableWebauthn := apref.IsSecondFactorWebauthnAllowed()

	// Fetch configurations. The IsSecondFactor*Allowed calls above already
	// include the necessary checks of config empty, disabled, etc.
	var u2fPref *types.U2F
	switch val, err := apref.GetU2F(); {
	case trace.IsNotFound(err): // OK, may happen.
	case err != nil: // NOK, unexpected.
		return nil, trace.Wrap(err)
	default:
		u2fPref = val
	}
	var webConfig *types.Webauthn
	switch val, err := apref.GetWebauthn(); {
	case trace.IsNotFound(err): // OK, may happen.
	case err != nil: // NOK, unexpected.
		return nil, trace.Wrap(err)
	default:
		webConfig = val
	}

	// Handle passwordless separately, it works differently from MFA.
	if passwordless {
		if !enableWebauthn {
			return nil, trace.BadParameter("passwordless requires WebAuthn")
		}
		webLogin := &wanlib.PasswordlessFlow{
			Webauthn: webConfig,
			Identity: a.Services,
		}
		assertion, err := webLogin.Begin(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return &proto.MFAAuthenticateChallenge{
			WebauthnChallenge: wanlib.CredentialAssertionToProto(assertion),
		}, nil
	}

	// User required for non-passwordless.
	if user == "" {
		return nil, trace.BadParameter("user required")
	}

	devs, err := a.Services.GetMFADevices(ctx, user, true /* withSecrets */)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	groupedDevs := groupByDeviceType(devs, enableWebauthn)
	challenge := &proto.MFAAuthenticateChallenge{}

	// TOTP challenge.
	if enableTOTP && groupedDevs.TOTP {
		challenge.TOTP = &proto.TOTPChallenge{}
	}

	// WebAuthn challenge.
	if len(groupedDevs.Webauthn) > 0 {
		webLogin := &wanlib.LoginFlow{
			U2F:      u2fPref,
			Webauthn: webConfig,
			Identity: wanlib.WithDevices(a.Services, groupedDevs.Webauthn),
		}
		assertion, err := webLogin.Begin(ctx, user)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		challenge.WebauthnChallenge = wanlib.CredentialAssertionToProto(assertion)
	}

	return challenge, nil
}

type devicesByType struct {
	TOTP     bool
	Webauthn []*types.MFADevice
}

func groupByDeviceType(devs []*types.MFADevice, groupWebauthn bool) devicesByType {
	res := devicesByType{}
	for _, dev := range devs {
		switch dev.Device.(type) {
		case *types.MFADevice_Totp:
			res.TOTP = true
		case *types.MFADevice_U2F:
			if groupWebauthn {
				res.Webauthn = append(res.Webauthn, dev)
			}
		case *types.MFADevice_Webauthn:
			if groupWebauthn {
				res.Webauthn = append(res.Webauthn, dev)
			}
		default:
			log.Warningf("Skipping MFA device of unknown type %T.", dev.Device)
		}
	}
	return res
}

// validateMFAAuthResponse validates an MFA or passwordless challenge.
// Returns the device used to solve the challenge (if applicable) and the
// username.
func (a *Server) validateMFAAuthResponse(
	ctx context.Context,
	resp *proto.MFAAuthenticateResponse, user string, passwordless bool,
) (*types.MFADevice, string, error) {
	// Sanity check user/passwordless.
	if user == "" && !passwordless {
		return nil, "", trace.BadParameter("user required")
	}

	switch res := resp.Response.(type) {
	// cases in order of preference
	case *proto.MFAAuthenticateResponse_Webauthn:
		// Read necessary configurations.
		cap, err := a.GetAuthPreference(ctx)
		if err != nil {
			return nil, "", trace.Wrap(err)
		}
		u2f, err := cap.GetU2F()
		switch {
		case trace.IsNotFound(err): // OK, may happen.
		case err != nil: // Unexpected.
			return nil, "", trace.Wrap(err)
		}
		webConfig, err := cap.GetWebauthn()
		if err != nil {
			return nil, "", trace.Wrap(err)
		}

		assertionResp := wanlib.CredentialAssertionResponseFromProto(res.Webauthn)
		var dev *types.MFADevice
		if passwordless {
			webLogin := &wanlib.PasswordlessFlow{
				Webauthn: webConfig,
				Identity: a.Services,
			}
			dev, user, err = webLogin.Finish(ctx, assertionResp)
		} else {
			webLogin := &wanlib.LoginFlow{
				U2F:      u2f,
				Webauthn: webConfig,
				Identity: a.Services,
			}
			dev, err = webLogin.Finish(ctx, user, wanlib.CredentialAssertionResponseFromProto(res.Webauthn))
		}
		if err != nil {
			return nil, "", trace.AccessDenied("MFA response validation failed: %v", err)
		}
		return dev, user, nil

	case *proto.MFAAuthenticateResponse_TOTP:
		dev, err := a.checkOTP(user, res.TOTP.Code)
		return dev, user, trace.Wrap(err)

	default:
		return nil, "", trace.BadParameter("unknown or missing MFAAuthenticateResponse type %T", resp.Response)
	}
}

func (a *Server) upsertWebSession(ctx context.Context, user string, session types.WebSession) error {
	if err := a.WebSessions().Upsert(ctx, session); err != nil {
		return trace.Wrap(err)
	}
	token, err := types.NewWebToken(session.GetBearerTokenExpiryTime(), types.WebTokenSpecV3{
		User:  session.GetUser(),
		Token: session.GetBearerToken(),
	})
	if err != nil {
		return trace.Wrap(err)
	}
	if err := a.WebTokens().Upsert(ctx, token); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func mergeKeySets(a, b types.CAKeySet) types.CAKeySet {
	newKeySet := a.Clone()
	newKeySet.SSH = append(newKeySet.SSH, b.SSH...)
	newKeySet.TLS = append(newKeySet.TLS, b.TLS...)
	newKeySet.JWT = append(newKeySet.JWT, b.JWT...)
	return newKeySet
}

// addAdditionalTrustedKeysAtomic performs an atomic CompareAndSwap to update
// the given CA with newKeys added to the AdditionalTrustedKeys
func (a *Server) addAdditionalTrustedKeysAtomic(
	ctx context.Context,
	currentCA types.CertAuthority,
	newKeys types.CAKeySet,
	needsUpdate func(types.CertAuthority) (bool, error),
) error {
	for {
		select {
		case <-ctx.Done():
			return trace.Wrap(ctx.Err())
		default:
		}
		updateRequired, err := needsUpdate(currentCA)
		if err != nil {
			return trace.Wrap(err)
		}
		if !updateRequired {
			return nil
		}

		newCA := currentCA.Clone()
		currentKeySet := newCA.GetAdditionalTrustedKeys()
		mergedKeySet := mergeKeySets(currentKeySet, newKeys)
		if err := newCA.SetAdditionalTrustedKeys(mergedKeySet); err != nil {
			return trace.Wrap(err)
		}

		err = a.CompareAndSwapCertAuthority(newCA, currentCA)
		if err != nil && !trace.IsCompareFailed(err) {
			return trace.Wrap(err)
		}
		if err == nil {
			// success!
			return nil
		}
		// else trace.IsCompareFailed(err) == true (CA was concurrently updated)

		currentCA, err = a.Services.GetCertAuthority(ctx, currentCA.GetID(), true)
		if err != nil {
			return trace.Wrap(err)
		}
	}
}

// newKeySet generates a new sets of keys for a given CA type.
// Keep this function in sync with lib/service/suite/suite.go:NewTestCAWithConfig().
func newKeySet(ctx context.Context, keyStore *keystore.Manager, caID types.CertAuthID) (types.CAKeySet, error) {
	var keySet types.CAKeySet
	switch caID.Type {
	case types.UserCA, types.HostCA:
		sshKeyPair, err := keyStore.NewSSHKeyPair(ctx)
		if err != nil {
			return keySet, trace.Wrap(err)
		}
		tlsKeyPair, err := keyStore.NewTLSKeyPair(ctx, caID.DomainName)
		if err != nil {
			return keySet, trace.Wrap(err)
		}
		keySet.SSH = append(keySet.SSH, sshKeyPair)
		keySet.TLS = append(keySet.TLS, tlsKeyPair)
	case types.DatabaseCA:
		// Database CA only contains TLS cert.
		tlsKeyPair, err := keyStore.NewTLSKeyPair(ctx, caID.DomainName)
		if err != nil {
			return keySet, trace.Wrap(err)
		}
		keySet.TLS = append(keySet.TLS, tlsKeyPair)
	case types.JWTSigner:
		jwtKeyPair, err := keyStore.NewJWTKeyPair(ctx)
		if err != nil {
			return keySet, trace.Wrap(err)
		}
		keySet.JWT = append(keySet.JWT, jwtKeyPair)
	default:
		return keySet, trace.BadParameter("unknown ca type: %s", caID.Type)
	}
	return keySet, nil
}

// ensureLocalAdditionalKeys adds additional trusted keys to the CA if they are not
// already present.
func (a *Server) ensureLocalAdditionalKeys(ctx context.Context, ca types.CertAuthority) error {
	hasUsableKeys, err := a.keyStore.HasUsableAdditionalKeys(ctx, ca)
	if err != nil {
		return trace.Wrap(err)
	}
	if hasUsableKeys {
		// nothing to do
		return nil
	}

	newKeySet, err := newKeySet(ctx, a.keyStore, ca.GetID())
	if err != nil {
		return trace.Wrap(err)
	}

	// The CA still needs an update while the keystore does not have any usable
	// keys in the CA.
	needsUpdate := func(ca types.CertAuthority) (bool, error) {
		hasUsableKeys, err := a.keyStore.HasUsableAdditionalKeys(ctx, ca)
		return !hasUsableKeys, trace.Wrap(err)
	}
	err = a.addAdditionalTrustedKeysAtomic(ctx, ca, newKeySet, needsUpdate)
	if err != nil {
		return trace.Wrap(err)
	}
	log.Infof("Successfully added locally usable additional trusted keys to %s CA.", ca.GetType())
	return nil
}

// createSelfSignedCA creates a new self-signed CA and writes it to the
// backend, with the type and clusterName given by the argument caID.
func (a *Server) createSelfSignedCA(ctx context.Context, caID types.CertAuthID) error {
	keySet, err := newKeySet(ctx, a.keyStore, caID)
	if err != nil {
		return trace.Wrap(err)
	}
	ca, err := types.NewCertAuthority(types.CertAuthoritySpecV2{
		Type:        caID.Type,
		ClusterName: caID.DomainName,
		ActiveKeys:  keySet,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	if err := a.CreateCertAuthority(ca); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// deleteUnusedKeys deletes all teleport keys held in a connected HSM for this
// auth server which are not currently used in any CAs.
func (a *Server) deleteUnusedKeys(ctx context.Context) error {
	clusterName, err := a.Services.GetClusterName()
	if err != nil {
		return trace.Wrap(err)
	}

	var usedKeys [][]byte
	for _, caType := range types.CertAuthTypes {
		caID := types.CertAuthID{Type: caType, DomainName: clusterName.GetClusterName()}
		ca, err := a.Services.GetCertAuthority(ctx, caID, true)
		if err != nil {
			return trace.Wrap(err)
		}
		for _, keySet := range []types.CAKeySet{ca.GetActiveKeys(), ca.GetAdditionalTrustedKeys()} {
			for _, sshKeyPair := range keySet.SSH {
				usedKeys = append(usedKeys, sshKeyPair.PrivateKey)
			}
			for _, tlsKeyPair := range keySet.TLS {
				usedKeys = append(usedKeys, tlsKeyPair.Key)
			}
			for _, jwtKeyPair := range keySet.JWT {
				usedKeys = append(usedKeys, jwtKeyPair.PrivateKey)
			}
		}
	}
	return trace.Wrap(a.keyStore.DeleteUnusedKeys(ctx, usedKeys))
}

// authKeepAliver is a keep aliver using auth server directly
type authKeepAliver struct {
	sync.RWMutex
	a           *Server
	ctx         context.Context
	cancel      context.CancelFunc
	keepAlivesC chan types.KeepAlive
	err         error
}

// KeepAlives returns a channel accepting keep alive requests
func (k *authKeepAliver) KeepAlives() chan<- types.KeepAlive {
	return k.keepAlivesC
}

func (k *authKeepAliver) forwardKeepAlives() {
	for {
		select {
		case <-k.a.closeCtx.Done():
			k.Close()
			return
		case <-k.ctx.Done():
			return
		case keepAlive := <-k.keepAlivesC:
			err := k.a.KeepAliveServer(k.ctx, keepAlive)
			if err != nil {
				k.closeWithError(err)
				return
			}
		}
	}
}

func (k *authKeepAliver) closeWithError(err error) {
	k.Close()
	k.Lock()
	defer k.Unlock()
	k.err = err
}

// Error returns the error if keep aliver
// has been closed
func (k *authKeepAliver) Error() error {
	k.RLock()
	defer k.RUnlock()
	return k.err
}

// Done returns channel that is closed whenever
// keep aliver is closed
func (k *authKeepAliver) Done() <-chan struct{} {
	return k.ctx.Done()
}

// Close closes keep aliver and cancels all goroutines
func (k *authKeepAliver) Close() error {
	k.cancel()
	return nil
}

const (
	// BearerTokenTTL specifies standard bearer token to exist before
	// it has to be renewed by the client
	BearerTokenTTL = 10 * time.Minute

	// TokenLenBytes is len in bytes of the invite token
	TokenLenBytes = 16

	// RecoveryTokenLenBytes is len in bytes of a user token for recovery.
	RecoveryTokenLenBytes = 32

	// SessionTokenBytes is the number of bytes of a web or application session.
	SessionTokenBytes = 32
)

// oidcClient is internal structure that stores OIDC client and its config
type oidcClient struct {
	client    *oidc.Client
	connector types.OIDCConnector
	// syncCtx controls the provider sync goroutine.
	syncCtx    context.Context
	syncCancel context.CancelFunc
	// firstSync will be closed once the first provider sync succeeds
	firstSync chan struct{}
}

// samlProvider is internal structure that stores SAML client and its config
type samlProvider struct {
	provider  *saml2.SAMLServiceProvider
	connector types.SAMLConnector
}

// githubClient is internal structure that stores Github OAuth 2client and its config
type githubClient struct {
	client *oauth2.Client
	config oauth2.Config
}

// oauth2ConfigsEqual returns true if the provided OAuth2 configs are equal
func oauth2ConfigsEqual(a, b oauth2.Config) bool {
	if a.Credentials.ID != b.Credentials.ID {
		return false
	}
	if a.Credentials.Secret != b.Credentials.Secret {
		return false
	}
	if a.RedirectURL != b.RedirectURL {
		return false
	}
	if len(a.Scope) != len(b.Scope) {
		return false
	}
	for i := range a.Scope {
		if a.Scope[i] != b.Scope[i] {
			return false
		}
	}
	if a.AuthURL != b.AuthURL {
		return false
	}
	if a.TokenURL != b.TokenURL {
		return false
	}
	if a.AuthMethod != b.AuthMethod {
		return false
	}
	return true
}

// isHTTPS checks if the scheme for a URL is https or not.
func isHTTPS(u string) error {
	earl, err := url.Parse(u)
	if err != nil {
		return trace.Wrap(err)
	}
	if earl.Scheme != "https" {
		return trace.BadParameter("expected scheme https, got %q", earl.Scheme)
	}

	return nil
}

// WithClusterCAs returns a TLS hello callback that returns a copy of the provided
// TLS config with client CAs pool of the specified cluster.
func WithClusterCAs(tlsConfig *tls.Config, ap AccessCache, currentClusterName string, log logrus.FieldLogger) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		var clusterName string
		var err error
		if info.ServerName != "" {
			// Newer clients will set SNI that encodes the cluster name.
			clusterName, err = apiutils.DecodeClusterName(info.ServerName)
			if err != nil {
				if !trace.IsNotFound(err) {
					log.Debugf("Ignoring unsupported cluster name name %q.", info.ServerName)
					clusterName = ""
				}
			}
		}
		pool, totalSubjectsLen, err := DefaultClientCertPool(ap, clusterName)
		if err != nil {
			log.WithError(err).Errorf("Failed to retrieve client pool for %q.", clusterName)
			// this falls back to the default config
			return nil, nil
		}

		// Per https://tools.ietf.org/html/rfc5246#section-7.4.4 the total size of
		// the known CA subjects sent to the client can't exceed 2^16-1 (due to
		// 2-byte length encoding). The crypto/tls stack will panic if this
		// happens.
		//
		// This usually happens on the root cluster with a very large (>500) number
		// of leaf clusters. In these cases, the client cert will be signed by the
		// current (root) cluster.
		//
		// If the number of CAs turns out too large for the handshake, drop all but
		// the current cluster CA. In the unlikely case where it's wrong, the
		// client will be rejected.
		if totalSubjectsLen >= int64(math.MaxUint16) {
			log.Debugf("Number of CAs in client cert pool is too large and cannot be encoded in a TLS handshake; this is due to a large number of trusted clusters; will use only the CA of the current cluster to validate.")

			pool, _, err = DefaultClientCertPool(ap, currentClusterName)
			if err != nil {
				log.WithError(err).Errorf("Failed to retrieve client pool for %q.", currentClusterName)
				// this falls back to the default config
				return nil, nil
			}
		}
		tlsCopy := tlsConfig.Clone()
		tlsCopy.ClientCAs = pool
		return tlsCopy, nil
	}
}

// DefaultDNSNamesForRole returns default DNS names for the specified role.
func DefaultDNSNamesForRole(role types.SystemRole) []string {
	if (types.SystemRoles{role}).IncludeAny(types.RoleAuth, types.RoleAdmin, types.RoleProxy, types.RoleKube, types.RoleApp, types.RoleDatabase, types.RoleWindowsDesktop) {
		return []string{
			"*." + constants.APIDomain,
			constants.APIDomain,
		}
	}
	return nil
}
