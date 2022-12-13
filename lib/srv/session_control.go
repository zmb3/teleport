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

package srv

import (
	"context"
	"io"
	"strings"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/observability/tracing"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/api/utils/keys"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/observability/metrics"
	"github.com/zmb3/teleport/lib/services"
)

var (
	userSessionLimitHitCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: teleport.MetricUserMaxConcurrentSessionsHit,
			Help: "Number of times a user exceeded their max concurrent ssh connections",
		},
	)
)

func init() {
	_ = metrics.RegisterPrometheusCollectors(userSessionLimitHitCount)
}

// LockEnforcer determines whether a lock is being enforced on the provided targets
type LockEnforcer interface {
	CheckLockInForce(mode constants.LockingMode, targets ...types.LockTarget) error
}

// SessionControllerConfig contains dependencies needed to
// create a SessionController
type SessionControllerConfig struct {
	// Semaphores is used to obtain a semaphore lock when max sessions are defined
	Semaphores types.Semaphores
	// AccessPoint is the cache used to get cluster information
	AccessPoint AccessPoint
	// LockEnforcer is used to determine if locks should prevent a session
	LockEnforcer LockEnforcer
	// Emitter is used to emit session rejection events
	Emitter apievents.Emitter
	// Component is the component running the session controller. Nodes and Proxies
	// have different flows
	Component string
	// Logger is used to emit log entries
	Logger *logrus.Entry
	// TracerProvider creates a tracer so that spans may be emitted
	TracerProvider oteltrace.TracerProvider
	// ServerID is the UUID of the server
	ServerID string
	// Clock used in tests to change time
	Clock clockwork.Clock

	tracer oteltrace.Tracer
}

// CheckAndSetDefaults ensures all the required dependencies were
// provided and sets any optional values to their defaults
func (c *SessionControllerConfig) CheckAndSetDefaults() error {
	if c.Semaphores == nil {
		return trace.BadParameter("Semaphores must be provided")
	}

	if c.AccessPoint == nil {
		return trace.BadParameter("AccessPoint must be provided")
	}

	if c.LockEnforcer == nil {
		return trace.BadParameter("LockWatcher must be provided")
	}

	if c.Emitter == nil {
		return trace.BadParameter("Emitter must be provided")
	}

	if c.Component == "" {
		return trace.BadParameter("Component must be provided")
	}

	if c.TracerProvider == nil {
		c.TracerProvider = tracing.DefaultProvider()
	}

	if c.Logger == nil {
		c.Logger = logrus.WithField(trace.Component, "SessionCtrl")
	}

	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}

	c.tracer = c.TracerProvider.Tracer("SessionController")

	return nil
}

// SessionController enforces session control restrictions required by
// locks, private key policy, and max connection limits
type SessionController struct {
	cfg SessionControllerConfig
}

// NewSessionController creates a SessionController from the provided config. If any
// of the required parameters in the SessionControllerConfig are not provided an
// error is returned.
func NewSessionController(cfg SessionControllerConfig) (*SessionController, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &SessionController{cfg: cfg}, nil
}

