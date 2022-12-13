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

package reversetunnel

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/utils"
)

func TestRemoteConnCleanup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := clockwork.NewFakeClock()

	watcher, err := services.NewProxyWatcher(ctx, services.ProxyWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: "test",
			Log:       utils.NewLoggerForTests(),
			Clock:     clock,
			Client:    &mockLocalSiteClient{},
		},
		ProxiesC: make(chan []types.Server, 2),
	})
	require.NoError(t, err)
	require.NoError(t, watcher.WaitInitialization())

	// setup the site
	srv := &server{
		ctx:              ctx,
		Config:           Config{Clock: clock},
		localAuthClient:  &mockLocalSiteClient{},
		log:              utils.NewLoggerForTests(),
		offlineThreshold: time.Second,
		proxyWatcher:     watcher,
	}

	site, err := newlocalSite(srv, "clustername", nil, withPeriodicFunctionInterval(time.Hour), withProxySyncInterval(time.Hour))
	require.NoError(t, err)

	// add a connection
	rconn := &mockRemoteConnConn{}
	sconn := &mockedSSHConn{}
	conn1, err := site.addConn(uuid.NewString(), types.NodeTunnel, rconn, sconn)
	require.NoError(t, err)

	reqs := make(chan *ssh.Request)

	// terminated by too many missed heartbeats
	go func() {
		site.handleHeartbeat(conn1, nil, reqs)
		cancel()
	}()

	// send an initial heartbeat
	reqs <- &ssh.Request{Type: "heartbeat"}

	// create a fake session
	fakeSession := newSessionTrackingConn(conn1, &mockRemoteConnConn{})

	// advance the clock to trigger missing a heartbeat, the last advance
	// should not force the connection to close since there is still an active session
	for i := 0; i <= missedHeartBeatThreshold+1; i++ {
		// wait until the heartbeat loop has created the timer
		clock.BlockUntil(3) // periodic ticker + heart beat timer + resync ticker = 3
		clock.Advance(srv.offlineThreshold)
	}

	// the fake session should have prevented anything from closing
	require.False(t, conn1.closed.Load())
	require.False(t, sconn.closed.Load())

	// send another heartbeat to reset exceeding the threshold
	reqs <- &ssh.Request{Type: "heartbeat"}

	// close the fake session
	clock.BlockUntil(3) // periodic ticker + heart beat timer + resync ticker = 3
	require.NoError(t, fakeSession.Close())

	// advance the clock to trigger missing a heartbeat, the last advance
	// should force the connection to close since there are no active sessions
	for i := 0; i <= missedHeartBeatThreshold; i++ {
		// wait until the heartbeat loop has created the timer
		clock.BlockUntil(3) // periodic ticker + heart beat timer + resync ticker = 3
		clock.Advance(srv.offlineThreshold)
	}

	// wait for handleHeartbeat to finish
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second): // artificially high to prevent flakiness
		t.Fatal("LocalSite heart beat handler never terminated")
	}

	// assert the connections were closed
	require.True(t, conn1.closed.Load())
	require.True(t, sconn.closed.Load())
}

func TestLocalSiteOverlap(t *testing.T) {
	t.Parallel()

	srv := &server{
		Config:          Config{Clock: clockwork.NewFakeClock()},
		ctx:             context.Background(),
		localAuthClient: &mockLocalSiteClient{},
	}

	site, err := newlocalSite(srv, "clustername", nil, withPeriodicFunctionInterval(time.Hour))
	require.NoError(t, err)

	nodeID := uuid.NewString()
	connType := types.NodeTunnel
	dreq := &sshutils.DialReq{
		ServerID: nodeID,
		ConnType: connType,
	}

	// add a few connections for the same node id
	conn1, err := site.addConn(nodeID, connType, &mockRemoteConnConn{}, nil)
	require.NoError(t, err)

	conn2, err := site.addConn(nodeID, connType, &mockRemoteConnConn{}, nil)
	require.NoError(t, err)

	conn3, err := site.addConn(nodeID, connType, &mockRemoteConnConn{}, nil)
	require.NoError(t, err)

	// no heartbeats from any of them shouldn't return a connection
	c, err := site.getRemoteConn(dreq)
	require.True(t, trace.IsNotFound(err))
	require.Nil(t, c)

	// ensure conn1 is ready
	conn1.setLastHeartbeat(time.Now())

	// getRemoteConn returns the only healthy connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn1, c)

	// ensure conn2 is ready
	conn2.setLastHeartbeat(time.Now())

	// getRemoteConn returns the newest healthy connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn2, c)

	// mark conn2 invalid
	conn2.markInvalid(nil)

	// getRemoteConn returns the only healthy connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn1, c)

	// mark conn1 invalid
	conn1.markInvalid(nil)

	// getRemoteConn returns the only healthy connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn2, c)

	// remove conn2
	site.removeRemoteConn(conn2)

	// getRemoteConn returns the only invalid connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn1, c)

	// remove conn1
	site.removeRemoteConn(conn1)

	// no ready connections exist
	c, err = site.getRemoteConn(dreq)
	require.True(t, trace.IsNotFound(err))
	require.Nil(t, c)

	// mark conn3 as ready
	conn3.setLastHeartbeat(time.Now())

	// getRemoteConn returns the only healthy connection
	c, err = site.getRemoteConn(dreq)
	require.NoError(t, err)
	require.Equal(t, conn3, c)
}

