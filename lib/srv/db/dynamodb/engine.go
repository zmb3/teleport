/*

 Copyright 2022 Gravitational, Inc.

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

package dynamodb

import (
	"bufio"
	"context"
	"net"
	"net/http"

	"github.com/gravitational/trace"

	apievents "github.com/gravitational/teleport/api/types/events"
	apiaws "github.com/gravitational/teleport/api/utils/aws"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/srv/db/common"
	"github.com/gravitational/teleport/lib/srv/db/common/role"
	"github.com/gravitational/teleport/lib/utils"
	libaws "github.com/gravitational/teleport/lib/utils/aws"
)

func init() {
	common.RegisterEngine(newEngine, defaults.ProtocolDynamoDB)
}

// newEngine create new DynamoDB engine.
func newEngine(ec common.EngineConfig) common.Engine {
	return &Engine{
		EngineConfig:  ec,
		roundTrippers: make(map[string]http.RoundTripper),
	}
}

// Engine handles connections from DynamoDB clients coming from Teleport
// proxy over reverse tunnel.
type Engine struct {
	*libaws.SigningService
	// EngineConfig is the common database engine configuration.
	common.EngineConfig
	// clientConn is a client connection.
	clientConn net.Conn
	// sessionCtx is current session context.
	sessionCtx *common.Session
	// roundTrippers is a cache of RoundTrippers, mapped by service endpoint.
	// It is not guarded by a mutex, since requests are processed serially.
	roundTrippers map[string]http.RoundTripper
}

var _ common.Engine = (*Engine)(nil)

func (e *Engine) InitializeConnection(clientConn net.Conn, sessionCtx *common.Session) error {
	e.clientConn = clientConn
	e.sessionCtx = sessionCtx
	svc, err := libaws.NewSigningService(libaws.SigningServiceConfig{})
	if err != nil {
		return trace.Wrap(err)
	}
	e.SigningService = svc
	return nil
}

// SendError sends an error to DynamoDB client.
func (e *Engine) SendError(err error) {
	if e.clientConn == nil || err == nil || utils.IsOKNetworkError(err) {
		return
	}
	e.Log.Debugf("DynamoDB connection error: %v.", err)

	statusCode := trace.ErrorToCode(err)
	response := &http.Response{
		ProtoMajor: 1,
		ProtoMinor: 1,
		StatusCode: statusCode,
	}

	if err := response.Write(e.clientConn); err != nil {
		e.Log.WithError(err).Errorf("failed to send error response to DynamoDB client")
		return
	}
}

// HandleConnection authorizes the incoming client connection, connects to the
// target DynamoDB server and starts proxying requests between client/server.
func (e *Engine) HandleConnection(ctx context.Context, _ *common.Session) error {
	err := e.checkAccess(ctx, e.sessionCtx)
	e.Audit.OnSessionStart(e.Context, e.sessionCtx, err)
	if err != nil {
		return trace.Wrap(err)
	}
	defer e.Audit.OnSessionEnd(e.Context, e.sessionCtx)

	clientConnReader := bufio.NewReader(e.clientConn)
	for {
		req, err := http.ReadRequest(clientConnReader)
		if err != nil {
			return trace.Wrap(err)
		}

		if err := e.process(ctx, req); err != nil {
			return trace.Wrap(err)
		}
	}
}

// process reads request from connected dynamodb client, processes the requests/responses and send data back
// to the client.
func (e *Engine) process(ctx context.Context, req *http.Request) error {
	defer req.Body.Close()

	service, err := e.getService(req)
	if err != nil {
		return trace.Wrap(err)
	}
	signingName, err := apiaws.DynamoDBServiceToSigningName(service)
	if err != nil {
		return trace.Wrap(err)
	}
	uri, err := e.getTargetURI(req, service)
	if err != nil {
		return trace.Wrap(err)
	}
	// rewrite the request URL and headers.
	reqCopy := rewriteRequest(req, uri)

	region := e.sessionCtx.Database.GetAWS().Region
	accountID := e.sessionCtx.Database.GetAWS().AccountID
	signedReq, err := e.SigningService.SignRequest(reqCopy,
		libaws.SigningCtx{
			SigningName:   signingName,
			SigningRegion: region,
			Expiry:        e.sessionCtx.Identity.Expires,
			SessionName:   e.sessionCtx.Identity.Username,
			AWSRoleArn:    libaws.BuildRoleARN(e.sessionCtx.DatabaseUser, region, accountID),
		})
	if err != nil {
		return trace.Wrap(err)
	}

	rt, err := e.getRoundTripper(ctx, uri)
	if err != nil {
		return trace.Wrap(err)
	}

	// Send the request to DynamoDB API.
	resp, err := rt.RoundTrip(signedReq)
	if err != nil {
		return trace.Wrap(err)
	}
	defer resp.Body.Close()

	// set the signed request body again for further processing, since ServeHTTP should have closed it.
	e.emitAuditEvent(reqCopy, uri, resp.StatusCode)
	return trace.Wrap(e.sendResponse(resp))
}

// sendResponse sends the response back to the DynamoDB client.
func (e *Engine) sendResponse(resp *http.Response) error {
	return trace.Wrap(resp.Write(e.clientConn))
}

// emitAuditEvent writes the request and response status code to the audit stream.
func (e *Engine) emitAuditEvent(req *http.Request, uri string, statusCode int) {
	// Try to read the body and JSON unmarshal it.
	// If this fails, we still want to emit the rest of the event info; the request event Body is nullable, so it's ok if body is left nil here.
	body, err := libaws.UnmarshalRequestBody(req)
	if err != nil {
		e.Log.WithError(err).Warn("Failed to read request body as JSON, omitting the body from the audit event.")
	}
	// get the API target from the request header, according to the API request format documentation:
	// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.LowLevelAPI.html#Programming.LowLevelAPI.RequestFormat
	target := req.Header.Get(libaws.TargetHeader)
	event := &apievents.DynamoDBRequest{
		Metadata: apievents.Metadata{
			Type: events.DatabaseSessionDynamoDBRequestEvent,
			Code: events.DynamoDBRequestCode,
		},
		UserMetadata:    e.sessionCtx.Identity.GetUserMetadata(),
		SessionMetadata: common.MakeSessionMetadata(e.sessionCtx),
		DatabaseMetadata: apievents.DatabaseMetadata{
			DatabaseService:  e.sessionCtx.Database.GetName(),
			DatabaseProtocol: e.sessionCtx.Database.GetProtocol(),
			DatabaseURI:      uri,
			DatabaseName:     e.sessionCtx.DatabaseName,
			DatabaseUser:     e.sessionCtx.DatabaseUser,
		},
		StatusCode: uint32(statusCode),
		Path:       req.URL.Path,
		RawQuery:   req.URL.RawQuery,
		Method:     req.Method,
		Target:     target,
		Body:       body,
	}
	e.Audit.EmitEvent(e.Context, event)
}

// checkAccess does authorization check for DynamoDB connection about
// to be established.
func (e *Engine) checkAccess(ctx context.Context, sessionCtx *common.Session) error {
	ap, err := e.Auth.GetAuthPreference(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	mfaParams := sessionCtx.MFAParams(ap.GetRequireMFAType())
	dbRoleMatchers := role.DatabaseRoleMatchers(
		sessionCtx.Database.GetProtocol(),
		sessionCtx.DatabaseUser,
		sessionCtx.DatabaseName,
	)
	err = sessionCtx.Checker.CheckAccess(
		sessionCtx.Database,
		mfaParams,
		dbRoleMatchers...,
	)
	if err != nil {
		e.Audit.OnSessionStart(e.Context, sessionCtx, err)
		return trace.Wrap(err)
	}
	return nil
}

// getRoundTripper makes an HTTP client with TLS config based on the given URI host.
func (e *Engine) getRoundTripper(ctx context.Context, uri string) (http.RoundTripper, error) {
	if rt, ok := e.roundTrippers[uri]; ok {
		return rt, nil
	}
	tlsConfig, err := e.Auth.GetTLSConfig(ctx, e.sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if tlsConfig.ServerName == "" {
		addr, err := utils.ParseAddr(uri)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		tlsConfig.ServerName = addr.Host()
	}
	transport, err := defaults.Transport()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	transport.TLSClientConfig = tlsConfig
	e.roundTrippers[uri] = transport
	return transport, nil
}

// getService extracts the service ID from the request header X-Amz-Target.
func (e *Engine) getService(req *http.Request) (string, error) {
	// try to get the service from x-amz-target header.
	target := req.Header.Get(libaws.TargetHeader)
	service, err := apiaws.ParseDynamoDBServiceFromTarget(target)
	return service, trace.Wrap(err)
}

// getTargetURI returns the target URI constructed from configured region and a given service.
func (e *Engine) getTargetURI(req *http.Request, service string) (string, error) {
	endpoint, err := apiaws.DynamoDBEndpointFromRegionAndService(e.sessionCtx.Database.GetAWS().Region, service)
	if err != nil {
		return "", trace.Wrap(err)
	}
	return endpoint, nil
}

// rewriteRequest rewrites the request to modify headers and the URL.
func rewriteRequest(r *http.Request, uri string) *http.Request {
	reqCopy := &http.Request{}
	*reqCopy = *r
	reqCopy.Header = r.Header.Clone()

	// set url to match the database uri.
	u := *r.URL
	u.Host = uri
	u.Scheme = "https"
	reqCopy.URL = &u

	for key := range reqCopy.Header {
		// Remove Content-Length header for SigV4 signing.
		if http.CanonicalHeaderKey(key) == "Content-Length" {
			reqCopy.Header.Del(key)
		}
	}
	return reqCopy
}
