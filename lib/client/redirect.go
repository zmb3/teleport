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

package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/google/uuid"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/secret"
	"github.com/zmb3/teleport/lib/utils"
)

const (
	// LoginSuccessRedirectURL is a redirect URL when login was successful without errors.
	LoginSuccessRedirectURL = "/web/msg/info/login_success"

	// LoginFailedRedirectURL is the default redirect URL when an SSO error was encountered.
	LoginFailedRedirectURL = "/web/msg/error/login"

	// LoginFailedBadCallbackRedirectURL is a redirect URL when an SSO error specific to
	// auth connector's callback was encountered.
	LoginFailedBadCallbackRedirectURL = "/web/msg/error/login/callback"

	// LoginFailedUnauthorizedRedirectURL is a redirect URL for when an SSO authenticates successfully,
	// but the user has no matching roles in Teleport.
	LoginFailedUnauthorizedRedirectURL = "/web/msg/error/login/auth"
)

// Redirector handles SSH redirect flow with the Teleport server
type Redirector struct {
	// SSHLoginSSO contains SSH login parameters
	SSHLoginSSO
	server *httptest.Server
	mux    *http.ServeMux
	// redirectURL will be set based on the response from the Teleport
	// proxy server, will contain target redirect URL
	// to launch SSO workflow
	redirectURL utils.SyncString
	// key is a secret key used to encode/decode
	// the data with the server, it is used so that other
	// programs running on the same computer can't easilly sniff
	// the data
	key secret.Key
	// shortPath is a link-shortener path presented to the user
	// it is used to open up the browser window, notice
	// that redirectURL will be set later
	shortPath string
	// responseC is a channel to receive responses
	responseC chan *auth.SSHLoginResponse
	// errorC will contain errors
	errorC chan error
	// proxyClient is HTTP client to the Teleport Proxy
	proxyClient *WebClient
	// proxyURL is a URL to the Teleport Proxy
	proxyURL *url.URL
	// context is a close context
	context context.Context
	// cancel broadcasts cancel
	cancel context.CancelFunc
	// RedirectorConfig allows customization of Redirector
	RedirectorConfig
}

// RedirectorConfig allows customization of Redirector
type RedirectorConfig struct {
	// SSOLoginConsoleRequestFn allows customizing issuance of SSOLoginConsoleReq. Optional.
	SSOLoginConsoleRequestFn func(req SSOLoginConsoleReq) (*SSOLoginConsoleResponse, error)
}

// NewRedirector returns new local web server redirector
func NewRedirector(ctx context.Context, login SSHLoginSSO, config *RedirectorConfig) (*Redirector, error) {
	clt, proxyURL, err := initClient(login.ProxyAddr, login.Insecure, login.Pool)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Create secret key that will be sent with the request and then used the
	// decrypt the response from the server.
	key, err := secret.NewKey()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	rd := &Redirector{
		context:     ctxCancel,
		cancel:      cancel,
		proxyClient: clt,
		proxyURL:    proxyURL,
		SSHLoginSSO: login,
		mux:         http.NewServeMux(),
		key:         key,
		shortPath:   "/" + uuid.New().String(),
		responseC:   make(chan *auth.SSHLoginResponse, 1),
		errorC:      make(chan error, 1),
	}

	if config != nil {
		rd.RedirectorConfig = *config
	}

	if rd.SSOLoginConsoleRequestFn == nil {
		rd.SSOLoginConsoleRequestFn = rd.issueSSOLoginConsoleRequest
	}

	// callback is a callback URL communicated to the Teleport proxy,
	// after SAML/OIDC login, the teleport will redirect user's browser
	// to this laptop-local URL
	rd.mux.Handle("/callback", rd.wrapCallback(rd.callback))
	// short path is a link-shortener style URL
	// that will redirect to the Teleport-Proxy supplied address
	rd.mux.HandleFunc(rd.shortPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, rd.redirectURL.Value(), http.StatusFound)
	})
	return rd, nil
}

