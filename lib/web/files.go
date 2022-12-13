/*
Copyright 2018 Gravitational, Inc.

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

package web

import (
	"net/http"

	"github.com/gravitational/trace"
	"github.com/julienschmidt/httprouter"

	"github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/reversetunnel"
	"github.com/zmb3/teleport/lib/sshutils/scp"
)

// fileTransferRequest describes HTTP file transfer request
type fileTransferRequest struct {
	// Server describes a server to connect to (serverId|hostname[:port]).
	server string
	// Login is Linux username to connect as.
	login string
	// Namespace is node namespace.
	namespace string
	// Cluster is the name of the remote cluster to connect to.
	cluster string
	// remoteLocation is file remote location
	remoteLocation string
	// filename is a file name
	filename string
}

func (h *Handler) transferFile(w http.ResponseWriter, r *http.Request, p httprouter.Params, sctx *SessionContext, site reversetunnel.RemoteSite) (interface{}, error) {
	query := r.URL.Query()
	req := fileTransferRequest{
		cluster:        p.ByName("site"),
		login:          p.ByName("login"),
		server:         p.ByName("server"),
		remoteLocation: query.Get("location"),
		filename:       query.Get("filename"),
		namespace:      defaults.Namespace,
	}

	clt, err := sctx.GetUserClient(r.Context(), site)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ft := fileTransfer{
		ctx:           sctx,
		authClient:    clt,
		proxyHostPort: h.ProxyHostPort(),
	}

	isUpload := r.Method == http.MethodPost
	if isUpload {
		err = ft.upload(req, r)
	} else {
		err = ft.download(req, r, w)
	}

	if err != nil {
		return nil, trace.Wrap(err)
	}

	// We must return nil so that we don't write anything to
	// the response, which would corrupt the downloaded file.
	return nil, nil
}

type fileTransfer struct {
	// ctx is a web session context for the currently logged in user.
	ctx           *SessionContext
	authClient    auth.ClientI
	proxyHostPort string
}

func (f *fileTransfer) download(req fileTransferRequest, httpReq *http.Request, w http.ResponseWriter) error {
	cmd, err := scp.CreateHTTPDownload(scp.HTTPTransferRequest{
		RemoteLocation: req.remoteLocation,
		HTTPResponse:   w,
		User:           f.ctx.GetUser(),
	})
	if err != nil {
		return trace.Wrap(err)
	}

	tc, err := f.createClient(req, httpReq)
	if err != nil {
		return trace.Wrap(err)
	}

	err = tc.ExecuteSCP(httpReq.Context(), cmd)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (f *fileTransfer) upload(req fileTransferRequest, httpReq *http.Request) error {
	cmd, err := scp.CreateHTTPUpload(scp.HTTPTransferRequest{
		RemoteLocation: req.remoteLocation,
		FileName:       req.filename,
		HTTPRequest:    httpReq,
		User:           f.ctx.GetUser(),
	})
	if err != nil {
		return trace.Wrap(err)
	}

	tc, err := f.createClient(req, httpReq)
	if err != nil {
		return trace.Wrap(err)
	}

	err = tc.ExecuteSCP(httpReq.Context(), cmd)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (f *fileTransfer) createClient(req fileTransferRequest, httpReq *http.Request) (*client.TeleportClient, error) {
	if !types.IsValidNamespace(req.namespace) {
		return nil, trace.BadParameter("invalid namespace %q", req.namespace)
	}

	if req.login == "" {
		return nil, trace.BadParameter("missing login")
	}

	servers, err := f.authClient.GetNodes(httpReq.Context(), req.namespace)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	hostName, hostPort, err := resolveServerHostPort(req.server, servers)
	if err != nil {
		return nil, trace.BadParameter("invalid server name %q: %v", req.server, err)
	}

	cfg, err := makeTeleportClientConfig(httpReq.Context(), f.ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cfg.HostLogin = req.login
	cfg.SiteName = req.cluster
	cfg.Namespace = req.namespace
	if err := cfg.ParseProxyHost(f.proxyHostPort); err != nil {
		return nil, trace.BadParameter("failed to parse proxy address: %v", err)
	}
	cfg.Host = hostName
	cfg.HostPort = hostPort
	cfg.ClientAddr = httpReq.RemoteAddr

	tc, err := client.NewClient(cfg)
	if err != nil {
		return nil, trace.BadParameter("failed to create client: %v", err)
	}

	return tc, nil
}
