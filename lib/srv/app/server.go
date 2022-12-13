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

// app package runs the application proxy process. It keeps dynamic labels
// updated, heart beats its presence, checks access controls, and forwards
// connections between the tunnel and the target host.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/events"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/httplib"
	"github.com/zmb3/teleport/lib/labels"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv"
	appaws "github.com/zmb3/teleport/lib/srv/app/aws"
	"github.com/zmb3/teleport/lib/srv/app/common"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/aws"
)

type appServerContextKey string

const (
	connContextKey appServerContextKey = "teleport-connContextKey"
)

// Config is the configuration for an application server.
type Config struct {
	// Clock is used to control time.
	Clock clockwork.Clock

	// DataDir is the path to the data directory for the server.
	DataDir string

	// AuthClient is a client directly connected to the Auth server.
	AuthClient *auth.Client

	// AccessPoint is a caching client connected to the Auth Server.
	AccessPoint auth.AppsAccessPoint

	// TLSConfig is the *tls.Config for this server.
	TLSConfig *tls.Config

	// CipherSuites is the list of TLS cipher suites that have been configured
	// for this process.
	CipherSuites []uint16

	// Hostname is the hostname where this application agent is running.
	Hostname string

	// HostID is the id of the host where this application agent is running.
	HostID string

	// Authorizer is used to authorize requests.
	Authorizer auth.Authorizer

	// GetRotation returns the certificate rotation state.
	GetRotation services.RotationGetter

	// Apps is a list of statically registered apps this agent proxies.
	Apps types.Apps

	// CloudLabels is a service that imports labels from a cloud provider. The labels are shared
	// between all apps.
	CloudLabels labels.Importer

	// OnHeartbeat is called after every heartbeat. Used to update process state.
	OnHeartbeat func(error)

	// Cloud provides cloud provider access related functionality.
	Cloud Cloud

	// ResourceMatchers is a list of app resource matchers.
	ResourceMatchers []services.ResourceMatcher

	// OnReconcile is called after each database resource reconciliation.
	OnReconcile func(types.Apps)

	// ConnectedProxyGetter gets the proxies teleport is connected to.
	ConnectedProxyGetter *reversetunnel.ConnectedProxyGetter

	// LockWatcher is the lock watcher for app access targets.
	LockWatcher *services.LockWatcher

	// Emitter is an event emitter.
	Emitter events.Emitter

	// MonitorCloseChannel will be signaled when the monitor closes a connection.
	// Used only for testing. Optional.
	MonitorCloseChannel chan struct{}
}

// CheckAndSetDefaults makes sure the configuration has the minimum required
// to function.
func (c *Config) CheckAndSetDefaults() error {
	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}

	if c.DataDir == "" {
		return trace.BadParameter("data dir missing")
	}
	if c.AuthClient == nil {
		return trace.BadParameter("auth client log missing")
	}
	if c.AccessPoint == nil {
		return trace.BadParameter("access point missing")
	}
	if c.TLSConfig == nil {
		return trace.BadParameter("tls config missing")
	}
	if len(c.CipherSuites) == 0 {
		return trace.BadParameter("cipersuites missing")
	}
	if c.Hostname == "" {
		return trace.BadParameter("hostname missing")
	}
	if c.HostID == "" {
		return trace.BadParameter("host id missing")
	}
	if c.Authorizer == nil {
		return trace.BadParameter("authorizer missing")
	}
	if c.GetRotation == nil {
		return trace.BadParameter("rotation getter missing")
	}
	if c.OnHeartbeat == nil {
		return trace.BadParameter("heartbeat missing")
	}
	if c.Cloud == nil {
		cloud, err := NewCloud(CloudConfig{})
		if err != nil {
			return trace.Wrap(err)
		}
		c.Cloud = cloud
	}
	if c.ConnectedProxyGetter == nil {
		c.ConnectedProxyGetter = reversetunnel.NewConnectedProxyGetter()
	}

	return nil
}

