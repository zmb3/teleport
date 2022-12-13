/*
Copyright 2016 Gravitational, Inc.

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

package regular

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gravitational/trace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/observability/tracing"
	apisshutils "github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/observability/metrics"
	"github.com/zmb3/teleport/lib/proxy"
	"github.com/zmb3/teleport/lib/srv"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/utils"
)

var ( // failedConnectingToNode counts failed attempts to connect to a node
	proxiedSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: teleport.MetricProxySSHSessions,
			Help: "Number of active sessions through this proxy",
		},
	)

	failedConnectingToNode = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: teleport.MetricFailedConnectToNodeAttempts,
			Help: "Number of failed SSH connection attempts to a node. Use with `teleport_connect_to_node_attempts_total` to get the failure rate.",
		},
	)

	connectingToNode = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: teleport.MetricNamespace,
			Name:      teleport.MetricConnectToNodeAttempts,
			Help:      "Number of SSH connection attempts to a node. Use with `failed_connect_to_node_attempts_total` to get the failure rate.",
		},
	)
)

func init() {
	metrics.RegisterPrometheusCollectors(proxiedSessions, failedConnectingToNode, connectingToNode)
}

// proxySubsys implements an SSH subsystem for proxying listening sockets from
// remote hosts to a proxy client (AKA port mapping)
type proxySubsys struct {
	proxySubsysRequest
	router *proxy.Router
	ctx    *srv.ServerContext
	log    *logrus.Entry
	closeC chan error
}

// parseProxySubsys looks at the requested subsystem name and returns a fully configured
// proxy subsystem
//
// proxy subsystem name can take the following forms:
//
//	"proxy:host:22"          - standard SSH request to connect to  host:22 on the 1st cluster
//	"proxy:@clustername"        - Teleport request to connect to an auth server for cluster with name 'clustername'
//	"proxy:host:22@clustername" - Teleport request to connect to host:22 on cluster 'clustername'
//	"proxy:host:22@namespace@clustername"
func parseProxySubsysRequest(request string) (proxySubsysRequest, error) {
	log.Debugf("parse_proxy_subsys(%q)", request)
	var (
		clusterName  string
		targetHost   string
		targetPort   string
		paramMessage = fmt.Sprintf("invalid format for proxy request: %q, expected 'proxy:host:port@cluster'", request)
	)
	const prefix = "proxy:"
	// get rid of 'proxy:' prefix:
	if strings.Index(request, prefix) != 0 {
		return proxySubsysRequest{}, trace.BadParameter(paramMessage)
	}
	requestBody := strings.TrimPrefix(request, prefix)
	namespace := apidefaults.Namespace
	parts := strings.Split(requestBody, "@")

	var err error
	switch {
	case len(parts) == 0: // "proxy:"
		return proxySubsysRequest{}, trace.BadParameter(paramMessage)
	case len(parts) == 1: // "proxy:host:22"
		targetHost, targetPort, err = utils.SplitHostPort(parts[0])
		if err != nil {
			return proxySubsysRequest{}, trace.BadParameter(paramMessage)
		}
	case len(parts) == 2: // "proxy:@clustername" or "proxy:host:22@clustername"
		if parts[0] != "" {
			targetHost, targetPort, err = utils.SplitHostPort(parts[0])
			if err != nil {
				return proxySubsysRequest{}, trace.BadParameter(paramMessage)
			}
		}
		clusterName = parts[1]
		if clusterName == "" && targetHost == "" {
			return proxySubsysRequest{}, trace.BadParameter("invalid format for proxy request: missing cluster name or target host in %q", request)
		}
	case len(parts) >= 3: // "proxy:host:22@namespace@clustername"
		clusterName = strings.Join(parts[2:], "@")
		namespace = parts[1]
		targetHost, targetPort, err = utils.SplitHostPort(parts[0])
		if err != nil {
			return proxySubsysRequest{}, trace.BadParameter(paramMessage)
		}
	}

	return proxySubsysRequest{
		namespace:   namespace,
		host:        targetHost,
		port:        targetPort,
		clusterName: clusterName,
	}, nil
}

// parseProxySubsys decodes a proxy subsystem request and sets up a proxy subsystem instance.
// See parseProxySubsysRequest for details on the request format.
func parseProxySubsys(request string, srv *Server, ctx *srv.ServerContext) (*proxySubsys, error) {
	req, err := parseProxySubsysRequest(request)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	subsys, err := newProxySubsys(ctx, srv, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return subsys, nil
}

// proxySubsysRequest encodes proxy subsystem request parameters.
type proxySubsysRequest struct {
	namespace   string
	host        string
	port        string
	clusterName string
}

func (p *proxySubsysRequest) String() string {
	return fmt.Sprintf("host=%v, port=%v, cluster=%v", p.host, p.port, p.clusterName)
}

// SpecifiedPort returns whether the port is set, and it has a non-zero value
func (p *proxySubsysRequest) SpecifiedPort() bool {
	return len(p.port) > 0 && p.port != "0"
}

// SetDefaults sets default values.
func (p *proxySubsysRequest) SetDefaults() {
	if p.namespace == "" {
		p.namespace = apidefaults.Namespace
	}
}

// newProxySubsys is a helper that creates a proxy subsystem from
// a port forwarding request, used to implement ProxyJump feature in proxy
// and reuse the code
func newProxySubsys(ctx *srv.ServerContext, srv *Server, req proxySubsysRequest) (*proxySubsys, error) {
	req.SetDefaults()
	if req.clusterName == "" && ctx.Identity.RouteToCluster != "" {
		log.Debugf("Proxy subsystem: routing user %q to cluster %q based on the route to cluster extension.",
			ctx.Identity.TeleportUser, ctx.Identity.RouteToCluster,
		)
		req.clusterName = ctx.Identity.RouteToCluster
	}
	if req.clusterName != "" && srv.proxyTun != nil {
		_, err := srv.tunnelWithAccessChecker(ctx).GetSite(req.clusterName)
		if err != nil {
			return nil, trace.BadParameter("invalid format for proxy request: unknown cluster %q", req.clusterName)
		}
	}
	log.Debugf("newProxySubsys(%v).", req)
	return &proxySubsys{
		proxySubsysRequest: req,
		ctx:                ctx,
		log: logrus.WithFields(logrus.Fields{
			trace.Component:       teleport.ComponentSubsystemProxy,
			trace.ComponentFields: map[string]string{},
		}),
		closeC: make(chan error),
		router: srv.router,
	}, nil
}

func (t *proxySubsys) String() string {
	return fmt.Sprintf("proxySubsys(cluster=%s/%s, host=%s, port=%s)",
		t.namespace, t.clusterName, t.host, t.port)
}

// Start is called by Golang's ssh when it needs to engage this sybsystem (typically to establish
// a mapping connection between a client & remote node we're proxying to)
func (t *proxySubsys) Start(ctx context.Context, sconn *ssh.ServerConn, ch ssh.Channel, req *ssh.Request, serverContext *srv.ServerContext) error {
	// once we start the connection, update logger to include component fields
	t.log = logrus.WithFields(logrus.Fields{
		trace.Component: teleport.ComponentSubsystemProxy,
		trace.ComponentFields: map[string]string{
			"src": sconn.RemoteAddr().String(),
			"dst": sconn.LocalAddr().String(),
		},
	})
	t.log.Debugf("Starting subsystem")

	clientAddr := sconn.RemoteAddr()

	// did the client pass us a true client IP ahead of time via an environment variable?
	// (usually the web client would do that)
	trueClientIP, ok := serverContext.GetEnv(sshutils.TrueClientAddrVar)
	if ok {
		a, err := utils.ParseAddr(trueClientIP)
		if err == nil {
			clientAddr = a
		}
	}

	// connect to a site's auth server
	if t.host == "" {
		return t.proxyToSite(ctx, ch, t.clusterName)
	}

	// connect to a server
	return t.proxyToHost(ctx, ch, clientAddr)
}

// proxyToSite establishes a proxy connection from the connected SSH client to the
// auth server of the requested remote site
func (t *proxySubsys) proxyToSite(ctx context.Context, ch ssh.Channel, clusterName string) error {
	t.log.Debugf("Connecting to site: %v", clusterName)

	conn, err := t.router.DialSite(ctx, clusterName)
	if err != nil {
		return trace.Wrap(err)
	}
	t.log.Infof("Connected to cluster %v at %v", clusterName, conn.RemoteAddr())

	proxiedSessions.Inc()

	go func() {
		t.close(utils.ProxyConn(ctx, ch, conn))
	}()
	return nil
}

// proxyToHost establishes a proxy connection from the connected SSH client to the
// requested remote node (t.host:t.port) via the given site
func (t *proxySubsys) proxyToHost(ctx context.Context, ch ssh.Channel, remoteAddr net.Addr) error {
	t.log.Debugf("proxy connecting to host=%v port=%v, exact port=%v", t.host, t.port, t.SpecifiedPort())

	conn, err := t.router.DialHost(ctx, remoteAddr, t.host, t.port, t.clusterName, t.ctx.Identity.AccessChecker, t.ctx.StartAgentChannel)
	if err != nil {
		failedConnectingToNode.Inc()
		return trace.Wrap(err)
	}

	// this custom SSH handshake allows SSH proxy to relay the client's IP
	// address to the SSH server
	t.doHandshake(ctx, remoteAddr, ch, conn)

	proxiedSessions.Inc()

	go func() {
		t.close(utils.ProxyConn(ctx, ch, conn))
	}()
	return nil
}

func (t *proxySubsys) close(err error) {
	proxiedSessions.Dec()
	t.closeC <- err
}

func (t *proxySubsys) Wait() error {
	return <-t.closeC
}

// doHandshake allows a proxy server to send additional information (client IP)
// to an SSH server before establishing a bridge
func (t *proxySubsys) doHandshake(ctx context.Context, clientAddr net.Addr, clientConn io.ReadWriter, serverConn io.ReadWriter) {
	// on behalf of a client ask the server for its version:
	buff := make([]byte, sshutils.MaxVersionStringBytes)
	n, err := serverConn.Read(buff)
	if err != nil {
		t.log.Error(err)
		return
	}
	// chop off extra unused bytes at the end of the buffer:
	buff = buff[:n]

	// is that a Teleport server?
	if bytes.HasPrefix(buff, []byte(sshutils.SSHVersionPrefix)) {
		// if we're connecting to a Teleport SSH server, send our own "handshake payload"
		// message, along with a client's IP:
		hp := &apisshutils.HandshakePayload{
			ClientAddr:     clientAddr.String(),
			TracingContext: tracing.PropagationContextFromContext(ctx),
		}
		payloadJSON, err := json.Marshal(hp)
		if err != nil {
			t.log.Error(err)
		} else {
			// send a JSON payload sandwiched between 'teleport proxy signature' and 0x00:
			payload := fmt.Sprintf("%s%s\x00", apisshutils.ProxyHelloSignature, payloadJSON)
			_, err = serverConn.Write([]byte(payload))
			if err != nil {
				t.log.Error(err)
			}
		}
	}
	// forward server's response to the client:
	_, err = clientConn.Write(buff)
	if err != nil {
		t.log.Error(err)
	}
}
