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

package srv

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/events/eventstest"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/tlsca"
)

func newTestMonitor(ctx context.Context, t *testing.T, asrv *auth.TestAuthServer, mut ...func(*MonitorConfig)) (*mockTrackingConn, *eventstest.ChannelEmitter, MonitorConfig) {
	conn := &mockTrackingConn{make(chan struct{})}
	emitter := eventstest.NewChannelEmitter(1)
	cfg := MonitorConfig{
		Context:     ctx,
		Conn:        conn,
		Emitter:     emitter,
		Clock:       asrv.Clock(),
		Tracker:     &mockActivityTracker{asrv.Clock()},
		Entry:       logrus.StandardLogger(),
		LockWatcher: asrv.LockWatcher,
		LockTargets: []types.LockTarget{{User: "test-user"}},
		LockingMode: constants.LockingModeBestEffort,
	}
	for _, f := range mut {
		f(&cfg)
	}
	require.NoError(t, StartMonitor(cfg))
	return conn, emitter, cfg
}

func TestMonitorLockInForce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	asrv, err := auth.NewTestAuthServer(auth.TestAuthServerConfig{
		Dir:   t.TempDir(),
		Clock: clockwork.NewFakeClock(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, asrv.Close()) })

	conn, emitter, cfg := newTestMonitor(ctx, t, asrv)
	select {
	case <-conn.closedC:
		t.Fatal("Connection is already closed.")
	default:
	}
	lock, err := types.NewLock("test-lock", types.LockSpecV2{Target: cfg.LockTargets[0]})
	require.NoError(t, err)
	require.NoError(t, asrv.AuthServer.UpsertLock(ctx, lock))
	select {
	case <-conn.closedC:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for connection close.")
	}
	require.Equal(t, services.LockInForceAccessDenied(lock).Error(), (<-emitter.C()).(*apievents.ClientDisconnect).Reason)

	// Monitor should also detect preexistent locks.
	conn, emitter, cfg = newTestMonitor(ctx, t, asrv)
	select {
	case <-conn.closedC:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for connection close.")
	}
	require.Equal(t, services.LockInForceAccessDenied(lock).Error(), (<-emitter.C()).(*apievents.ClientDisconnect).Reason)
}

func TestMonitorStaleLocks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	asrv, err := auth.NewTestAuthServer(auth.TestAuthServerConfig{
		Dir:   t.TempDir(),
		Clock: clockwork.NewFakeClock(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, asrv.Close()) })

	conn, emitter, _ := newTestMonitor(ctx, t, asrv, func(cfg *MonitorConfig) {
		cfg.LockingMode = constants.LockingModeStrict
	})
	select {
	case <-conn.closedC:
		t.Fatal("Connection is already closed.")
	default:
	}

	select {
	case <-asrv.LockWatcher.LoopC:
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for LockWatcher loop check.")
	}
	select {
	case asrv.LockWatcher.StaleC <- struct{}{}:
	default:
		t.Fatal("No staleness event should be scheduled yet. This is a bug in the test.")
	}

	// ensure ResetC is drained
	select {
	case <-asrv.LockWatcher.ResetC:
	default:
	}
	go asrv.Backend.CloseWatchers()

	// wait for reset
	select {
	case <-asrv.LockWatcher.ResetC:
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for LockWatcher reset.")
	}
	select {
	case <-conn.closedC:
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for connection close.")
	}
	require.Equal(t, services.StrictLockingModeAccessDenied.Error(), (<-emitter.C()).(*apievents.ClientDisconnect).Reason)
}

type mockTrackingConn struct {
	closedC chan struct{}
}

func (c *mockTrackingConn) LocalAddr() net.Addr  { return &net.IPAddr{IP: net.IPv6loopback} }
func (c *mockTrackingConn) RemoteAddr() net.Addr { return &net.IPAddr{IP: net.IPv6loopback} }
func (c *mockTrackingConn) Close() error {
	close(c.closedC)
	return nil
}