// Server is an application server. It authenticates requests from the web
// proxy and forwards them to internal applications.
type Server struct {
	c   *Config
	log *logrus.Entry

	closeContext context.Context
	closeFunc    context.CancelFunc

	httpServer *http.Server
	tcpServer  *tcpServer
	tlsConfig  *tls.Config

	mu            sync.RWMutex
	heartbeats    map[string]*srv.Heartbeat
	dynamicLabels map[string]*labels.Dynamic

	connAuthMu sync.Mutex
	// connAuth is used to map an initial failure of authorization to a connection.
	// This will force the HTTP server to serve an error and close the connection.
	connAuth map[net.Conn]error

	// apps are all apps this server currently proxies. Proxied apps are
	// reconciled against monitoredApps below.
	apps map[string]types.Application
	// monitoredApps contains all cluster apps the proxied apps are
	// reconciled against.
	monitoredApps monitoredApps
	// reconcileCh triggers reconciliation of proxied apps.
	reconcileCh chan struct{}

	proxyPort string

	cache *sessionChunkCache

	awsSigner *appaws.SigningService

	// watcher monitors changes to application resources.
	watcher *services.AppWatcher

	// authMiddleware allows wrapping connections with identity information.
	authMiddleware *auth.Middleware
}

// monitoredApps is a collection of applications from different sources
// like configuration file and dynamic resources.
//
// It's updated by respective watchers and is used for reconciling with the
// currently proxied apps.
type monitoredApps struct {
	// static are apps from the agent's YAML configuration.
	static types.Apps
	// resources are apps created via CLI or API.
	resources types.Apps
	// mu protects access to the fields.
	mu sync.Mutex
}

func (m *monitoredApps) setResources(apps types.Apps) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resources = apps
}

func (m *monitoredApps) get() types.ResourcesWithLabelsMap {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append(m.static, m.resources...).AsResources().ToMap()
}

