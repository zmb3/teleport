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

package helpers

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/lib/httplib/csrf"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/web"
	"github.com/gravitational/teleport/lib/web/ui"
)

// WebClientPack is an authenticated HTTP Client for Teleport.
type WebClientPack struct {
	clt         *http.Client
	host        string
	webCookie   string
	bearerToken string
	clusterName string
}

// LoginWebClient receives the host url, the username and a password.
// It will login into that host and return a WebClientPack.
func LoginWebClient(t *testing.T, host, username, password string) *WebClientPack {
	csReq, err := json.Marshal(web.CreateSessionReq{
		User: username,
		Pass: password,
	})
	require.NoError(t, err)

	// Create POST request to create session.
	u := url.URL{
		Scheme: "https",
		Host:   host,
		Path:   "/v1/webapi/sessions/web",
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewBuffer(csReq))
	require.NoError(t, err)

	// Attach CSRF token in cookie and header.
	csrfToken, err := utils.CryptoRandomHex(32)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{
		Name:  csrf.CookieName,
		Value: csrfToken,
	})
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set(csrf.HeaderName, csrfToken)

	// Issue request.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Read in response.
	var csResp *web.CreateSessionResponse
	err = json.NewDecoder(resp.Body).Decode(&csResp)
	require.NoError(t, err)

	// Extract session cookie and bearer token.
	require.Len(t, resp.Cookies(), 1)
	cookie := resp.Cookies()[0]
	require.Equal(t, cookie.Name, web.CookieName)

	webClient := &WebClientPack{
		clt:         client,
		host:        host,
		webCookie:   cookie.Value,
		bearerToken: csResp.Token,
	}

	resp, err = webClient.DoRequest(http.MethodGet, "sites", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	var clusters []ui.Cluster
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&clusters))
	require.NotEmpty(t, clusters)

	webClient.clusterName = clusters[0].Name
	return webClient
}

// DoRequest receives a method, endpoint and payload and sends an HTTP Request to the Teleport API.
// The endpoint must not contain the host neither the base path ('/v1/webapi/').
// Returns the http.Response.
func (w *WebClientPack) DoRequest(method, endpoint string, payload any) (*http.Response, error) {
	endpoint = strings.ReplaceAll(endpoint, "$site", w.clusterName)
	u := url.URL{
		Scheme: "https",
		Host:   w.host,
		Path:   fmt.Sprintf("/v1/webapi/%s", endpoint),
	}

	bs, err := json.Marshal(payload)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	req, err := http.NewRequest(method, u.String(), bytes.NewBuffer(bs))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	req.AddCookie(&http.Cookie{
		Name:  web.CookieName,
		Value: w.webCookie,
	})
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", w.bearerToken))
	req.Header.Add("Content-Type", "application/json")

	resp, err := w.clt.Do(req)
	return resp, trace.Wrap(err)
}