// AcquireSessionContext attempts to create a context for the session. If the session is
// not allowed due to session control an error is returned. The returned
// context is scoped to the session and will be canceled in the event the semaphore lock
// is no longer held. The closers provided are immediately closed when the semaphore lock
// is released as well.
func (s *SessionController) AcquireSessionContext(ctx context.Context, identity IdentityContext, localAddr, remoteAddr string, closers ...io.Closer) (context.Context, error) {
	// create a separate context for tracing the operations
	// within that doesn't leak into the returned context
	spanCtx, span := s.cfg.tracer.Start(ctx, "SessionController/AcquireSessionContext")
	defer span.End()

	authPref, err := s.cfg.AccessPoint.GetAuthPreference(spanCtx)
	if err != nil {
		return ctx, trace.Wrap(err)
	}

	clusterName, err := s.cfg.AccessPoint.GetClusterName()
	if err != nil {
		return ctx, trace.Wrap(err)
	}

	lockingMode := identity.AccessChecker.LockingMode(authPref.GetLockingMode())
	lockTargets := ComputeLockTargets(clusterName.GetClusterName(), s.cfg.ServerID, identity)

	if lockErr := s.cfg.LockEnforcer.CheckLockInForce(lockingMode, lockTargets...); lockErr != nil {
		s.emitRejection(spanCtx, identity.GetUserMetadata(), localAddr, remoteAddr, lockErr.Error(), 0)
		return ctx, trace.Wrap(lockErr)
	}

	// Check that the required private key policy, defined by roles and auth pref,
	// is met by this Identity's ssh certificate.
	identityPolicy := identity.Certificate.Extensions[teleport.CertExtensionPrivateKeyPolicy]
	requiredPolicy := identity.AccessChecker.PrivateKeyPolicy(authPref.GetPrivateKeyPolicy())
	if err := requiredPolicy.VerifyPolicy(keys.PrivateKeyPolicy(identityPolicy)); err != nil {
		return ctx, trace.Wrap(err)
	}

	// Don't apply the following checks in non-node contexts.
	if s.cfg.Component != teleport.ComponentNode {
		return ctx, nil
	}

	maxConnections := identity.AccessChecker.MaxConnections()
	if maxConnections == 0 {
		// concurrent session control is not active, nothing
		// else needs to be done here.
		return ctx, nil
	}

	netConfig, err := s.cfg.AccessPoint.GetClusterNetworkingConfig(spanCtx)
	if err != nil {
		return ctx, trace.Wrap(err)
	}

	semLock, err := services.AcquireSemaphoreLock(spanCtx, services.SemaphoreLockConfig{
		Service: s.cfg.Semaphores,
		Clock:   s.cfg.Clock,
		Expiry:  netConfig.GetSessionControlTimeout(),
		Params: types.AcquireSemaphoreRequest{
			SemaphoreKind: types.SemaphoreKindConnection,
			SemaphoreName: identity.TeleportUser,
			MaxLeases:     maxConnections,
			Holder:        s.cfg.ServerID,
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), teleport.MaxLeases) {
			// user has exceeded their max concurrent ssh connections.
			userSessionLimitHitCount.Inc()
			s.emitRejection(spanCtx, identity.GetUserMetadata(), localAddr, remoteAddr, events.SessionRejectedEvent, maxConnections)

			return ctx, trace.AccessDenied("too many concurrent ssh connections for user %q (max=%d)", identity.TeleportUser, maxConnections)
		}

		return ctx, trace.Wrap(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	// ensure that losing the lock closes the connection context.  Under normal
	// conditions, cancellation propagates from the connection context to the
	// lock, but if we lose the lock due to some error (e.g. poor connectivity
	// to auth server) then cancellation propagates in the other direction.
	go func() {
		// TODO(fspmarshall): If lock was lost due to error, find a way to propagate
		// an error message to user.
		<-semLock.Done()
		cancel()

		// close any provided closers
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()

	return ctx, nil
}

// emitRejection emits a SessionRejectedEvent with the provided information
func (s *SessionController) emitRejection(ctx context.Context, userMetadata apievents.UserMetadata, localAddr, remoteAddr string, reason string, max int64) {
	// link a background context to the current span so things
	// are related but while still allowing the audit event to
	// not be tied to the request scoped context
	emitCtx := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.SpanContextFromContext(ctx))

	ctx, span := s.cfg.tracer.Start(emitCtx, "SessionController/emitRejection")
	defer span.End()

	if err := s.cfg.Emitter.EmitAuditEvent(ctx, &apievents.SessionReject{
		Metadata: apievents.Metadata{
			Type: events.SessionRejectedEvent,
			Code: events.SessionRejectedCode,
		},
		UserMetadata: userMetadata,
		ConnectionMetadata: apievents.ConnectionMetadata{
			Protocol:   events.EventProtocolSSH,
			LocalAddr:  localAddr,
			RemoteAddr: remoteAddr,
		},
		ServerMetadata: apievents.ServerMetadata{
			ServerID:        s.cfg.ServerID,
			ServerNamespace: apidefaults.Namespace,
		},
		Reason:  reason,
		Maximum: max,
	}); err != nil {
		s.cfg.Logger.WithError(err).Warn("Failed to emit session reject event.")
	}
}
