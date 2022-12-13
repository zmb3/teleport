/*
Copyright 2015-2020 Gravitational, Inc.

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

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gravitational/trace"
	"github.com/moby/term"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/client/webclient"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/observability/tracing"
	tracessh "github.com/zmb3/teleport/api/observability/tracing/ssh"
	"github.com/zmb3/teleport/api/types"
	apievents "github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/sshutils"
	"github.com/zmb3/teleport/lib/sshutils/scp"
	"github.com/zmb3/teleport/lib/sshutils/sftp"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/socks"
)

// ProxyClient implements ssh client to a teleport proxy
// It can provide list of nodes or connect to nodes
type ProxyClient struct {
	teleportClient  *TeleportClient
	Client          *tracessh.Client
	Tracer          oteltrace.Tracer
	hostLogin       string
	proxyAddress    string
	proxyPrincipal  string
	hostKeyCallback ssh.HostKeyCallback
	authMethods     []ssh.AuthMethod
	siteName        string
	clientAddr      string

	// currentCluster is a client for the local teleport auth server that
	// the proxy is connected to. This should be reused for the duration
	// of the ProxyClient to ensure the auth server is only dialed once.
	currentCluster auth.ClientI
}

// NodeClient implements ssh client to a ssh node (teleport or any regular ssh node)
// NodeClient can run shell and commands or upload and download files.
type NodeClient struct {
	Namespace   string
	Tracer      oteltrace.Tracer
	Client      *tracessh.Client
	TC          *TeleportClient
	OnMFA       func()
	FIPSEnabled bool
}

// ClusterName returns the name of the cluster the proxy is a member of.
func (proxy *ProxyClient) ClusterName() string {
	return proxy.siteName
}

// GetSites returns list of the "sites" (AKA teleport clusters) connected to the proxy
// Each site is returned as an instance of its auth server
func (proxy *ProxyClient) GetSites(ctx context.Context) ([]types.Site, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/GetSites",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", proxy.siteName),
		),
	)
	defer span.End()

	proxySession, err := proxy.Client.NewSession(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer proxySession.Close()
	stdout := &bytes.Buffer{}
	reader, err := proxySession.StdoutPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	done := make(chan struct{})
	go func() {
		if _, err := io.Copy(stdout, reader); err != nil {
			log.Warningf("Error reading STDOUT from proxy: %v", err)
		}
		close(done)
	}()
	// this function is async because,
	// the function call StdoutPipe() could fail if proxy rejected
	// the session request, and then RequestSubsystem call could hang
	// forever
	go func() {
		if err := proxySession.RequestSubsystem(ctx, "proxysites"); err != nil {
			log.Warningf("Failed to request subsystem: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(apidefaults.DefaultDialTimeout):
		return nil, trace.ConnectionProblem(nil, "timeout")
	}
	log.Debugf("Found clusters: %v", stdout.String())
	var sites []types.Site
	if err := json.Unmarshal(stdout.Bytes(), &sites); err != nil {
		return nil, trace.Wrap(err)
	}
	return sites, nil
}

// GetLeafClusters returns the leaf/remote clusters.
func (proxy *ProxyClient) GetLeafClusters(ctx context.Context) ([]types.RemoteCluster, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/GetLeafClusters",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", proxy.siteName),
		),
	)
	defer span.End()

	clt, err := proxy.ConnectToRootCluster(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer clt.Close()

	remoteClusters, err := clt.GetRemoteClusters()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return remoteClusters, nil
}

// ReissueParams encodes optional parameters for
// user certificate reissue.
type ReissueParams struct {
	RouteToCluster        string
	NodeName              string
	KubernetesCluster     string
	AccessRequests        []string
	DropAccessRequests    []string
	RouteToDatabase       proto.RouteToDatabase
	RouteToApp            proto.RouteToApp
	RouteToWindowsDesktop proto.RouteToWindowsDesktop

	// ExistingCreds is a gross hack for lib/web/terminal.go to pass in
	// existing user credentials. The TeleportClient in lib/web/terminal.go
	// doesn't have a real LocalKeystore and keeps all certs in memory.
	// Normally, existing credentials are loaded from
	// TeleportClient.localAgent.
	//
	// TODO(awly): refactor lib/web to use a Keystore implementation that
	// mimics LocalKeystore and remove this.
	ExistingCreds *Key

	// MFACheck is optional parameter passed if MFA check was already done.
	// It can be nil.
	MFACheck *proto.IsMFARequiredResponse
	// AuthClient is the client used for the MFACheck that can be reused
	AuthClient auth.ClientI
}

func (p ReissueParams) usage() proto.UserCertsRequest_CertUsage {
	switch {
	case p.NodeName != "":
		// SSH means a request for an SSH certificate for access to a specific
		// SSH node, as specified by NodeName.
		return proto.UserCertsRequest_SSH
	case p.KubernetesCluster != "":
		// Kubernetes means a request for a TLS certificate for access to a
		// specific Kubernetes cluster, as specified by KubernetesCluster.
		return proto.UserCertsRequest_Kubernetes
	case p.RouteToDatabase.ServiceName != "":
		// Database means a request for a TLS certificate for access to a
		// specific database, as specified by RouteToDatabase.
		return proto.UserCertsRequest_Database
	case p.RouteToApp.Name != "":
		// App means a request for a TLS certificate for access to a specific
		// web app, as specified by RouteToApp.
		return proto.UserCertsRequest_App
	case p.RouteToWindowsDesktop.WindowsDesktop != "":
		return proto.UserCertsRequest_WindowsDesktop
	default:
		// All means a request for both SSH and TLS certificates for the
		// overall user session. These certificates are not specific to any SSH
		// node, Kubernetes cluster, database or web app.
		return proto.UserCertsRequest_All
	}
}

func (p ReissueParams) isMFARequiredRequest(sshLogin string) *proto.IsMFARequiredRequest {
	req := new(proto.IsMFARequiredRequest)
	switch {
	case p.NodeName != "":
		req.Target = &proto.IsMFARequiredRequest_Node{Node: &proto.NodeLogin{Node: p.NodeName, Login: sshLogin}}
	case p.KubernetesCluster != "":
		req.Target = &proto.IsMFARequiredRequest_KubernetesCluster{KubernetesCluster: p.KubernetesCluster}
	case p.RouteToDatabase.ServiceName != "":
		req.Target = &proto.IsMFARequiredRequest_Database{Database: &p.RouteToDatabase}
	case p.RouteToWindowsDesktop.WindowsDesktop != "":
		req.Target = &proto.IsMFARequiredRequest_WindowsDesktop{WindowsDesktop: &p.RouteToWindowsDesktop}
	}
	return req
}

// CertCachePolicy describes what should happen to the certificate cache when a
// user certificate is re-issued
type CertCachePolicy int

const (
	// CertCacheDrop indicates that all user certificates should be dropped as
	// part of the re-issue process. This can be necessary if the roles
	// assigned to the user are expected to change as a part of the re-issue.
	CertCacheDrop CertCachePolicy = 0

	// CertCacheKeep indicates that all user certificates (except those
	// explicitly updated by the re-issue) should be preserved across the
	// re-issue process.
	CertCacheKeep CertCachePolicy = 1
)

// ReissueUserCerts generates certificates for the user
// that have a metadata instructing server to route the requests to the cluster
func (proxy *ProxyClient) ReissueUserCerts(ctx context.Context, cachePolicy CertCachePolicy, params ReissueParams) error {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/ReissueUserCerts",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", proxy.siteName),
		),
	)
	defer span.End()

	key, err := proxy.reissueUserCerts(ctx, cachePolicy, params)
	if err != nil {
		return trace.Wrap(err)
	}

	if cachePolicy == CertCacheDrop {
		proxy.localAgent().DeleteUserCerts("", WithAllCerts...)
	}

	// save the cert to the local storage (~/.tsh usually):
	err = proxy.localAgent().AddKey(key)
	return trace.Wrap(err)
}

func (proxy *ProxyClient) reissueUserCerts(ctx context.Context, cachePolicy CertCachePolicy, params ReissueParams) (*Key, error) {
	if params.RouteToCluster == "" {
		params.RouteToCluster = proxy.siteName
	}
	key := params.ExistingCreds
	if key == nil {
		var err error

		// Don't load the certs if we're going to drop all of them all as part
		// of the re-issue. If we load all of the old certs now we won't be able
		// to differentiate between legacy certificates (that need to be
		// deleted) and newly re-issued certs (that we definitely do *not* want
		// to delete) when it comes time to drop them from the local agent.
		certOptions := []CertOption{}
		if cachePolicy == CertCacheKeep {
			certOptions = WithAllCerts
		}

		key, err = proxy.localAgent().GetKey(params.RouteToCluster, certOptions...)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	req, err := proxy.prepareUserCertsRequest(params, key)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clt, err := proxy.ConnectToRootCluster(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer clt.Close()
	certs, err := clt.GenerateUserCerts(ctx, *req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	key.ClusterName = params.RouteToCluster

	// Only update the parts of key that match the usage. See the docs on
	// proto.UserCertsRequest_CertUsage for which certificates match which
	// usage.
	//
	// This prevents us from overwriting the top-level key.TLSCert with
	// usage-restricted certificates.
	switch params.usage() {
	case proto.UserCertsRequest_All:
		key.Cert = certs.SSH
		key.TLSCert = certs.TLS
	case proto.UserCertsRequest_SSH:
		key.Cert = certs.SSH
	case proto.UserCertsRequest_App:
		key.AppTLSCerts[params.RouteToApp.Name] = certs.TLS
	case proto.UserCertsRequest_Database:
		dbCert, err := makeDatabaseClientPEM(params.RouteToDatabase.Protocol, certs.TLS, key)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		key.DBTLSCerts[params.RouteToDatabase.ServiceName] = dbCert
	case proto.UserCertsRequest_Kubernetes:
		key.KubeTLSCerts[params.KubernetesCluster] = certs.TLS
	case proto.UserCertsRequest_WindowsDesktop:
		key.WindowsDesktopCerts[params.RouteToWindowsDesktop.WindowsDesktop] = certs.TLS
	}
	return key, nil
}

// makeDatabaseClientPEM returns appropriate client PEM file contents for the
// specified database type. Some databases only need certificate in the PEM
// file, others both certificate and key.
func makeDatabaseClientPEM(proto string, cert []byte, pk *Key) ([]byte, error) {
	// MongoDB expects certificate and key pair in the same pem file.
	if proto == defaults.ProtocolMongoDB {
		rsaKeyPEM, err := pk.PrivateKey.RSAPrivateKeyPEM()
		if err == nil {
			return append(cert, rsaKeyPEM...), nil
		} else if !trace.IsBadParameter(err) {
			return nil, trace.Wrap(err)
		}
		log.WithError(err).Warn("MongoDB integration is not supported when logging in with a non-rsa private key.")
	}
	return cert, nil
}

// PromptMFAChallengeHandler is a handler for MFA challenges.
//
// The challenge c from proxyAddr should be presented to the user, asking to
// use one of their registered MFA devices. User's response should be returned,
// or an error if anything goes wrong.
type PromptMFAChallengeHandler func(ctx context.Context, proxyAddr string, c *proto.MFAAuthenticateChallenge) (*proto.MFAAuthenticateResponse, error)

// IssueUserCertsWithMFA generates a single-use certificate for the user.
func (proxy *ProxyClient) IssueUserCertsWithMFA(ctx context.Context, params ReissueParams, promptMFAChallenge PromptMFAChallengeHandler) (*Key, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/IssueUserCertsWithMFA",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", proxy.siteName),
		),
	)
	defer span.End()

	if params.RouteToCluster == "" {
		params.RouteToCluster = proxy.siteName
	}
	key := params.ExistingCreds
	if key == nil {
		var err error
		key, err = proxy.localAgent().GetKey(params.RouteToCluster, WithAllCerts...)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	var clt auth.ClientI
	// requiredCheck passed from param can be nil.
	requiredCheck := params.MFACheck
	if requiredCheck == nil || requiredCheck.Required {
		// Connect to the target cluster (root or leaf) to check whether MFA is
		// required or if we know from param that it's required, connect because
		// it will be needed to do MFA check.
		if params.AuthClient != nil {
			clt = params.AuthClient
		} else {
			authClt, err := proxy.ConnectToCluster(ctx, params.RouteToCluster)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			clt = authClt
			defer clt.Close()
		}
	}

	if requiredCheck == nil {
		check, err := clt.IsMFARequired(ctx, params.isMFARequiredRequest(proxy.hostLogin))
		if err != nil {
			if trace.IsNotImplemented(err) {
				// Probably talking to an older server, use the old non-MFA endpoint.
				log.WithError(err).Debug("Auth server does not implement IsMFARequired.")
				// SSH certs can be used without reissuing.
				if params.usage() == proto.UserCertsRequest_SSH && key.Cert != nil {
					return key, nil
				}
				return proxy.reissueUserCerts(ctx, CertCacheKeep, params)
			}
			return nil, trace.Wrap(err)
		}
		requiredCheck = check
	}

	if !requiredCheck.Required {
		log.Debug("MFA not required for access.")
		// MFA is not required.
		// SSH certs can be used without embedding the node name.
		if params.usage() == proto.UserCertsRequest_SSH && key.Cert != nil {
			return key, nil
		}
		// All other targets need their name embedded in the cert for routing,
		// fall back to non-MFA reissue.
		return proxy.reissueUserCerts(ctx, CertCacheKeep, params)
	}

	// Always connect to root for getting new credentials, but attempt to reuse
	// the existing client if possible.
	rootClusterName, err := key.RootClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if params.RouteToCluster != rootClusterName {
		clt.Close()
		rootClusterProxy := proxy
		if jumpHost := proxy.teleportClient.JumpHosts; jumpHost != nil {
			// In case of MFA connect to root teleport proxy instead of JumpHost to request
			// MFA certificates.
			proxy.teleportClient.JumpHosts = nil
			rootClusterProxy, err = proxy.teleportClient.ConnectToProxy(ctx)
			proxy.teleportClient.JumpHosts = jumpHost
			if err != nil {
				return nil, trace.Wrap(err)
			}
			defer rootClusterProxy.Close()
		}
		clt, err = rootClusterProxy.ConnectToCluster(ctx, rootClusterName)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		defer clt.Close()
	}

	log.Debug("Attempting to issue a single-use user certificate with an MFA check.")
	stream, err := clt.GenerateUserSingleUseCerts(ctx)
	if err != nil {
		if trace.IsNotImplemented(err) {
			// Probably talking to an older server, use the old non-MFA endpoint.
			log.WithError(err).Debug("Auth server does not implement GenerateUserSingleUseCerts.")
			// SSH certs can be used without reissuing.
			if params.usage() == proto.UserCertsRequest_SSH && key.Cert != nil {
				return key, nil
			}
			return proxy.reissueUserCerts(ctx, CertCacheKeep, params)
		}
		return nil, trace.Wrap(err)
	}
	defer func() {
		// CloseSend closes the client side of the stream
		stream.CloseSend()
		// Recv to wait for the server side of the stream to end, this needs to
		// be called to ensure the spans are finished properly
		stream.Recv()
	}()

	initReq, err := proxy.prepareUserCertsRequest(params, key)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	err = stream.Send(&proto.UserSingleUseCertsRequest{Request: &proto.UserSingleUseCertsRequest_Init{
		Init: initReq,
	}})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resp, err := stream.Recv()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	mfaChal := resp.GetMFAChallenge()
	if mfaChal == nil {
		return nil, trace.BadParameter("server sent a %T on GenerateUserSingleUseCerts, expected MFAChallenge", resp.Response)
	}
	mfaResp, err := promptMFAChallenge(ctx, proxy.teleportClient.WebProxyAddr, mfaChal)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	err = stream.Send(&proto.UserSingleUseCertsRequest{Request: &proto.UserSingleUseCertsRequest_MFAResponse{MFAResponse: mfaResp}})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resp, err = stream.Recv()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	certResp := resp.GetCert()
	if certResp == nil {
		return nil, trace.BadParameter("server sent a %T on GenerateUserSingleUseCerts, expected SingleUseUserCert", resp.Response)
	}
	switch crt := certResp.Cert.(type) {
	case *proto.SingleUseUserCert_SSH:
		key.Cert = crt.SSH
	case *proto.SingleUseUserCert_TLS:
		switch initReq.Usage {
		case proto.UserCertsRequest_Kubernetes:
			key.KubeTLSCerts[initReq.KubernetesCluster] = crt.TLS
		case proto.UserCertsRequest_Database:
			dbCert, err := makeDatabaseClientPEM(params.RouteToDatabase.Protocol, crt.TLS, key)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			key.DBTLSCerts[params.RouteToDatabase.ServiceName] = dbCert
		case proto.UserCertsRequest_WindowsDesktop:
			key.WindowsDesktopCerts[params.RouteToWindowsDesktop.WindowsDesktop] = crt.TLS
		default:
			return nil, trace.BadParameter("server returned a TLS certificate but cert request usage was %s", initReq.Usage)
		}
	default:
		return nil, trace.BadParameter("server sent a %T SingleUseUserCert in response", certResp.Cert)
	}
	key.ClusterName = params.RouteToCluster
	log.Debug("Issued single-use user certificate after an MFA check.")
	return key, nil
}

func (proxy *ProxyClient) prepareUserCertsRequest(params ReissueParams, key *Key) (*proto.UserCertsRequest, error) {
	tlsCert, err := key.TeleportTLSCertificate()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if len(params.AccessRequests) == 0 {
		// Get the active access requests to include in the cert.
		activeRequests, err := key.ActiveRequests()
		// key.ActiveRequests can return a NotFound error if it doesn't have an
		// SSH cert. That's OK, we just assume that there are no AccessRequests
		// in that case.
		if err != nil && !trace.IsNotFound(err) {
			return nil, trace.Wrap(err)
		}
		params.AccessRequests = activeRequests.AccessRequests
	}

	return &proto.UserCertsRequest{
		PublicKey:             key.MarshalSSHPublicKey(),
		Username:              tlsCert.Subject.CommonName,
		Expires:               tlsCert.NotAfter,
		RouteToCluster:        params.RouteToCluster,
		KubernetesCluster:     params.KubernetesCluster,
		AccessRequests:        params.AccessRequests,
		DropAccessRequests:    params.DropAccessRequests,
		RouteToDatabase:       params.RouteToDatabase,
		RouteToWindowsDesktop: params.RouteToWindowsDesktop,
		RouteToApp:            params.RouteToApp,
		NodeName:              params.NodeName,
		Usage:                 params.usage(),
		Format:                proxy.teleportClient.CertificateFormat,
	}, nil
}

// RootClusterName returns name of the current cluster
func (proxy *ProxyClient) RootClusterName(ctx context.Context) (string, error) {
	_, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/RootClusterName",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	tlsKey, err := proxy.localAgent().GetCoreKey()
	if err != nil {
		if trace.IsNotFound(err) {
			// Fallback to TLS client certificates.
			tls := proxy.teleportClient.TLS
			if len(tls.Certificates) == 0 || len(tls.Certificates[0].Certificate) == 0 {
				return "", trace.BadParameter("missing TLS.Certificates")
			}
			cert, err := x509.ParseCertificate(tls.Certificates[0].Certificate[0])
			if err != nil {
				return "", trace.Wrap(err)
			}

			clusterName := cert.Issuer.CommonName
			if clusterName == "" {
				return "", trace.NotFound("failed to extract root cluster name from Teleport TLS cert")
			}
			return clusterName, nil
		}
		return "", trace.Wrap(err)
	}
	return tlsKey.RootClusterName()
}

// CreateAccessRequest registers a new access request with the auth server.
func (proxy *ProxyClient) CreateAccessRequest(ctx context.Context, req types.AccessRequest) error {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/CreateAccessRequest",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(attribute.String("request", req.GetName())),
	)
	defer span.End()

	site := proxy.CurrentCluster()

	return site.CreateAccessRequest(ctx, req)
}

// GetAccessRequests loads all access requests matching the supplied filter.
func (proxy *ProxyClient) GetAccessRequests(ctx context.Context, filter types.AccessRequestFilter) ([]types.AccessRequest, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/GetAccessRequests",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("id", filter.ID),
			attribute.String("user", filter.User),
		),
	)
	defer span.End()

	site := proxy.CurrentCluster()

	reqs, err := site.GetAccessRequests(ctx, filter)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return reqs, nil
}

// GetRole loads a role resource by name.
func (proxy *ProxyClient) GetRole(ctx context.Context, name string) (types.Role, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/GetRole",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("role", name),
		),
	)
	defer span.End()

	site := proxy.CurrentCluster()

	role, err := site.GetRole(ctx, name)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return role, nil
}

// NewWatcher sets up a new event watcher.
func (proxy *ProxyClient) NewWatcher(ctx context.Context, watch types.Watch) (types.Watcher, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/NewWatcher",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("name", watch.Name),
		),
	)
	defer span.End()

	site := proxy.CurrentCluster()

	watcher, err := site.NewWatcher(ctx, watch)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return watcher, nil
}

// FindNodesByFilters returns list of the nodes which have filters matched.
func (proxy *ProxyClient) FindNodesByFilters(ctx context.Context, req proto.ListResourcesRequest) ([]types.Server, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindNodesByFilters",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	servers, err := proxy.FindNodesByFiltersForCluster(ctx, req, proxy.siteName)
	return servers, trace.Wrap(err)
}

func (proxy *ProxyClient) GetClusterAlerts(ctx context.Context, req types.GetClusterAlertsRequest) ([]types.ClusterAlert, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/GetClusterAlerts",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	site := proxy.CurrentCluster()
	defer site.Close()

	alerts, err := site.GetClusterAlerts(ctx, req)
	return alerts, trace.Wrap(err)
}

// FindNodesByFiltersForCluster returns list of the nodes in a specified cluster which have filters matched.
func (proxy *ProxyClient) FindNodesByFiltersForCluster(ctx context.Context, req proto.ListResourcesRequest, cluster string) ([]types.Server, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindNodesByFiltersForCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", cluster),
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	req.ResourceType = types.KindNode

	site, err := proxy.ConnectToCluster(ctx, cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resources, err := client.GetResourcesWithFilters(ctx, site, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	servers, err := types.ResourcesWithLabels(resources).AsServers()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return servers, nil
}

// FindAppServersByFilters returns a list of application servers in the current cluster which have filters matched.
func (proxy *ProxyClient) FindAppServersByFilters(ctx context.Context, req proto.ListResourcesRequest) ([]types.AppServer, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindAppServersByFilters",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	servers, err := proxy.FindAppServersByFiltersForCluster(ctx, req, proxy.siteName)
	return servers, trace.Wrap(err)
}

// FindAppServersByFiltersForCluster returns a list of application servers for a given cluster which have filters matched.
func (proxy *ProxyClient) FindAppServersByFiltersForCluster(ctx context.Context, req proto.ListResourcesRequest, cluster string) ([]types.AppServer, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindAppServersByFiltersForCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", cluster),
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	req.ResourceType = types.KindAppServer
	authClient, err := proxy.ConnectToCluster(ctx, cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resources, err := client.GetResourcesWithFilters(ctx, authClient, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	servers, err := types.ResourcesWithLabels(resources).AsAppServers()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return servers, nil
}

// CreateAppSession creates a new application access session.
func (proxy *ProxyClient) CreateAppSession(ctx context.Context, req types.CreateAppSessionRequest) (types.WebSession, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/CreateAppSession",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("username", req.Username),
			attribute.String("cluster", req.ClusterName),
		),
	)
	defer span.End()

	clusterName, err := proxy.RootClusterName(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	authClient, err := proxy.ConnectToCluster(ctx, clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	ws, err := authClient.CreateAppSession(ctx, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Make sure to wait for the created app session to propagate through the cache.
	accessPoint, err := proxy.ConnectToCluster(ctx, clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	err = auth.WaitForAppSession(ctx, ws.GetName(), ws.GetUser(), accessPoint)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return ws, nil
}

// DeleteAppSession removes the specified application access session.
func (proxy *ProxyClient) DeleteAppSession(ctx context.Context, sessionID string) error {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/DeleteAppSession",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("session", sessionID),
		),
	)
	defer span.End()

	authClient, err := proxy.ConnectToRootCluster(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	err = authClient.DeleteAppSession(ctx, types.DeleteAppSessionRequest{SessionID: sessionID})
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// DeleteUserAppSessions removes user's all application web sessions.
func (proxy *ProxyClient) DeleteUserAppSessions(ctx context.Context, req *proto.DeleteUserAppSessionsRequest) error {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/DeleteUserAppSessions",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("username", req.Username),
		),
	)
	defer span.End()

	authClient, err := proxy.ConnectToRootCluster(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	err = authClient.DeleteUserAppSessions(ctx, req)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// FindDatabaseServersByFilters returns registered database proxy servers that match the provided filter.
func (proxy *ProxyClient) FindDatabaseServersByFilters(ctx context.Context, req proto.ListResourcesRequest) ([]types.DatabaseServer, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindDatabaseServersByFilters",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	servers, err := proxy.FindDatabaseServersByFiltersForCluster(ctx, req, proxy.siteName)
	return servers, trace.Wrap(err)
}

// FindDatabaseServersByFiltersForCluster returns all registered database proxy servers in the provided cluster.
func (proxy *ProxyClient) FindDatabaseServersByFiltersForCluster(ctx context.Context, req proto.ListResourcesRequest, cluster string) ([]types.DatabaseServer, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindDatabaseServersByFiltersForCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", cluster),
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	req.ResourceType = types.KindDatabaseServer
	authClient, err := proxy.ConnectToCluster(ctx, cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	resources, err := client.GetResourcesWithFilters(ctx, authClient, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	servers, err := types.ResourcesWithLabels(resources).AsDatabaseServers()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return servers, nil
}

// FindDatabasesByFilters returns registered databases that match the provided
// filter in the current cluster.
func (proxy *ProxyClient) FindDatabasesByFilters(ctx context.Context, req proto.ListResourcesRequest) ([]types.Database, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindDatabasesByFilters",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	databases, err := proxy.FindDatabasesByFiltersForCluster(ctx, req, proxy.siteName)
	return databases, trace.Wrap(err)
}

// FindDatabasesByFiltersForCluster returns registered databases that match the provided
// filter in the provided cluster.
func (proxy *ProxyClient) FindDatabasesByFiltersForCluster(ctx context.Context, req proto.ListResourcesRequest, cluster string) ([]types.Database, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/FindDatabasesByFiltersForCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", req.ResourceType),
			attribute.Int("limit", int(req.Limit)),
			attribute.String("predicate", req.PredicateExpression),
			attribute.StringSlice("keywords", req.SearchKeywords),
		),
	)
	defer span.End()

	servers, err := proxy.FindDatabaseServersByFiltersForCluster(ctx, req, cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	databases := types.DatabaseServers(servers).ToDatabases()
	return types.DeduplicateDatabases(databases), nil
}

// ListResources returns a paginated list of resources.
func (proxy *ProxyClient) ListResources(ctx context.Context, namespace, resource, startKey string, limit int) ([]types.ResourceWithLabels, string, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/ListResources",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("resource", resource),
			attribute.Int("limit", limit),
		),
	)
	defer span.End()

	authClient := proxy.CurrentCluster()

	resp, err := authClient.ListResources(ctx, proto.ListResourcesRequest{
		Namespace:    namespace,
		ResourceType: resource,
		StartKey:     startKey,
		Limit:        int32(limit),
	})
	if err != nil {
		return nil, "", trace.Wrap(err)
	}
	return resp.Resources, resp.NextKey, nil
}

// sharedAuthClient is a wrapper around auth.ClientI which
// prevents the underlying client from being closed.
type sharedAuthClient struct {
	auth.ClientI
}

// Close is a no-op
func (a sharedAuthClient) Close() error {
	return nil
}

// CurrentCluster returns an authenticated auth server client for the local cluster.
func (proxy *ProxyClient) CurrentCluster() auth.ClientI {
	// The auth.ClientI is wrapped in an sharedAuthClient to prevent callers from
	// being able to close the client. The auth.ClientI is only to be closed
	// when the ProxyClient is closed.
	return sharedAuthClient{ClientI: proxy.currentCluster}
}

// ConnectToRootCluster connects to the auth server of the root cluster
// via proxy. It returns connected and authenticated auth server client
func (proxy *ProxyClient) ConnectToRootCluster(ctx context.Context) (auth.ClientI, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/ConnectToRootCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	clusterName, err := proxy.RootClusterName(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return proxy.ConnectToCluster(ctx, clusterName)
}

func (proxy *ProxyClient) loadTLS(clusterName string) (*tls.Config, error) {
	if proxy.teleportClient.SkipLocalAuth {
		return proxy.teleportClient.TLS.Clone(), nil
	}
	tlsKey, err := proxy.localAgent().GetCoreKey()
	if err != nil {
		return nil, trace.Wrap(err, "failed to fetch TLS key for %v", proxy.teleportClient.Username)
	}

	tlsConfig, err := tlsKey.TeleportClientTLSConfig(nil, []string{clusterName})
	if err != nil {
		return nil, trace.Wrap(err, "failed to generate client TLS config")
	}
	return tlsConfig.Clone(), nil
}

// ConnectToAuthServiceThroughALPNSNIProxy uses ALPN proxy service to connect to remote/local auth
// service and returns auth client. For routing purposes, TLS ServerName is set to destination auth service
// cluster name with ALPN values set to teleport-auth protocol.
func (proxy *ProxyClient) ConnectToAuthServiceThroughALPNSNIProxy(ctx context.Context, clusterName, proxyAddr string) (auth.ClientI, error) {
	tlsConfig, err := proxy.loadTLS(clusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if proxyAddr == "" {
		proxyAddr = proxy.teleportClient.WebProxyAddr
	}

	tlsConfig.InsecureSkipVerify = proxy.teleportClient.InsecureSkipVerify
	clt, err := auth.NewClient(client.Config{
		Context: ctx,
		Addrs:   []string{proxyAddr},
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
		ALPNSNIAuthDialClusterName: clusterName,
		CircuitBreakerConfig:       breaker.NoopBreakerConfig(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return clt, nil
}

func (proxy *ProxyClient) shouldDialWithTLSRouting(ctx context.Context) (string, bool) {
	if len(proxy.teleportClient.JumpHosts) > 0 {
		// Check if the provided JumpHost address is a Teleport Proxy.
		// This is needed to distinguish if the JumpHost address from Teleport Proxy Web address
		// or Teleport Proxy SSH address.
		jumpHostAddr := proxy.teleportClient.JumpHosts[0].Addr.String()
		resp, err := webclient.Find(
			&webclient.Config{
				Context:   ctx,
				ProxyAddr: jumpHostAddr,
				Insecure:  proxy.teleportClient.InsecureSkipVerify,
			},
		)
		if err != nil {
			// HTTP ping call failed. The JumpHost address is not a Teleport proxy address
			return "", false
		}
		return jumpHostAddr, resp.Proxy.TLSRoutingEnabled
	}
	return proxy.teleportClient.WebProxyAddr, proxy.teleportClient.TLSRoutingEnabled
}

// ConnectToCluster connects to the auth server of the given cluster via proxy.
// It returns connected and authenticated auth server client
func (proxy *ProxyClient) ConnectToCluster(ctx context.Context, clusterName string) (auth.ClientI, error) {
	// If connecting to the local cluster then return the already
	// established auth client instead of dialing it a second time.
	if clusterName == proxy.siteName && proxy.currentCluster != nil {
		return proxy.CurrentCluster(), nil
	}

	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/ConnectToCluster",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", clusterName),
		),
	)
	defer span.End()

	if proxyAddr, ok := proxy.shouldDialWithTLSRouting(ctx); ok {
		// If proxy supports multiplex listener mode dial root/leaf cluster auth service via ALPN Proxy
		// directly without using SSH tunnels.
		clt, err := proxy.ConnectToAuthServiceThroughALPNSNIProxy(ctx, clusterName, proxyAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return clt, nil
	}

	dialer := client.ContextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		// link the span created dialing the auth server to the one created above. grpc dialing
		// passes in a context.Background() during dial which causes these two spans to be in
		// different traces.
		ctx = oteltrace.ContextWithSpan(ctx, span)
		return proxy.dialAuthServer(ctx, clusterName)
	})

	if proxy.teleportClient.SkipLocalAuth {
		return auth.NewClient(client.Config{
			Context: ctx,
			Dialer:  dialer,
			Credentials: []client.Credentials{
				client.LoadTLS(proxy.teleportClient.TLS),
			},
			CircuitBreakerConfig: breaker.NoopBreakerConfig(),
		})
	}

	tlsKey, err := proxy.localAgent().GetCoreKey()
	if err != nil {
		return nil, trace.Wrap(err, "failed to fetch TLS key for %v", proxy.teleportClient.Username)
	}
	tlsConfig, err := tlsKey.TeleportClientTLSConfig(nil, []string{clusterName})
	if err != nil {
		return nil, trace.Wrap(err, "failed to generate client TLS config")
	}
	tlsConfig.InsecureSkipVerify = proxy.teleportClient.InsecureSkipVerify
	clt, err := auth.NewClient(client.Config{
		Context: ctx,
		Dialer:  dialer,
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
		CircuitBreakerConfig: breaker.NoopBreakerConfig(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return clt, nil
}

// NewTracingClient connects to the auth server of the given cluster via proxy.
// It returns a connected and authenticated tracing.Client that will export spans
// to the auth server, where they will be forwarded onto the configured exporter.
func (proxy *ProxyClient) NewTracingClient(ctx context.Context, clusterName string) (*tracing.Client, error) {
	dialer := client.ContextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return proxy.dialAuthServer(ctx, clusterName)
	})

	switch {
	case proxy.teleportClient.TLSRoutingEnabled:
		tlsConfig, err := proxy.loadTLS(clusterName)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		clt, err := client.NewTracingClient(ctx, client.Config{
			Addrs:            []string{proxy.teleportClient.WebProxyAddr},
			DialInBackground: true,
			Credentials: []client.Credentials{
				client.LoadTLS(tlsConfig),
			},
			ALPNSNIAuthDialClusterName: clusterName,
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return clt, nil
	case proxy.teleportClient.SkipLocalAuth:
		clt, err := client.NewTracingClient(ctx, client.Config{
			Dialer:           dialer,
			DialInBackground: true,
			Credentials: []client.Credentials{
				client.LoadTLS(proxy.teleportClient.TLS),
			},
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return clt, nil
	default:
		tlsKey, err := proxy.localAgent().GetCoreKey()
		if err != nil {
			return nil, trace.Wrap(err, "failed to fetch TLS key for %v", proxy.teleportClient.Username)
		}

		tlsConfig, err := tlsKey.TeleportClientTLSConfig(nil, []string{clusterName})
		if err != nil {
			return nil, trace.Wrap(err, "failed to generate client TLS config")
		}
		tlsConfig.InsecureSkipVerify = proxy.teleportClient.InsecureSkipVerify

		clt, err := client.NewTracingClient(ctx, client.Config{
			Dialer:           dialer,
			DialInBackground: true,
			Credentials: []client.Credentials{
				client.LoadTLS(tlsConfig),
			},
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return clt, nil
	}
}

// nodeName removes the port number from the hostname, if present
func nodeName(node string) string {
	n, _, err := net.SplitHostPort(node)
	if err != nil {
		return node
	}
	return n
}

type proxyResponse struct {
	isRecord bool
	err      error
}

// clusterDetails retrieves information about the current cluster needed to connect to nodes.
func (proxy *ProxyClient) clusterDetails(ctx context.Context) (sshutils.ClusterDetails, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/clusterDetails",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	var details sshutils.ClusterDetails
	ok, resp, err := proxy.Client.SendRequest(ctx, teleport.ClusterDetailsReqType, true, nil)
	if err != nil {
		return details, trace.Wrap(err)
	}

	// server understood the ClusterDetailsReqType
	if ok {
		err = ssh.Unmarshal(resp, &details)
		return details, trace.Wrap(err)
	}

	// talking to an older server which doesn't know about ClusterDetailsReqType
	// fallback to sending multiple requests
	recordingProxy, err := proxy.isRecordingProxy(ctx)
	if err != nil {
		return details, trace.Wrap(err)
	}

	pong, err := proxy.CurrentCluster().Ping(ctx)
	if err != nil {
		return details, trace.Wrap(err)
	}

	details.RecordingProxy = recordingProxy
	details.FIPSEnabled = pong.IsBoring

	return details, nil
}

// isRecordingProxy returns true if the proxy is in recording mode. Note, this
// function can only be called after authentication has occurred and should be
// called before the first session is created.
//
// DEPRECATED: clusterDetails should be used instead to avoid multiple round trips for
// cluster information.
// TODO(tross):DELETE IN 12.0
func (proxy *ProxyClient) isRecordingProxy(ctx context.Context) (bool, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/isRecordingProxy",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	responseCh := make(chan proxyResponse)

	// we have to run this in a goroutine because older version of Teleport handled
	// global out-of-band requests incorrectly: Teleport would ignore requests it
	// does not know about and never reply to them. So if we wait a second and
	// don't hear anything back, most likley we are trying to connect to an older
	// version of Teleport and we should not try and forward our agent.
	go func() {
		ok, responseBytes, err := proxy.Client.SendRequest(ctx, teleport.RecordingProxyReqType, true, nil)
		if err != nil {
			responseCh <- proxyResponse{isRecord: false, err: trace.Wrap(err)}
			return
		}
		if !ok {
			responseCh <- proxyResponse{isRecord: false, err: trace.AccessDenied("unable to determine proxy type")}
			return
		}

		recordingProxy, err := strconv.ParseBool(string(responseBytes))
		if err != nil {
			responseCh <- proxyResponse{isRecord: false, err: trace.Wrap(err)}
			return
		}

		responseCh <- proxyResponse{isRecord: recordingProxy, err: nil}
	}()

	select {
	case resp := <-responseCh:
		if resp.err != nil {
			return false, trace.Wrap(resp.err)
		}
		return resp.isRecord, nil
	case <-time.After(1 * time.Second):
		// probably the older version of the proxy or at least someone that is
		// responding incorrectly, don't forward agent to it
		return false, nil
	}
}

// dialAuthServer returns auth server connection forwarded via proxy
func (proxy *ProxyClient) dialAuthServer(ctx context.Context, clusterName string) (net.Conn, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/dialAuthServer",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("cluster", clusterName),
		),
	)
	defer span.End()

	log.Debugf("Client %v is connecting to auth server on cluster %q.", proxy.clientAddr, clusterName)

	address := "@" + clusterName

	// parse destination first:
	localAddr, err := utils.ParseAddr("tcp://" + proxy.proxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	fakeAddr, err := utils.ParseAddr("tcp://" + address)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	proxySession, err := proxy.Client.NewSession(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyWriter, err := proxySession.StdinPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyReader, err := proxySession.StdoutPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyErr, err := proxySession.StderrPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	err = proxySession.RequestSubsystem(ctx, "proxy:"+address)
	if err != nil {
		// read the stderr output from the failed SSH session and append
		// it to the end of our own message:
		serverErrorMsg, _ := io.ReadAll(proxyErr)
		return nil, trace.ConnectionProblem(err, "failed connecting to node %v. %s",
			nodeName(strings.Split(address, "@")[0]), serverErrorMsg)
	}
	return utils.NewPipeNetConn(
		proxyReader,
		proxyWriter,
		proxySession,
		localAddr,
		fakeAddr,
	), nil
}

// NodeDetails provides connection information for a node
type NodeDetails struct {
	// Addr is an address to dial
	Addr string
	// Namespace is the node namespace
	Namespace string
	// Cluster is the name of the target cluster
	Cluster string

	// MFACheck is optional parameter passed if MFA check was already done.
	// It can be nil.
	MFACheck *proto.IsMFARequiredResponse
}

// String returns a user-friendly name
func (n NodeDetails) String() string {
	parts := []string{nodeName(n.Addr)}
	if n.Cluster != "" {
		parts = append(parts, "on cluster", n.Cluster)
	}
	return strings.Join(parts, " ")
}

// ProxyFormat returns the address in the format
// used by the proxy subsystem
func (n *NodeDetails) ProxyFormat() string {
	parts := []string{n.Addr}
	if n.Namespace != "" {
		parts = append(parts, n.Namespace)
	}
	if n.Cluster != "" {
		parts = append(parts, n.Cluster)
	}
	return strings.Join(parts, "@")
}

// requestSubsystem sends a subsystem request on the session. If the passed
// in context is canceled first, unblocks.
func requestSubsystem(ctx context.Context, session *tracessh.Session, name string) error {
	errCh := make(chan error)

	go func() {
		er := session.RequestSubsystem(ctx, name)
		errCh <- er
	}()

	select {
	case err := <-errCh:
		return trace.Wrap(err)
	case <-ctx.Done():
		err := session.Close()
		if err != nil {
			log.Debugf("Failed to close session: %v.", err)
		}
		return trace.Wrap(ctx.Err())
	}
}

// ConnectToNode connects to the ssh server via Proxy.
// It returns connected and authenticated NodeClient
func (proxy *ProxyClient) ConnectToNode(ctx context.Context, nodeAddress NodeDetails, user string, details sshutils.ClusterDetails, authMethods []ssh.AuthMethod) (*NodeClient, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/ConnectToNode",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("node", nodeAddress.Addr),
			attribute.String("cluster", nodeAddress.Cluster),
			attribute.String("user", user),
		),
	)
	defer span.End()

	log.Infof("Client=%v connecting to node=%v", proxy.clientAddr, nodeAddress)
	if len(proxy.teleportClient.JumpHosts) > 0 {
		return proxy.PortForwardToNode(ctx, nodeAddress, user, details, authMethods)
	}

	// parse destination first:
	localAddr, err := utils.ParseAddr("tcp://" + proxy.proxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	fakeAddr, err := utils.ParseAddr("tcp://" + nodeAddress.Addr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	proxySession, err := proxy.Client.NewSession(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyWriter, err := proxySession.StdinPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyReader, err := proxySession.StdoutPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	proxyErr, err := proxySession.StderrPipe()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// pass the true client IP (if specified) to the proxy so it could pass it into the
	// SSH session for proper audit
	if len(proxy.clientAddr) > 0 {
		if err = proxySession.Setenv(ctx, sshutils.TrueClientAddrVar, proxy.clientAddr); err != nil {
			log.Error(err)
		}
	}

	// the client only tries to forward an agent when the proxy is in recording
	// mode. we always try and forward an agent here because each new session
	// creates a new context which holds the agent. if ForwardToAgent returns an error
	// "already have handler for" we ignore it.
	if details.RecordingProxy {
		if proxy.teleportClient.localAgent == nil {
			return nil, trace.BadParameter("cluster is in proxy recording mode and requires agent forwarding for connections, but no agent was initialized")
		}
		err = agent.ForwardToAgent(proxy.Client.Client, proxy.teleportClient.localAgent.ExtendedAgent)
		if err != nil && !strings.Contains(err.Error(), "agent: already have handler for") {
			return nil, trace.Wrap(err)
		}

		err = agent.RequestAgentForwarding(proxySession.Session)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	err = requestSubsystem(ctx, proxySession, "proxy:"+nodeAddress.ProxyFormat())
	if err != nil {
		// If the user pressed Ctrl-C, no need to try and read the error from
		// the proxy, return an error right away.
		if trace.Unwrap(err) == context.Canceled {
			return nil, trace.Wrap(err)
		}

		// read the stderr output from the failed SSH session and append
		// it to the end of our own message:
		serverErrorMsg, _ := io.ReadAll(proxyErr)
		return nil, trace.ConnectionProblem(err, "failed connecting to node %v. %s",
			nodeName(nodeAddress.Addr), serverErrorMsg)
	}

	pipeNetConn := utils.NewPipeNetConn(
		proxyReader,
		proxyWriter,
		proxySession,
		localAddr,
		fakeAddr,
	)

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: proxy.hostKeyCallback,
	}

	nc, err := NewNodeClient(ctx, sshConfig, pipeNetConn, nodeAddress.ProxyFormat(), proxy.teleportClient, details.FIPSEnabled)
	return nc, trace.Wrap(err)
}

// PortForwardToNode connects to the ssh server via Proxy
// It returns connected and authenticated NodeClient
func (proxy *ProxyClient) PortForwardToNode(ctx context.Context, nodeAddress NodeDetails, user string, details sshutils.ClusterDetails, authMethods []ssh.AuthMethod) (*NodeClient, error) {
	ctx, span := proxy.Tracer.Start(
		ctx,
		"proxyClient/PortForwardToNode",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("node", nodeAddress.Addr),
			attribute.String("cluster", nodeAddress.Cluster),
			attribute.String("user", user),
		),
	)
	defer span.End()

	log.Infof("Client=%v jumping to node=%s", proxy.clientAddr, nodeAddress)

	// the client only tries to forward an agent when the proxy is in recording
	// mode. we always try and forward an agent here because each new session
	// creates a new context which holds the agent. if ForwardToAgent returns an error
	// "already have handler for" we ignore it.
	if details.RecordingProxy {
		if proxy.teleportClient.localAgent == nil {
			return nil, trace.BadParameter("cluster is in proxy recording mode and requires agent forwarding for connections, but no agent was initialized")
		}
		err := agent.ForwardToAgent(proxy.Client.Client, proxy.teleportClient.localAgent.ExtendedAgent)
		if err != nil && !strings.Contains(err.Error(), "agent: already have handler for") {
			return nil, trace.Wrap(err)
		}
	}

	proxyConn, err := proxy.Client.DialContext(ctx, "tcp", nodeAddress.Addr)
	if err != nil {
		return nil, trace.ConnectionProblem(err, "failed connecting to node %v. %s", nodeAddress, err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: proxy.hostKeyCallback,
	}

	nc, err := NewNodeClient(ctx, sshConfig, proxyConn, nodeAddress.Addr, proxy.teleportClient, details.FIPSEnabled)
	return nc, trace.Wrap(err)
}

// NewNodeClient constructs a NodeClient that is connected to the node at nodeAddress
func NewNodeClient(ctx context.Context, sshConfig *ssh.ClientConfig, conn net.Conn, nodeAddress string, tc *TeleportClient, fipsEnabled bool) (*NodeClient, error) {
	ctx, span := tc.Tracer.Start(
		ctx,
		"NewNodeClient",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("node", nodeAddress),
		),
	)
	defer span.End()

	sshconn, chans, reqs, err := newClientConn(ctx, conn, nodeAddress, sshConfig)
	if err != nil {
		if utils.IsHandshakeFailedError(err) {
			conn.Close()
			log.Infof("Access denied to %v connecting to %v: %v", sshConfig.User, nodeAddress, err)
			return nil, trace.AccessDenied(`access denied to %v connecting to %v`, sshConfig.User, nodeAddress)
		}
		return nil, trace.Wrap(err)
	}

	// We pass an empty channel which we close right away to ssh.NewClient
	// because the client need to handle requests itself.
	emptyCh := make(chan *ssh.Request)
	close(emptyCh)

	nc := &NodeClient{
		Client:      tracessh.NewClient(sshconn, chans, emptyCh),
		Namespace:   apidefaults.Namespace,
		TC:          tc,
		Tracer:      tc.Tracer,
		FIPSEnabled: fipsEnabled,
	}

	// Start a goroutine that will run for the duration of the client to process
	// global requests from the client. Teleport clients will use this to update
	// terminal sizes when the remote PTY size has changed.
	go nc.handleGlobalRequests(ctx, reqs)

	return nc, nil
}

// RunInteractiveShell creates an interactive shell on the node and copies stdin/stdout/stderr
// to and from the node and local shell. This will block until the interactive shell on the node
// is terminated.
func (c *NodeClient) RunInteractiveShell(ctx context.Context, mode types.SessionParticipantMode, sessToJoin types.SessionTracker) error {
	ctx, span := c.Tracer.Start(
		ctx,
		"nodeClient/RunInteractiveShell",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	env := make(map[string]string)
	env[teleport.EnvSSHJoinMode] = string(mode)
	env[teleport.EnvSSHSessionReason] = c.TC.Config.Reason
	env[teleport.EnvSSHSessionDisplayParticipantRequirements] = strconv.FormatBool(c.TC.Config.DisplayParticipantRequirements)
	encoded, err := json.Marshal(&c.TC.Config.Invited)
	if err != nil {
		return trace.Wrap(err)
	}

	env[teleport.EnvSSHSessionInvited] = string(encoded)
	for key, value := range c.TC.Env {
		env[key] = value
	}

	nodeSession, err := newSession(ctx, c, sessToJoin, env, c.TC.Stdin, c.TC.Stdout, c.TC.Stderr, c.TC.EnableEscapeSequences)
	if err != nil {
		return trace.Wrap(err)
	}

	if err = nodeSession.runShell(ctx, mode, nil, c.TC.OnShellCreated); err != nil {
		switch e := trace.Unwrap(err).(type) {
		case *ssh.ExitError:
			c.TC.ExitStatus = e.ExitStatus()
		case *ssh.ExitMissingError:
			c.TC.ExitStatus = 1
		}

		return trace.Wrap(err)
	}

	if nodeSession.ExitMsg == "" {
		fmt.Fprintln(c.TC.Stderr, "the connection was closed on the remote side at ", time.Now().Format(time.RFC822))
	} else {
		fmt.Fprintln(c.TC.Stderr, nodeSession.ExitMsg)
	}

	return nil
}

func (c *NodeClient) handleGlobalRequests(ctx context.Context, requestCh <-chan *ssh.Request) {
	for {
		select {
		case r := <-requestCh:
			// When the channel is closing, nil is returned.
			if r == nil {
				return
			}

			switch r.Type {
			case teleport.MFAPresenceRequest:
				if c.OnMFA == nil {
					log.Warn("Received MFA presence request, but no callback was provided.")
					continue
				}

				c.OnMFA()
			case teleport.SessionEvent:
				// Parse event and create events.EventFields that can be consumed directly
				// by caller.
				var e events.EventFields
				err := json.Unmarshal(r.Payload, &e)
				if err != nil {
					log.Warnf("Unable to parse event: %v: %v.", string(r.Payload), err)
					continue
				}

				// Send event to event channel.
				err = c.TC.SendEvent(ctx, e)
				if err != nil {
					log.Warnf("Unable to send event %v: %v.", string(r.Payload), err)
					continue
				}
			default:
				// This handles keep-alive messages and matches the behavior of OpenSSH.
				err := r.Reply(false, nil)
				if err != nil {
					log.Warnf("Unable to reply to %v request.", r.Type)
					continue
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// newClientConn is a wrapper around ssh.NewClientConn
func newClientConn(
	ctx context.Context,
	conn net.Conn,
	nodeAddress string,
	config *ssh.ClientConfig,
) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	type response struct {
		conn   ssh.Conn
		chanCh <-chan ssh.NewChannel
		reqCh  <-chan *ssh.Request
		err    error
	}

	respCh := make(chan response, 1)
	go func() {
		// Use a noop text map propagator so that the tracing context isn't included in
		// the connection handshake. Since the provided conn will already include the tracing
		// context we don't want to send it again.
		conn, chans, reqs, err := tracessh.NewClientConn(ctx, conn, nodeAddress, config, tracing.WithTextMapPropagator(propagation.NewCompositeTextMapPropagator()))
		respCh <- response{conn, chans, reqs, err}
	}()

	select {
	case resp := <-respCh:
		if resp.err != nil {
			return nil, nil, nil, trace.Wrap(resp.err, "failed to connect to %q", nodeAddress)
		}
		return resp.conn, resp.chanCh, resp.reqCh, nil
	case <-ctx.Done():
		errClose := conn.Close()
		if errClose != nil {
			log.Error(errClose)
		}
		// drain the channel
		resp := <-respCh
		return nil, nil, nil, trace.ConnectionProblem(resp.err, "failed to connect to %q", nodeAddress)
	}
}

// Close closes the proxy and auth clients
func (proxy *ProxyClient) Close() error {
	return trace.NewAggregate(proxy.Client.Close(), proxy.currentCluster.Close())
}

// ExecuteSCP runs remote scp command(shellCmd) on the remote server and
// runs local scp handler using SCP Command
func (c *NodeClient) ExecuteSCP(ctx context.Context, cmd scp.Command) error {
	ctx, span := c.Tracer.Start(
		ctx,
		"nodeClient/ExecuteSCP",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	shellCmd, err := cmd.GetRemoteShellCmd()
	if err != nil {
		return trace.Wrap(err)
	}

	s, err := c.Client.NewSession(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	defer s.Close()

	stdin, err := s.StdinPipe()
	if err != nil {
		return trace.Wrap(err)
	}

	stdout, err := s.StdoutPipe()
	if err != nil {
		return trace.Wrap(err)
	}

	// Stream scp's stderr so tsh gets the verbose remote error
	// if the command fails
	stderr, err := s.StderrPipe()
	if err != nil {
		return trace.Wrap(err)
	}
	go io.Copy(os.Stderr, stderr)

	ch := utils.NewPipeNetConn(
		stdout,
		stdin,
		utils.MultiCloser(),
		&net.IPAddr{},
		&net.IPAddr{},
	)

	execC := make(chan error, 1)
	go func() {
		err := cmd.Execute(ch)
		if err != nil && !trace.IsEOF(err) {
			log.WithError(err).Warn("Failed to execute SCP command.")
		}
		stdin.Close()
		execC <- err
	}()

	runC := make(chan error, 1)
	go func() {
		err := s.Run(ctx, shellCmd)
		if err != nil && errors.Is(err, &ssh.ExitMissingError{}) {
			// TODO(dmitri): currently, if the session is aborted with (*session).Close,
			// the remote side cannot send exit-status and this error results.
			// To abort the session properly, Teleport needs to support `signal` request
			err = nil
		}
		runC <- err
	}()

	var runErr error
	select {
	case <-ctx.Done():
		if err := s.Close(); err != nil {
			log.WithError(err).Debug("Failed to close the SSH session.")
		}
		err, runErr = <-execC, <-runC
	case err = <-execC:
		runErr = <-runC
	case runErr = <-runC:
		err = <-execC
	}

	if runErr != nil && (err == nil || trace.IsEOF(err)) {
		err = runErr
	}
	if trace.IsEOF(err) {
		err = nil
	}
	return trace.Wrap(err)
}

// TransferFiles transfers files over SFTP.
func (c *NodeClient) TransferFiles(ctx context.Context, cfg *sftp.Config) error {
	ctx, span := c.Tracer.Start(
		ctx,
		"nodeClient/TransferFiles",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
	)
	defer span.End()

	return trace.Wrap(cfg.TransferFiles(ctx, c.Client.Client))
}

type netDialer interface {
	Dial(string, string) (net.Conn, error)
}

func proxyConnection(ctx context.Context, conn net.Conn, remoteAddr string, dialer netDialer) error {
	defer conn.Close()
	defer log.Debugf("Finished proxy from %v to %v.", conn.RemoteAddr(), remoteAddr)

	var (
		remoteConn net.Conn
		err        error
	)

	log.Debugf("Attempting to connect proxy from %v to %v.", conn.RemoteAddr(), remoteAddr)
	for attempt := 1; attempt <= 5; attempt++ {
		remoteConn, err = dialer.Dial("tcp", remoteAddr)
		if err != nil {
			log.Debugf("Proxy connection attempt %v: %v.", attempt, err)

			timer := time.NewTimer(time.Duration(100*attempt) * time.Millisecond)
			defer timer.Stop()

			// Wait and attempt to connect again, if the context has closed, exit
			// right away.
			select {
			case <-ctx.Done():
				return trace.Wrap(ctx.Err())
			case <-timer.C:
				continue
			}
		}
		// Connection established, break out of the loop.
		break
	}
	if err != nil {
		return trace.BadParameter("failed to connect to node: %v", remoteAddr)
	}
	defer remoteConn.Close()

	// Start proxying, close the connection if a problem occurs on either leg.
	errCh := make(chan error, 2)
	go func() {
		defer conn.Close()
		defer remoteConn.Close()

		_, err := io.Copy(conn, remoteConn)
		errCh <- err
	}()
	go func() {
		defer conn.Close()
		defer remoteConn.Close()

		_, err := io.Copy(remoteConn, conn)
		errCh <- err
	}()

	var errs []error
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil && err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
				log.Warnf("Failed to proxy connection: %v.", err)
				errs = append(errs, err)
			}
		case <-ctx.Done():
			return trace.Wrap(ctx.Err())
		}
	}

	return trace.NewAggregate(errs...)
}

// acceptWithContext calls "Accept" on the listener but will unblock when the
// context is canceled.
func acceptWithContext(ctx context.Context, l net.Listener) (net.Conn, error) {
	acceptCh := make(chan net.Conn, 1)
	errorCh := make(chan error, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			errorCh <- err
			return
		}
		acceptCh <- conn
	}()

	select {
	case conn := <-acceptCh:
		return conn, nil
	case err := <-errorCh:
		return nil, trace.Wrap(err)
	case <-ctx.Done():
		return nil, trace.Wrap(ctx.Err())
	}
}

// listenAndForward listens on a given socket and forwards all incoming
// commands to the remote address through the SSH tunnel.
func (c *NodeClient) listenAndForward(ctx context.Context, ln net.Listener, localAddr string, remoteAddr string) {
	defer ln.Close()

	log := log.WithField("localAddr", localAddr).WithField("remoteAddr", remoteAddr)

	log.Infof("Starting port forwarding")

	for ctx.Err() == nil {
		// Accept connections from the client.
		conn, err := acceptWithContext(ctx, ln)
		if err != nil {
			if ctx.Err() == nil {
				log.WithError(err).Errorf("Port forwarding failed.")
			}
			continue
		}

		// Proxy the connection to the remote address.
		go func() {
			// `err` must be a fresh variable, hence `:=` instead of `=`.
			if err := proxyConnection(ctx, conn, remoteAddr, c.Client); err != nil {
				log.WithError(err).Warnf("Failed to proxy connection.")
			}
		}()
	}

	log.WithError(ctx.Err()).Infof("Shutting down port forwarding.")
}

// dynamicListenAndForward listens for connections, performs a SOCKS5
// handshake, and then proxies the connection to the requested address.
func (c *NodeClient) dynamicListenAndForward(ctx context.Context, ln net.Listener, localAddr string) {
	defer ln.Close()

	log := log.WithField("localAddr", localAddr)

	log.Infof("Starting dynamic port forwarding.")

	for ctx.Err() == nil {
		// Accept connection from the client. Here the client is typically
		// something like a web browser or other SOCKS5 aware application.
		conn, err := acceptWithContext(ctx, ln)
		if err != nil {
			if ctx.Err() == nil {
				log.WithError(err).Errorf("Dynamic port forwarding (SOCKS5) failed.")
			}
			continue
		}

		// Perform the SOCKS5 handshake with the client to find out the remote
		// address to proxy.
		remoteAddr, err := socks.Handshake(conn)
		if err != nil {
			log.WithError(err).Errorf("SOCKS5 handshake failed.")
			if err = conn.Close(); err != nil {
				log.WithError(err).Errorf("Error closing failed proxy connection.")
			}
			continue
		}
		log.Debugf("SOCKS5 proxy forwarding requests to %v.", remoteAddr)

		// Proxy the connection to the remote address.
		go func() {
			// `err` must be a fresh variable, hence `:=` instead of `=`.
			if err := proxyConnection(ctx, conn, remoteAddr, c.Client); err != nil {
				log.WithError(err).Warnf("Failed to proxy connection.")
				if err = conn.Close(); err != nil {
					log.WithError(err).Errorf("Error closing failed proxy connection.")
				}
			}
		}()
	}

	log.WithError(ctx.Err()).Infof("Shutting down dynamic port forwarding.")
}

// GetRemoteTerminalSize fetches the terminal size of a given SSH session.
func (c *NodeClient) GetRemoteTerminalSize(ctx context.Context, sessionID string) (*term.Winsize, error) {
	ctx, span := c.Tracer.Start(
		ctx,
		"nodeClient/GetRemoteTerminalSize",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(attribute.String("session", sessionID)),
	)
	defer span.End()

	ok, payload, err := c.Client.SendRequest(ctx, teleport.TerminalSizeRequest, true, []byte(sessionID))
	if err != nil {
		return nil, trace.Wrap(err)
	} else if !ok {
		return nil, trace.BadParameter("failed to get terminal size")
	}

	ws := new(term.Winsize)
	err = json.Unmarshal(payload, ws)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return ws, nil
}

// Close closes client and it's operations
func (c *NodeClient) Close() error {
	return c.Client.Close()
}

func (proxy *ProxyClient) sessionSSHCertificate(ctx context.Context, nodeAddr NodeDetails) ([]ssh.AuthMethod, error) {
	if _, err := proxy.teleportClient.localAgent.GetKey(nodeAddr.Cluster); err != nil {
		if trace.IsNotFound(err) {
			// Either running inside the web UI in a proxy or using an identity
			// file. Fall back to whatever AuthMethod we currently have.
			return proxy.authMethods, nil
		}
		return nil, trace.Wrap(err)
	}

	key, err := proxy.IssueUserCertsWithMFA(
		ctx,
		ReissueParams{
			NodeName:       nodeName(nodeAddr.Addr),
			RouteToCluster: proxy.ClusterName(),
			MFACheck:       nodeAddr.MFACheck,
		},
		func(ctx context.Context, proxyAddr string, c *proto.MFAAuthenticateChallenge) (*proto.MFAAuthenticateResponse, error) {
			return proxy.teleportClient.PromptMFAChallenge(ctx, proxyAddr, c, nil /* applyOpts */)
		},
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	am, err := key.AsAuthMethod()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return []ssh.AuthMethod{am}, nil
}

// localAgent returns for the Teleport client's local agent.
func (proxy *ProxyClient) localAgent() *LocalKeyAgent {
	return proxy.teleportClient.LocalAgent()
}

// GetPaginatedSessions grabs up to 'max' sessions.
func GetPaginatedSessions(ctx context.Context, fromUTC, toUTC time.Time, pageSize int, order types.EventOrder, max int, authClient auth.ClientI) ([]apievents.AuditEvent, error) {
	prevEventKey := ""
	var sessions []apievents.AuditEvent
	for {
		if remaining := max - len(sessions); remaining < pageSize {
			pageSize = remaining
		}
		nextEvents, eventKey, err := authClient.SearchSessionEvents(fromUTC, toUTC,
			pageSize, order, prevEventKey, nil /* where condition */, "" /* session ID */)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		sessions = append(sessions, nextEvents...)
		if eventKey == "" || len(sessions) >= max {
			break
		}
		prevEventKey = eventKey
	}
	if max < len(sessions) {
		return sessions[:max], nil
	}
	return sessions, nil
}