// New returns a new application server.
func New(ctx context.Context, c *Config) (*Server, error) {
	err := c.CheckAndSetDefaults()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	awsSigner, err := appaws.NewSigningService(appaws.SigningServiceConfig{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	s := &Server{
		c: c,
		log: logrus.WithFields(logrus.Fields{
			trace.Component: teleport.ComponentApp,
		}),
		heartbeats:    make(map[string]*srv.Heartbeat),
		dynamicLabels: make(map[string]*labels.Dynamic),
		apps:          make(map[string]types.Application),
		connAuth:      make(map[net.Conn]error),
		awsSigner:     awsSigner,
		monitoredApps: monitoredApps{
			static: c.Apps,
		},
		reconcileCh: make(chan struct{}),
	}

	s.closeContext, s.closeFunc = context.WithCancel(ctx)

	// Make copy of server's TLS configuration and update it with the specific
	// functionality this server needs, like requiring client certificates.
	s.tlsConfig = copyAndConfigureTLS(s.c.TLSConfig, s.getConfigForClient)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Create and configure HTTP server with authorizing middleware.
	s.httpServer = s.newHTTPServer()

	// TCP server will handle TCP applications.
	s.tcpServer = s.newTCPServer()

	// Create a new session cache, this holds sessions that can be used to
	// forward requests.
	s.cache, err = s.newSessionChunkCache()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Figure out the port the proxy is running on.
	s.proxyPort = s.getProxyPort()

	return s, nil
}

// startApp registers the specified application.
func (s *Server) startApp(ctx context.Context, app types.Application) error {
	// Start a goroutine that will be updating apps's command labels (if any)
	// on the defined schedule.
	if err := s.startDynamicLabels(ctx, app); err != nil {
		return trace.Wrap(err)
	}
	// Heartbeat will periodically report the presence of this proxied app
	// to the auth server.
	if err := s.startHeartbeat(ctx, app); err != nil {
		return trace.Wrap(err)
	}
	s.log.Debugf("Started %v.", app)
	return nil
}

// stopApp uninitializes the app with the specified name.
func (s *Server) stopApp(ctx context.Context, name string) error {
	s.stopDynamicLabels(name)
	if err := s.stopHeartbeat(name); err != nil {
		return trace.Wrap(err)
	}
	// Heartbeat is stopped but if we don't remove this app server,
	// it can linger for up to ~10m until its TTL expires.
	if err := s.removeAppServer(ctx, name); err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	s.log.Debugf("Stopped app %q.", name)
	return nil
}

// removeAppServer deletes app server for the specified app.
func (s *Server) removeAppServer(ctx context.Context, name string) error {
	return s.c.AuthClient.DeleteApplicationServer(ctx, apidefaults.Namespace,
		s.c.HostID, name)
}

// startDynamicLabels starts dynamic labels for the app if it has them.
func (s *Server) startDynamicLabels(ctx context.Context, app types.Application) error {
	if len(app.GetDynamicLabels()) == 0 {
		return nil // Nothing to do.
	}
	dynamic, err := labels.NewDynamic(ctx, &labels.DynamicConfig{
		Labels: app.GetDynamicLabels(),
		Log:    s.log,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	dynamic.Sync()
	dynamic.Start()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dynamicLabels[app.GetName()] = dynamic
	return nil
}

// stopDynamicLabels stops dynamic labels for the specified app.
func (s *Server) stopDynamicLabels(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dynamic, ok := s.dynamicLabels[name]
	if !ok {
		return
	}
	delete(s.dynamicLabels, name)
	dynamic.Close()
}

// startHeartbeat starts the registration heartbeat to the auth server.
func (s *Server) startHeartbeat(ctx context.Context, app types.Application) error {
	heartbeat, err := srv.NewHeartbeat(srv.HeartbeatConfig{
		Context:         s.closeContext,
		Component:       teleport.ComponentApp,
		Mode:            srv.HeartbeatModeApp,
		Announcer:       s.c.AccessPoint,
		GetServerInfo:   s.getServerInfoFunc(app),
		KeepAlivePeriod: apidefaults.ServerKeepAliveTTL(),
		AnnouncePeriod:  apidefaults.ServerAnnounceTTL/2 + utils.RandomDuration(apidefaults.ServerAnnounceTTL/10),
		CheckPeriod:     defaults.HeartbeatCheckPeriod,
		ServerTTL:       apidefaults.ServerAnnounceTTL,
		OnHeartbeat:     s.c.OnHeartbeat,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	go heartbeat.Run()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats[app.GetName()] = heartbeat
	return nil
}

// stopHeartbeat stops the heartbeat for the specified app.
func (s *Server) stopHeartbeat(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	heartbeat, ok := s.heartbeats[name]
	if !ok {
		return nil
	}
	delete(s.heartbeats, name)
	return heartbeat.Close()
}

// getServerInfoFunc returns function that the heartbeater uses to report the
// provided application to the auth server.
func (s *Server) getServerInfoFunc(app types.Application) func() (types.Resource, error) {
	return func() (types.Resource, error) {
		return s.getServerInfo(app)
	}
}

// getServerInfo returns up-to-date app resource.
func (s *Server) getServerInfo(app types.Application) (types.Resource, error) {
	// Make sure to return a new object, because it gets cached by
	// heartbeat and will always compare as equal otherwise.
	s.mu.RLock()
	copy := s.appWithUpdatedLabels(app)
	s.mu.RUnlock()
	expires := s.c.Clock.Now().UTC().Add(apidefaults.ServerAnnounceTTL)
	server, err := types.NewAppServerV3(types.Metadata{
		Name:    copy.GetName(),
		Expires: &expires,
	}, types.AppServerSpecV3{
		Version:  teleport.Version,
		Hostname: s.c.Hostname,
		HostID:   s.c.HostID,
		Rotation: s.getRotationState(),
		App:      copy,
		ProxyIDs: s.c.ConnectedProxyGetter.GetProxyIDs(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return server, nil
}

// getRotationState is a helper to return this server's CA rotation state.
func (s *Server) getRotationState() types.Rotation {
	rotation, err := s.c.GetRotation(types.RoleApp)
	if err != nil && !trace.IsNotFound(err) {
		s.log.WithError(err).Warn("Failed to get rotation state.")
	}
	if rotation != nil {
		return *rotation
	}
	return types.Rotation{}
}

// registerApp starts proxying the app.
func (s *Server) registerApp(ctx context.Context, app types.Application) error {
	if err := s.startApp(ctx, app); err != nil {
		return trace.Wrap(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[app.GetName()] = app
	return nil
}

// updateApp updates application that is already registered.
func (s *Server) updateApp(ctx context.Context, app types.Application) error {
	// Stop heartbeat and dynamic labels before starting new ones.
	if err := s.stopApp(ctx, app.GetName()); err != nil {
		return trace.Wrap(err)
	}
	if err := s.registerApp(ctx, app); err != nil {
		// If we failed to re-register, don't keep proxying the old app.
		if errUnregister := s.unregisterApp(ctx, app.GetName()); errUnregister != nil {
			return trace.NewAggregate(err, errUnregister)
		}
		return trace.Wrap(err)
	}
	return nil
}

// unregisterApp stops proxying the app.
func (s *Server) unregisterApp(ctx context.Context, name string) error {
	if err := s.stopApp(ctx, name); err != nil {
		return trace.Wrap(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apps, name)
	return nil
}

// getApps returns a list of all apps this server is proxying.
func (s *Server) getApps() (apps types.Apps) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, app := range s.apps {
		apps = append(apps, app)
	}
	return apps
}

// Start starts proxying all registered apps.
func (s *Server) Start(ctx context.Context) (err error) {
	// Register all apps from static configuration.
	for _, app := range s.c.Apps {
		if err := s.registerApp(ctx, app); err != nil {
			return trace.Wrap(err)
		}
	}

	// Start reconciler that will be reconciling proxied apps with
	// application resources.
	if err := s.startReconciler(ctx); err != nil {
		return trace.Wrap(err)
	}

	// Initialize watcher that will be dynamically (un-)registering
	// proxied apps based on the application resources.
	if s.watcher, err = s.startResourceWatcher(ctx); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// Close will shut the server down and unblock any resources.
func (s *Server) Close() error {
	var errs []error

	// Stop all proxied apps.
	for _, app := range s.getApps() {
		if err := s.unregisterApp(s.closeContext, app.GetName()); err != nil {
			errs = append(errs, err)
		}
	}

	// Stop HTTP server.
	if err := s.httpServer.Close(); err != nil {
		errs = append(errs, err)
	}

	// Close the session cache and its remaining sessions. Sessions
	// use server.closeContext to complete cleanup, so we must wait
	// for sessions to finish closing before closing the context.
	s.cache.closeAllSessions()

	// Signal to any blocking go routine that it should exit.
	s.closeFunc()

	// Stop the database resource watcher.
	if s.watcher != nil {
		s.watcher.Close()
	}

	return trace.NewAggregate(errs...)
}

// Wait will block while the server is running.
func (s *Server) Wait() error {
	<-s.closeContext.Done()
	return s.closeContext.Err()
}

// ForceHeartbeat is used in tests to force updating of app servers.
func (s *Server) ForceHeartbeat() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, heartbeat := range s.heartbeats {
		s.log.Debugf("Forcing heartbeat for %q.", name)
		if err := heartbeat.ForceSend(time.Second); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

func (s *Server) getAndDeleteConnAuth(conn net.Conn) error {
	s.connAuthMu.Lock()
	defer s.connAuthMu.Unlock()
	err := s.connAuth[conn]
	delete(s.connAuth, conn)
	return err
}

func (s *Server) setConnAuth(conn net.Conn, err error) {
	s.connAuthMu.Lock()
	defer s.connAuthMu.Unlock()
	s.connAuth[conn] = err
}

func (s *Server) deleteConnAuth(conn net.Conn) {
	s.connAuthMu.Lock()
	defer s.connAuthMu.Unlock()
	delete(s.connAuth, conn)
}

// HandleConnection takes a connection and wraps it in a listener so it can
// be passed to http.Serve to process as a HTTP request.
func (s *Server) HandleConnection(conn net.Conn) {
	// Wrap conn in a CloserConn to detect when it is closed.
	// Returning early will close conn before it has been serviced.
	// httpServer will initiate the close call.
	closerConn := utils.NewCloserConn(conn)

	cleanup, err := s.handleConnection(closerConn)
	// Make sure that the cleanup function is run
	if cleanup != nil {
		defer cleanup()
	}

	if err != nil {
		s.log.WithError(err).Warnf("Failed to handle client connection.")
		if err := conn.Close(); err != nil {
			s.log.WithError(err).Warnf("Failed to close client connection.")
		}
		return
	}

	// Wait for connection to close.
	closerConn.Wait()
}

func (s *Server) handleConnection(conn net.Conn) (func(), error) {
	// Make sure everything here is wrapped in the tracking read connection for monitoring.
	ctx, cancel := context.WithCancel(s.closeContext)
	tc, err := srv.NewTrackingReadConn(srv.TrackingReadConnConfig{
		Conn:    conn,
		Clock:   s.c.Clock,
		Context: ctx,
		Cancel:  cancel,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Proxy sends a X.509 client certificate to pass identity information,
	// extract it and run authorization checks on it.
	tlsConn, user, app, err := s.getConnectionInfo(ctx, tc)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	authCtx, _, err := s.authorizeContext(context.WithValue(ctx, auth.ContextUser, user))

	// The behavior here is a little hard to track. To be clear here, if authorization fails
	// the following will occur:
	// 1. If the application is a TCP application, error out immediately as expected.
	// 2. If the application is an HTTP application, store the error and let the HTTP handler
	//    serve the error directly so that it's properly converted to an HTTP status code.
	//    This will ensure users will get a 403 when authorization fails.
	if err != nil {
		if !app.IsTCP() {
			s.setConnAuth(tlsConn, err)
		} else {
			return nil, trace.Wrap(err)
		}
	} else {
		err = s.monitorConn(ctx, tc, authCtx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// Application access supports plain TCP connections which are handled
	// differently than HTTP requests from web apps.
	if app.IsTCP() {
		identity := authCtx.Identity.GetIdentity()
		return nil, s.handleTCPApp(ctx, tlsConn, &identity, app)
	}

	return func() {
		s.deleteConnAuth(tlsConn)
	}, s.handleHTTPApp(ctx, tlsConn)
}

// monitorConn takes a TrackingReadConn and starts a connection monitor. The tracking connection will be
// auto-terminated if disconnect_expired_cert or idle timeout is configured.
func (s *Server) monitorConn(ctx context.Context, tc *srv.TrackingReadConn, authCtx *auth.Context) error {
	authPref, err := s.c.AuthClient.GetAuthPreference(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	netConfig, err := s.c.AuthClient.GetClusterNetworkingConfig(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	identity := authCtx.Identity.GetIdentity()
	checker := authCtx.Checker

	idleTimeout := checker.AdjustClientIdleTimeout(netConfig.GetClientIdleTimeout())

	// Start monitoring client connection. When client connection is closed the monitor goroutine exits.
	err = srv.StartMonitor(srv.MonitorConfig{
		LockWatcher:           s.c.LockWatcher,
		LockTargets:           authCtx.LockTargets(),
		DisconnectExpiredCert: srv.GetDisconnectExpiredCertFromIdentity(checker, authPref, &identity),
		ClientIdleTimeout:     idleTimeout,
		Conn:                  tc,
		Tracker:               tc,
		Context:               ctx,
		Clock:                 s.c.Clock,
		ServerID:              s.c.HostID,
		TeleportUser:          identity.Username,
		Emitter:               s.c.Emitter,
		Entry:                 s.log,
		MonitorCloseChannel:   s.c.MonitorCloseChannel,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// handleTCPApp handles connection for a TCP application.
func (s *Server) handleTCPApp(ctx context.Context, conn net.Conn, identity *tlsca.Identity, app types.Application) error {
	err := s.tcpServer.handleConnection(s.closeContext, conn, identity, app)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// handleHTTPApp handles connection for an HTTP application.
func (s *Server) handleHTTPApp(ctx context.Context, conn net.Conn) error {
	// Wrap a TLS authorizing conn in a single-use listener.
	listener := newListener(s.closeContext, conn)

	// Serve will return as soon as tlsConn is running in its own goroutine
	err := s.httpServer.Serve(listener)
	if err != nil && !errors.Is(err, errListenerConnServed) {
		// okay to ignore errListenerConnServed; it is a signal that our
		// single-use listener has passed the connection to http.Serve
		// and conn is being served. See listener.Accept for details.
		return trace.Wrap(err)
	}

	return nil
}

// ServeHTTP will forward the *http.Request to the target application.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// See if the initial auth failed. If it didn't, serve the HTTP regularly, which
	// will include subsequent auth attempts to prevent race-type conditions.
	conn, ok := r.Context().Value(connContextKey).(net.Conn)
	if !ok {
		s.log.Errorf("unable to extract connection from context")
	}
	err := s.getAndDeleteConnAuth(conn)
	if err == nil {
		err = s.serveHTTP(w, r)
	}
	if err != nil {
		s.log.Warnf("Failed to serve request: %v.", err)

		// Covert trace error type to HTTP and write response, make sure we close the
		// connection afterwards so that the monitor is recreated if needed.
		code := trace.ErrorToCode(err)
		w.Header().Set("Connection", "close")
		http.Error(w, http.StatusText(code), code)
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) error {
	// Extract the identity and application being requested from the certificate
	// and check if the caller has access.
	authCtx, app, err := s.authorizeContext(r.Context())
	if err != nil {
		return trace.Wrap(err)
	}

	identity := authCtx.Identity.GetIdentity()
	switch {
	case app.IsAWSConsole():
		//  Requests from AWS applications are singed by AWS Signature Version
		//  4 algorithm. AWS CLI and AWS SDKs automatically use SigV4 for all
		//  services that support it (All services expect Amazon SimpleDB but
		//  this AWS service has been deprecated)
		if aws.IsSignedByAWSSigV4(r) {
			return s.serveSession(w, r, &identity, app, s.withAWSForwarder)
		}

		// Request for AWS console access originated from Teleport Proxy WebUI
		// is not signed by SigV4.
		return s.serveAWSWebConsole(w, r, &identity, app)

	default:
		return s.serveSession(w, r, &identity, app, s.withJWTTokenForwarder)
	}

}

// serveAWSWebConsole generates a sign-in URL for AWS management console and
// redirects the user to it.
func (s *Server) serveAWSWebConsole(w http.ResponseWriter, r *http.Request, identity *tlsca.Identity, app types.Application) error {
	s.log.Debugf("Redirecting %v to AWS mananement console with role %v.",
		identity.Username, identity.RouteToApp.AWSRoleARN)

	url, err := s.c.Cloud.GetAWSSigninURL(AWSSigninRequest{
		Identity:   identity,
		TargetURL:  app.GetURI(),
		Issuer:     app.GetPublicAddr(),
		ExternalID: app.GetAWSExternalID(),
	})
	if err != nil {
		return trace.Wrap(err)
	}
	http.Redirect(w, r, url.SigninURL, http.StatusFound)
	return nil
}

// serveSession finds the app session and forwards the request.
func (s *Server) serveSession(w http.ResponseWriter, r *http.Request, identity *tlsca.Identity, app types.Application, opts ...sessionOpt) error {
	// Fetch a cached request forwarder (or create one) that lives about 5
	// minutes. Used to stream session chunks to the Audit Log.
	session, err := s.getSession(r.Context(), identity, app, opts...)
	if err != nil {
		return trace.Wrap(err)
	}
	defer session.release()

	// Create session context.
	sessionCtx := &common.SessionContext{
		Identity: identity,
		App:      app,
		Emitter:  session.streamWriter,
	}

	// Forward request to the target application.
	session.fwd.ServeHTTP(w, common.WithSessionContext(r, sessionCtx))
	return nil
}

// getConnectionInfo extracts identity information from the provided
// connection and runs authorization checks on it.
//
// The connection comes from the reverse tunnel and is expected to be TLS and
// carry identity in the client certificate.
func (s *Server) getConnectionInfo(ctx context.Context, conn net.Conn) (*tls.Conn, auth.IdentityGetter, types.Application, error) {
	tlsConn := tls.Server(conn, s.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		return nil, nil, nil, trace.Wrap(err, "TLS handshake failed")
	}

	user, err := s.authMiddleware.GetUser(tlsConn.ConnectionState())
	if err != nil {
		return nil, nil, nil, trace.Wrap(err)
	}

	app, err := s.getApp(ctx, user.GetIdentity().RouteToApp.PublicAddr)
	if err != nil {
		return nil, nil, nil, trace.Wrap(err)
	}

	return tlsConn, user, app, nil
}

// authorizeContext will check if the context carries identity information and
// runs authorization checks on it.
func (s *Server) authorizeContext(ctx context.Context) (*auth.Context, types.Application, error) {
	// Only allow local and remote identities to proxy to an application.
	userType := ctx.Value(auth.ContextUser)
	switch userType.(type) {
	case auth.LocalUser, auth.RemoteUser:
	default:
		return nil, nil, trace.BadParameter("invalid identity: %T", userType)
	}

	// Extract authorizing context and identity of the user from the request.
	authContext, err := s.c.Authorizer.Authorize(ctx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	identity := authContext.Identity.GetIdentity()

	// Fetch the application and check if the identity has access.
	app, err := s.getApp(ctx, identity.RouteToApp.PublicAddr)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	ap, err := s.c.AccessPoint.GetAuthPreference(ctx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	// When accessing AWS management console, check permissions to assume
	// requested IAM role as well.
	var matchers []services.RoleMatcher
	if app.IsAWSConsole() {
		matchers = append(matchers, &services.AWSRoleARNMatcher{
			RoleARN: identity.RouteToApp.AWSRoleARN,
		})
	}

	mfaParams := authContext.MFAParams(ap.GetRequireMFAType())
	err = authContext.Checker.CheckAccess(
		app,
		mfaParams,
		matchers...)
	if err != nil {
		return nil, nil, utils.OpaqueAccessDenied(err)
	}

	return authContext, app, nil
}

// getSession returns a request session used to proxy the request to the
// target application. Always checks if the session is valid first and if so,
// will return a cached session, otherwise will create one.
// The in-flight request count is automatically incremented on the session.
// The caller must call session.release() after finishing its use
func (s *Server) getSession(ctx context.Context, identity *tlsca.Identity, app types.Application, opts ...sessionOpt) (*sessionChunk, error) {
	session, err := s.cache.get(identity.RouteToApp.SessionID)
	// If a cached forwarder exists, return it right away.
	if err == nil && session.acquire() == nil {
		return session, nil
	}

	// Create a new session with a recorder and forwarder in it.
	session, err = s.newSessionChunk(ctx, identity, app, opts...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return session, nil
}

// getApp returns an application matching the public address. If multiple
// matching applications exist, the first one is returned. Random selection
// (or round robin) does not need to occur here because they will all point
// to the same target address. Random selection (or round robin) occurs at the
// web proxy to load balance requests to the application service.
func (s *Server) getApp(ctx context.Context, publicAddr string) (types.Application, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.getApps() {
		if publicAddr == a.GetPublicAddr() {
			return s.appWithUpdatedLabels(a), nil
		}
	}
	return nil, trace.NotFound("no application at %v found", publicAddr)
}

// appWithUpdatedLabels will inject updated dynamic and cloud labels into an application
// object. The caller must invoke an RLock on `s.mu` before calling this function.
func (s *Server) appWithUpdatedLabels(app types.Application) *types.AppV3 {
	// Create a copy of the application to modify
	copy := app.Copy()

	// Update dynamic labels if the app has them.
	labels := s.dynamicLabels[copy.GetName()]

	if labels != nil {
		copy.SetDynamicLabels(labels.Get())
	}

	// Add in the cloud labels if the app has them.
	if s.c.CloudLabels != nil {
		s.c.CloudLabels.Apply(copy)
	}

	return copy
}

// newHTTPServer creates an *http.Server that can authorize and forward
// requests to a target application.
func (s *Server) newHTTPServer() *http.Server {
	// Reuse the auth.Middleware to authorize requests but only accept
	// certificates that were specifically generated for applications.
	s.authMiddleware = &auth.Middleware{
		AccessPoint:   s.c.AccessPoint,
		AcceptedUsage: []string{teleport.UsageAppsOnly},
	}
	s.authMiddleware.Wrap(s)

	return &http.Server{
		Handler:           httplib.MakeTracingHandler(s.authMiddleware, teleport.ComponentApp),
		ReadHeaderTimeout: apidefaults.DefaultDialTimeout,
		ErrorLog:          utils.NewStdlogger(s.log.Error, teleport.ComponentApp),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connContextKey, c)
		},
	}
}

// newTCPServer creates a server that proxies TCP applications.
func (s *Server) newTCPServer() *tcpServer {
	return &tcpServer{
		authClient: s.c.AuthClient,
		hostID:     s.c.HostID,
		log:        s.log,
	}
}

// getProxyPort tries to figure out the address the proxy is running at.
func (s *Server) getProxyPort() string {
	servers, err := s.c.AccessPoint.GetProxies()
	if err != nil {
		return strconv.Itoa(defaults.HTTPListenPort)
	}
	if len(servers) == 0 {
		return strconv.Itoa(defaults.HTTPListenPort)
	}
	_, port, err := net.SplitHostPort(servers[0].GetPublicAddr())
	if err != nil {
		return strconv.Itoa(defaults.HTTPListenPort)
	}
	return port
}

// getConfigForClient returns the list of CAs that could have signed the
// client's certificate.
func (s *Server) getConfigForClient(info *tls.ClientHelloInfo) (*tls.Config, error) {
	var clusterName string
	var err error

	// Try and extract the name of the cluster that signed the client's certificate.
	if info.ServerName != "" {
		clusterName, err = apiutils.DecodeClusterName(info.ServerName)
		if err != nil {
			if !trace.IsNotFound(err) {
				s.log.Debugf("Ignoring unsupported cluster name %q.", info.ServerName)
			}
		}
	}

	// Fetch list of CAs that could have signed this certificate. If clusterName
	// is empty, all CAs that this cluster knows about are returned.
	pool, _, err := auth.DefaultClientCertPool(s.c.AccessPoint, clusterName)
	if err != nil {
		// If this request fails, return nil and fallback to the default ClientCAs.
		s.log.Debugf("Failed to retrieve client pool: %v.", trace.DebugReport(err))
		return nil, nil
	}

	// Don't modify the server's *tls.Config, create one per connection because
	// the requests could be coming from different clusters.
	tlsCopy := s.tlsConfig.Clone()
	tlsCopy.ClientCAs = pool
	return tlsCopy, nil
}

// copyAndConfigureTLS can be used to copy and modify an existing *tls.Config
// for Teleport application proxy servers.
func copyAndConfigureTLS(config *tls.Config, fn func(*tls.ClientHelloInfo) (*tls.Config, error)) *tls.Config {
	tlsConfig := config.Clone()

	// Require clients to present a certificate
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert

	// Configure function that will be used to fetch the CA that signed the
	// client's certificate to verify the chain presented. If the client does not
	// pass in the cluster name, this functions pulls back all CA to try and
	// match the certificate presented against any CA.
	tlsConfig.GetConfigForClient = fn

	return tlsConfig
}