// Start launches local http server on the machine,
// initiates SSO login request sequence with the Teleport Proxy
func (rd *Redirector) Start() error {
	if rd.BindAddr != "" {
		log.Debugf("Binding to %v.", rd.BindAddr)
		listener, err := net.Listen("tcp", rd.BindAddr)
		if err != nil {
			return trace.Wrap(err, "%v: could not bind to %v, make sure the address is host:port format for ipv4 and [ipv6]:port format for ipv6, and the address is not in use", err, rd.BindAddr)
		}
		rd.server = &httptest.Server{
			Listener: listener,
			Config:   &http.Server{Handler: rd.mux},
		}
		rd.server.Start()
	} else {
		rd.server = httptest.NewServer(rd.mux)
	}
	log.Infof("Waiting for response at: %v.", rd.server.URL)

	// communicate callback redirect URL to the Teleport Proxy
	u, err := url.Parse(rd.server.URL + "/callback")
	if err != nil {
		return trace.Wrap(err)
	}
	query := u.Query()
	query.Set("secret_key", rd.key.String())
	u.RawQuery = query.Encode()

	req := SSOLoginConsoleReq{
		RedirectURL:          u.String(),
		PublicKey:            rd.PubKey,
		CertTTL:              rd.TTL,
		ConnectorID:          rd.ConnectorID,
		Compatibility:        rd.Compatibility,
		RouteToCluster:       rd.RouteToCluster,
		KubernetesCluster:    rd.KubernetesCluster,
		AttestationStatement: rd.AttestationStatement,
	}

	response, err := rd.SSOLoginConsoleRequestFn(req)
	if err != nil {
		return trace.Wrap(err)
	}

	// notice late binding of the redirect URL here, it is referenced
	// in the callback handler, but is known only after the request
	// is sent to the Teleport Proxy, that's why
	// redirectURL is a SyncString
	rd.redirectURL.Set(response.RedirectURL)
	return nil
}

// issueSSOLoginConsoleRequest is default implementation, but may be overridden via RedirectorConfig.IssueSSOLoginConsoleRequest.
func (rd *Redirector) issueSSOLoginConsoleRequest(req SSOLoginConsoleReq) (*SSOLoginConsoleResponse, error) {
	out, err := rd.proxyClient.PostJSON(rd.context, rd.proxyClient.Endpoint("webapi", rd.Protocol, "login", "console"), req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var re *SSOLoginConsoleResponse
	err = json.Unmarshal(out.Bytes(), &re)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return re, nil
}

// Done is called when redirector is closed
// or parent context is closed
func (rd *Redirector) Done() <-chan struct{} {
	return rd.context.Done()
}

// ClickableURL returns a short clickable redirect URL
func (rd *Redirector) ClickableURL() string {
	if rd.server == nil {
		return "<undefined - server is not started>"
	}
	return utils.ClickableURL(rd.server.URL + rd.shortPath)
}

// ResponseC returns a channel with response
func (rd *Redirector) ResponseC() <-chan *auth.SSHLoginResponse {
	return rd.responseC
}

// ErrorC returns a channel with error
func (rd *Redirector) ErrorC() <-chan error {
	return rd.errorC
}

// callback is used by Teleport proxy to send back credentials
// issued by Teleport proxy
func (rd *Redirector) callback(w http.ResponseWriter, r *http.Request) (*auth.SSHLoginResponse, error) {
	if r.URL.Path != "/callback" {
		return nil, trace.NotFound("path not found")
	}

	if r.URL.Query().Has("err") {
		err := r.URL.Query().Get("err")
		return nil, trace.Errorf("identity provider callback failed with error: %v", err)
	}

	// Decrypt ciphertext to get login response.
	plaintext, err := rd.key.Open([]byte(r.URL.Query().Get("response")))
	if err != nil {
		return nil, trace.BadParameter("failed to decrypt response: in %v, err: %v", r.URL.String(), err)
	}

	var re *auth.SSHLoginResponse
	err = json.Unmarshal(plaintext, &re)
	if err != nil {
		return nil, trace.BadParameter("failed to decrypt response: in %v, err: %v", r.URL.String(), err)
	}

	return re, nil
}

// Close closes redirector and releases all resources
func (rd *Redirector) Close() error {
	rd.cancel()
	if rd.server != nil {
		rd.server.Close()
	}
	return nil
}

// wrapCallback is a helper wrapper method that wraps callback HTTP handler
// and sends a result to the channel and redirect users to error page
func (rd *Redirector) wrapCallback(fn func(http.ResponseWriter, *http.Request) (*auth.SSHLoginResponse, error)) http.Handler {
	clone := *rd.proxyURL
	clone.Path = LoginFailedRedirectURL
	errorURL := clone.String()
	clone.Path = LoginSuccessRedirectURL
	successURL := clone.String()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := fn(w, r)
		if err != nil {
			if trace.IsNotFound(err) {
				http.NotFound(w, r)
				return
			}
			select {
			case rd.errorC <- err:
			case <-rd.context.Done():
				http.Redirect(w, r, errorURL, http.StatusFound)
				return
			}
			http.Redirect(w, r, errorURL, http.StatusFound)
			return
		}
		select {
		case rd.responseC <- response:
		case <-rd.context.Done():
			http.Redirect(w, r, errorURL, http.StatusFound)
			return
		}
		http.Redirect(w, r, successURL, http.StatusFound)
	})
}
