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

package aws

import (
	"bytes"
	"io"
	"net/http"
	"net/url"

	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/gravitational/oxy/forward"
	oxyutils "github.com/gravitational/oxy/utils"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/httplib"
	"github.com/gravitational/teleport/lib/srv/app/common"
	awsutils "github.com/gravitational/teleport/lib/utils/aws"
)

// signerHandler is an http.Handler for signing and forwarding requests to AWS API.
type signerHandler struct {
	// fwd is a Forwarder used to forward signed requests to AWS API.
	fwd *forward.Forwarder
	// AwsSignerHandlerConfig is the awsSignerHandler configuration.
	SignerHandlerConfig
}

// SignerHandlerConfig is the awsSignerHandler configuration.
type SignerHandlerConfig struct {
	// Log is a logger for the handler.
	Log logrus.FieldLogger
	// RoundTripper is an http.RoundTripper instance used for requests.
	RoundTripper http.RoundTripper
	*awsutils.SigningService
	*common.SessionContext
}

// CheckAndSetDefaults validates the AwsSignerHandlerConfig.
func (cfg *SignerHandlerConfig) CheckAndSetDefaults() error {
	if cfg.SigningService == nil {
		return trace.BadParameter("missing SigningService")
	}
	if cfg.SessionContext == nil {
		return trace.BadParameter("missing SessionContext")
	}
	if err := cfg.SessionContext.Check(); err != nil {
		return trace.Wrap(err)
	}
	if cfg.RoundTripper == nil {
		tr, err := defaults.Transport()
		if err != nil {
			return trace.Wrap(err)
		}
		cfg.RoundTripper = tr
	}
	if cfg.Log == nil {
		cfg.Log = logrus.WithField(trace.Component, "aws:signer")
	}
	return nil
}

// NewAWSSignerHandler creates a new request handler for signing and forwarding requests to AWS API.
func NewAWSSignerHandler(config SignerHandlerConfig) (http.Handler, error) {
	if err := config.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	handler := &signerHandler{
		SignerHandlerConfig: config,
	}
	fwd, err := forward.New(
		forward.RoundTripper(config.RoundTripper),
		forward.ErrorHandler(oxyutils.ErrorHandlerFunc(handler.formatForwardResponseError)),
		forward.PassHostHeader(true),
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	handler.fwd = fwd
	return handler, nil
}

// formatForwardResponseError converts an error to a status code and writes the code to a response.
func (s *signerHandler) formatForwardResponseError(rw http.ResponseWriter, r *http.Request, err error) {
	// Convert trace error type to HTTP and write response.
	code := trace.ErrorToCode(err)
	s.Log.WithError(err).Debugf("Failed to process request. Response status code: %v.", code)
	rw.WriteHeader(code)
}

// ServeHTTP handles incoming requests by signing them and then forwarding them to the proper AWS API.
func (s *signerHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// It's important that we resolve the endpoint before modifying the request headers,
	// as they may be needed to resolve the endpoint correctly.
	re, err := awsutils.ResolveEndpoint(req)
	if err != nil {
		s.formatForwardResponseError(w, req, err)
		return
	}

	// rewrite headers before signing the request to avoid signature validation problems.
	reqCopy, err := rewriteRequest(req, re)
	if err != nil {
		s.formatForwardResponseError(w, req, err)
		return
	}

	signedReq, payload, err := s.SignRequest(reqCopy,
		awsutils.SigningCtx{
			SigningName:   re.SigningName,
			SigningRegion: re.SigningRegion,
			Expiry:        s.Identity.Expires,
			SessionName:   s.Identity.Username,
			AWSRoleArn:    s.Identity.RouteToApp.AWSRoleARN,
			AWSExternalID: s.App.GetAWSExternalID(),
		})
	if err != nil {
		s.formatForwardResponseError(w, reqCopy, err)
		return
	}
	recorder := httplib.NewResponseStatusRecorder(w)
	s.fwd.ServeHTTP(recorder, signedReq)
	// set the signed request body again for further processing, since ServeHTTP should have closed it.
	reqCopy.Body = io.NopCloser(bytes.NewReader(payload))
	if awsutils.IsDynamoDBResolvedEndpoint(re) {
		err = s.Audit.OnDynamoDBRequest(reqCopy.Context(), s.SessionContext, signedReq, recorder.Status(), re)
	} else {
		err = s.Audit.OnRequest(reqCopy.Context(), s.SessionContext, signedReq, recorder.Status(), re)
	}
	if err != nil {
		s.Log.WithError(err).Warn("Failed to emit audit event.")
	}
}

// rewriteRequest rewrites a request to remove Teleport reserved headers and set the url.
func rewriteRequest(r *http.Request, re *endpoints.ResolvedEndpoint) (*http.Request, error) {
	// shallow copy request and make a deep copy for header modification.
	reqCopy := &http.Request{}
	*reqCopy = *r
	reqCopy.Header = r.Header.Clone()
	u, err := urlForResolvedEndpoint(r, re)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	reqCopy.URL = u

	for key := range reqCopy.Header {
		// Remove Teleport app headers.
		if common.IsReservedHeader(key) || http.CanonicalHeaderKey(key) == "Content-Length" {
			reqCopy.Header.Del(key)
		}
	}
	return reqCopy, nil
}

// urlForResolvedEndpoint creates an URL based on input request and resolved endpoint.
func urlForResolvedEndpoint(r *http.Request, re *endpoints.ResolvedEndpoint) (*url.URL, error) {
	resolvedURL, err := url.Parse(re.URL)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Replaces scheme and host. Keeps original path etc.
	clone := *r.URL
	if resolvedURL.Host != "" {
		clone.Host = resolvedURL.Host
	}
	if resolvedURL.Scheme != "" {
		clone.Scheme = resolvedURL.Scheme
	}
	return &clone, nil
}