type mockActivityTracker struct {
	clock clockwork.Clock
}

func (t *mockActivityTracker) GetClientLastActive() time.Time {
	return t.clock.Now()
}
func (t *mockActivityTracker) UpdateClientActivity() {}

// TestMonitorDisconnectExpiredCertBeforeTimeNow test case where DisconnectExpiredCert
// is already before time.Now
func TestMonitorDisconnectExpiredCertBeforeTimeNow(t *testing.T) {
	t.Parallel()

	clock := clockwork.NewRealClock()

	certExpirationTime := clock.Now().Add(-1 * time.Second)
	ctx := context.Background()
	asrv, err := auth.NewTestAuthServer(auth.TestAuthServerConfig{
		Dir:   t.TempDir(),
		Clock: clockwork.NewFakeClock(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, asrv.Close()) })

	conn, _, _ := newTestMonitor(ctx, t, asrv, func(config *MonitorConfig) {
		config.Clock = clock
		config.DisconnectExpiredCert = certExpirationTime
	})

	select {
	case <-conn.closedC:
	case <-time.After(5 * time.Second):
		t.Fatal("Client is still connected.")
	}
}

func TestTrackingReadConnEOF(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	// Close the server to force client reads to instantly return EOF.
	require.NoError(t, server.Close())

	// Wrap the client in a TrackingReadConn.
	ctx, cancel := context.WithCancel(context.Background())
	tc, err := NewTrackingReadConn(TrackingReadConnConfig{
		Conn:    client,
		Clock:   clockwork.NewFakeClock(),
		Context: ctx,
		Cancel:  cancel,
	})
	require.NoError(t, err)

	// Make sure it returns an EOF and not a wrapped exception.
	buf := make([]byte, 64)
	_, err = tc.Read(buf)
	require.Equal(t, io.EOF, err)
}

type mockChecker struct {
	services.AccessChecker
}

func (m *mockChecker) AdjustDisconnectExpiredCert(disconnect bool) bool {
	return disconnect
}

type mockAuthPreference struct {
	types.AuthPreference
}

var disconnectExpiredCert bool

func (m *mockAuthPreference) GetDisconnectExpiredCert() bool {
	return disconnectExpiredCert
}

func TestGetDisconnectExpiredCertFromIdentity(t *testing.T) {
	clock := clockwork.NewFakeClock()
	now := clock.Now()
	inAnHour := clock.Now().Add(time.Hour)
	var unset time.Time
	checker := &mockChecker{}
	authPref := &mockAuthPreference{}

	for _, test := range []struct {
		name                    string
		expires                 time.Time
		previousIdentityExpires time.Time
		mfaVerified             bool
		disconnectExpiredCert   bool
		expected                time.Time
	}{
		{
			name:                    "mfa overrides expires when set",
			expires:                 now,
			previousIdentityExpires: inAnHour,
			mfaVerified:             true,
			disconnectExpiredCert:   true,
			expected:                inAnHour,
		},
		{
			name:                    "expires returned when mfa unset",
			expires:                 now,
			previousIdentityExpires: unset,
			mfaVerified:             false,
			disconnectExpiredCert:   true,
			expected:                now,
		},
		{
			name:                    "unset when disconnectExpiredCert is false",
			expires:                 now,
			previousIdentityExpires: inAnHour,
			mfaVerified:             true,
			disconnectExpiredCert:   false,
			expected:                unset,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mfaVerified string
			if test.mfaVerified {
				mfaVerified = "1234"
			}
			identity := tlsca.Identity{
				Expires:                 test.expires,
				PreviousIdentityExpires: test.previousIdentityExpires,
				MFAVerified:             mfaVerified,
			}
			disconnectExpiredCert = test.disconnectExpiredCert
			got := GetDisconnectExpiredCertFromIdentity(checker, authPref, &identity)
			require.Equal(t, test.expected, got)
		})
	}
}