func TestProxyResync(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := clockwork.NewFakeClock()

	proxy1, err := types.NewServer(uuid.NewString(), types.KindProxy, types.ServerSpecV2{})
	require.NoError(t, err)

	proxy2, err := types.NewServer(uuid.NewString(), types.KindProxy, types.ServerSpecV2{})
	require.NoError(t, err)

	// set up the watcher and wait for it to be initialized
	watcher, err := services.NewProxyWatcher(ctx, services.ProxyWatcherConfig{
		ResourceWatcherConfig: services.ResourceWatcherConfig{
			Component: "test",
			Log:       utils.NewLoggerForTests(),
			Clock:     clock,
			Client: &mockLocalSiteClient{
				proxies: []types.Server{proxy1, proxy2},
			},
		},
		ProxiesC: make(chan []types.Server, 2),
	})
	require.NoError(t, err)
	require.NoError(t, watcher.WaitInitialization())

	// set up the site
	srv := &server{
		ctx:              ctx,
		Config:           Config{Clock: clock},
		localAuthClient:  &mockLocalSiteClient{},
		log:              utils.NewLoggerForTests(),
		offlineThreshold: 24 * time.Hour,
		proxyWatcher:     watcher,
	}
	site, err := newlocalSite(srv, "clustername", nil, withProxySyncInterval(time.Second), withPeriodicFunctionInterval(24*time.Hour))
	require.NoError(t, err)

	// create the ssh machinery to mock an agent
	discoveryCh := make(chan *discoveryRequest)

	reqHandler := func(name string, wantReply bool, payload []byte) (bool, error) {
		assert.Equal(t, name, chanDiscoveryReq)

		var req discoveryRequest
		assert.NoError(t, json.Unmarshal(payload, &req))
		discoveryCh <- &req
		return true, nil
	}
	channelCreator := func(name string) ssh.Channel {
		assert.Equal(t, name, chanDiscovery)
		return &mockedSSHChannel{reqHandler: reqHandler}
	}

	rconn := &mockRemoteConnConn{}
	sconn := &mockedSSHConn{
		channelFn: channelCreator,
	}

	// add a connection
	conn1, err := site.addConn(uuid.NewString(), types.NodeTunnel, rconn, sconn)
	require.NoError(t, err)

	reqs := make(chan *ssh.Request)

	// terminated by canceled context
	go func() {
		site.handleHeartbeat(conn1, nil, reqs)
	}()

	expected := []types.Server{proxy1, proxy2}
	sort.Slice(expected, func(i, j int) bool { return expected[i].GetName() < expected[j].GetName() })
	for i := 0; i < 5; i++ {
		// wait for the heartbeat loop to select
		clock.BlockUntil(3) // periodic ticker + heart beat timer + resync ticker = 3

		// advance the sync interval to force a discovery request to be sent
		clock.Advance(time.Second)

		// wait for the discovery request to be received
		select {
		case req := <-discoveryCh:
			require.NotNil(t, req)
			require.Len(t, req.Proxies, 2)

			sort.Slice(req.Proxies, func(i, j int) bool { return req.Proxies[i].GetName() < req.Proxies[j].GetName() })

			require.Equal(t, req.Proxies[0].GetName(), expected[0].GetName())
			require.Equal(t, req.Proxies[1].GetName(), expected[1].GetName())
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for discovery request")
		}
	}
}

type mockLocalSiteClient struct {
	auth.Client

	proxies []types.Server
}

// called by (*localSite).sshTunnelStats() as part of (*localSite).periodicFunctions()
func (*mockLocalSiteClient) GetNodes(_ context.Context, _ string) ([]types.Server, error) {
	return nil, nil
}

func (m *mockLocalSiteClient) GetProxies() ([]types.Server, error) {
	return m.proxies, nil
}

type mockWatcher struct {
	events chan types.Event
}

func newMockWatcher() mockWatcher {
	ch := make(chan types.Event, 1)

	ch <- types.Event{
		Type: types.OpInit,
	}

	return mockWatcher{events: ch}
}

func (m mockWatcher) Events() <-chan types.Event {
	return m.events
}

func (m mockWatcher) Done() <-chan struct{} {
	return make(chan struct{})
}

func (m mockWatcher) Close() error {
	return nil
}

func (m mockWatcher) Error() error {
	return nil
}

// called by proxyWatcher
func (mockLocalSiteClient) NewWatcher(context.Context, types.Watch) (types.Watcher, error) {
	return newMockWatcher(), nil
}

type mockRemoteConnConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *mockRemoteConnConn) Close() error {
	c.closed.Store(true)
	return nil
}

// called for logging by (*remoteConn).markInvalid()
func (*mockRemoteConnConn) RemoteAddr() net.Addr {
	return &utils.NetAddr{
		Addr:        "localhost",
		AddrNetwork: "tcp",
	}
}

type mockedSSHConn struct {
	ssh.Conn
	closed atomic.Bool

	channelFn func(string) ssh.Channel
}

func (c *mockedSSHConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *mockedSSHConn) OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	ch := make(chan *ssh.Request)
	close(ch)

	if c.channelFn != nil {
		return c.channelFn(name), ch, nil
	}

	return &mockedSSHChannel{}, ch, nil
}

func (*mockedSSHConn) RemoteAddr() net.Addr {
	return &utils.NetAddr{
		Addr:        "localhost",
		AddrNetwork: "tcp",
	}
}

type mockedSSHChannel struct {
	ssh.Channel

	reqHandler func(name string, wantReply bool, payload []byte) (bool, error)
}

func (c *mockedSSHChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	if c.reqHandler == nil {
		return true, nil
	}

	return c.reqHandler(name, wantReply, payload)
}

func (*mockedSSHChannel) Close() error {
	return nil
}
