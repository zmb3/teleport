/*
Copyright 2021-2022 Gravitational, Inc.

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

package auth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	awssdk "github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/coreos/go-semver/semver"
	"github.com/gravitational/trace"
	"golang.org/x/exp/slices"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/api/client"
	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/types"
	cloudaws "github.com/gravitational/teleport/lib/cloud/aws"
	"github.com/gravitational/teleport/lib/utils/aws"
)

const (
	// Hardcoding the sts API version here may be more strict than necessary,
	// but this is set by the Teleport node and can only be changed when we
	// update our AWS SDK dependency. Since Auth should always be upgraded
	// before nodes, we will have a chance to update the check on Auth if we
	// ever have a need to allow a newer API version.
	expectedSTSIdentityRequestBody = "Action=GetCallerIdentity&Version=2011-06-15"

	// AWS SignedHeaders will always be lowercase
	// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-auth-using-authorization-header.html#sigv4-auth-header-overview
	challengeHeaderKey = "x-teleport-challenge"
)

var (
	authTeleportVersion = semver.New(teleport.Version)
)

// validateSTSHost returns an error if the given stsHost is not a valid regional
// endpoint for the AWS STS service, or nil if it is valid. If fips is true, the
// endpoint must be a valid FIPS endpoint.
//
// This is a security-critical check: we are allowing the client to tell us
// which URL we should use to validate their identity. If the client could pass
// off an attacker-controlled URL as the STS endpoint, the entire security
// mechanism of the IAM join method would be compromised.
//
// To keep this validation simple and secure, we check the given endpoint
// against a static list of known valid endpoints. We will need to update this
// list as AWS adds new regions.
func validateSTSHost(stsHost string, cfg *iamRegisterConfig) error {
	valid := slices.Contains(validSTSEndpoints, stsHost)
	if !valid {
		return trace.AccessDenied("IAM join request uses unknown STS host %q. "+
			"This could mean that the Teleport Node attempting to join the cluster is "+
			"running in a new AWS region which is unknown to this Teleport auth server. "+
			"Alternatively, if this URL looks suspicious, an attacker may be attempting to "+
			"join your Teleport cluster. "+
			"Following is the list of valid STS endpoints known to this auth server. "+
			"If a legitimate STS endpoint is not included, please file an issue at "+
			"https://github.com/gravitational/teleport. %v",
			stsHost, validSTSEndpoints)
	}

	if cfg.fips && !slices.Contains(fipsSTSEndpoints, stsHost) {
		if cfg.authVersion.LessThan(semver.Version{Major: 12}) {
			log.Warnf("Non-FIPS STS endpoint (%s) was used by a node joining "+
				"the cluster with the IAM join method. "+
				"Ensure that all nodes joining the cluster are up to date and also run in FIPS mode. "+
				"This will be an error in Teleport 12.0.0.",
				stsHost)
		} else {
			return trace.AccessDenied("node selected non-FIPS STS endpoint (%s) for the IAM join method", stsHost)
		}
	}

	return nil
}

// validateSTSIdentityRequest checks that a received sts:GetCallerIdentity
// request is valid and includes the challenge as a signed header. An example
// valid request looks like:
// ```
// POST / HTTP/1.1
// Host: sts.amazonaws.com
// Accept: application/json
// Authorization: AWS4-HMAC-SHA256 Credential=AAAAAAAAAAAAAAAAAAAA/20211108/us-east-1/sts/aws4_request, SignedHeaders=accept;content-length;content-type;host;x-amz-date;x-amz-security-token;x-teleport-challenge, Signature=999...
// Content-Length: 43
// Content-Type: application/x-www-form-urlencoded; charset=utf-8
// User-Agent: aws-sdk-go/1.37.17 (go1.17.1; darwin; amd64)
// X-Amz-Date: 20211108T190420Z
// X-Amz-Security-Token: aaa...
// X-Teleport-Challenge: 0ezlc3usTAkXeZTcfOazUq0BGrRaKmb4EwODk8U7J5A
//
// Action=GetCallerIdentity&Version=2011-06-15
// ```
func validateSTSIdentityRequest(req *http.Request, challenge string, cfg *iamRegisterConfig) (err error) {
	defer func() {
		// Always log a warning on the Auth server if the function detects an
		// invalid sts:GetCallerIdentity request, it's either going to be caused
		// by a node in a unknown region or an attacker.
		if err != nil {
			log.WithError(err).Warn("Detected an invalid sts:GetCallerIdentity used by a client attempting to use the IAM join method.")
		}
	}()

	if err := validateSTSHost(req.Host, cfg); err != nil {
		return trace.Wrap(err)
	}

	if req.Method != http.MethodPost {
		return trace.AccessDenied("sts identity request method %q does not match expected method %q", req.RequestURI, http.MethodPost)
	}

	if req.Header.Get(challengeHeaderKey) != challenge {
		return trace.AccessDenied("sts identity request does not include challenge header or it does not match")
	}

	authHeader := req.Header.Get(aws.AuthorizationHeader)

	sigV4, err := aws.ParseSigV4(authHeader)
	if err != nil {
		return trace.Wrap(err)
	}
	if !slices.Contains(sigV4.SignedHeaders, challengeHeaderKey) {
		return trace.AccessDenied("sts identity request auth header %q does not include "+
			challengeHeaderKey+" as a signed header", authHeader)
	}

	body, err := aws.GetAndReplaceReqBody(req)
	if err != nil {
		return trace.Wrap(err)
	}
	if !bytes.Equal([]byte(expectedSTSIdentityRequestBody), body) {
		return trace.BadParameter("sts request body %q does not equal expected %q", string(body), expectedSTSIdentityRequestBody)
	}

	return nil
}

func parseSTSRequest(req []byte) (*http.Request, error) {
	httpReq, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(req)))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Unset RequestURI and set req.URL instead (necessary quirk of sending a
	// request parsed by http.ReadRequest). Also, force https here.
	if httpReq.RequestURI != "/" {
		return nil, trace.AccessDenied("unexpected sts identity request URI: %q", httpReq.RequestURI)
	}
	httpReq.RequestURI = ""
	httpReq.URL = &url.URL{
		Scheme: "https",
		Host:   httpReq.Host,
	}
	return httpReq, nil
}

// awsIdentity holds aws Account and Arn, used for JSON parsing
type awsIdentity struct {
	Account string `json:"Account"`
	Arn     string `json:"Arn"`
}

// getCallerIdentityReponse is used for JSON parsing
type getCallerIdentityResponse struct {
	GetCallerIdentityResult awsIdentity `json:"GetCallerIdentityResult"`
}

// stsIdentityResponse is used for JSON parsing
type stsIdentityResponse struct {
	GetCallerIdentityResponse getCallerIdentityResponse `json:"GetCallerIdentityResponse"`
}

type stsClient interface {
	Do(*http.Request) (*http.Response, error)
}

type stsClientKey struct{}

// stsClientFromContext allows the default http client to be overridden for tests
func stsClientFromContext(ctx context.Context) stsClient {
	client, ok := ctx.Value(stsClientKey{}).(stsClient)
	if ok {
		return client
	}
	return http.DefaultClient
}

// executeSTSIdentityRequest sends the sts:GetCallerIdentity HTTP request to the
// AWS API, parses the response, and returns the awsIdentity
func executeSTSIdentityRequest(ctx context.Context, req *http.Request) (*awsIdentity, error) {
	client := stsClientFromContext(ctx)

	// set the http request context so it can be canceled
	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, trace.AccessDenied("aws sts api returned status: %q body: %q",
			resp.Status, body)
	}

	var identityResponse stsIdentityResponse
	if err := json.Unmarshal(body, &identityResponse); err != nil {
		return nil, trace.Wrap(err)
	}

	id := &identityResponse.GetCallerIdentityResponse.GetCallerIdentityResult
	if id.Account == "" {
		return nil, trace.BadParameter("received empty AWS account ID from sts API")
	}
	if id.Arn == "" {
		return nil, trace.BadParameter("received empty AWS identity ARN from sts API")
	}
	return id, nil
}

// arnMatches returns true if arn matches the pattern.
// Pattern should be an AWS ARN which may include "*" to match any combination
// of zero or more characters and "?" to match any single character.
// See https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_resource.html
func arnMatches(pattern, arn string) (bool, error) {
	pattern = regexp.QuoteMeta(pattern)
	pattern = strings.ReplaceAll(pattern, `\*`, ".*")
	pattern = strings.ReplaceAll(pattern, `\?`, ".")
	pattern = "^" + pattern + "$"
	matched, err := regexp.MatchString(pattern, arn)
	return matched, trace.Wrap(err)
}

// checkIAMAllowRules checks if the given identity matches any of the given
// allowRules.
func checkIAMAllowRules(identity *awsIdentity, allowRules []*types.TokenRule) error {
	for _, rule := range allowRules {
		// if this rule specifies an AWS account, the identity must match
		if len(rule.AWSAccount) > 0 {
			if rule.AWSAccount != identity.Account {
				// account doesn't match, continue to check the next rule
				continue
			}
		}
		// if this rule specifies an AWS ARN, the identity must match
		if len(rule.AWSARN) > 0 {
			matches, err := arnMatches(rule.AWSARN, identity.Arn)
			if err != nil {
				return trace.Wrap(err)
			}
			if !matches {
				// arn doesn't match, continue to check the next rule
				continue
			}
		}
		// node identity matches this allow rule
		return nil
	}
	return trace.AccessDenied("instance did not match any allow rules")
}

// checkIAMRequest checks if the given request satisfies the token rules and
// included the required challenge.
func (a *Server) checkIAMRequest(ctx context.Context, challenge string, req *proto.RegisterUsingIAMMethodRequest, cfg *iamRegisterConfig) error {
	tokenName := req.RegisterUsingTokenRequest.Token
	provisionToken, err := a.GetToken(ctx, tokenName)
	if err != nil {
		return trace.Wrap(err)
	}
	if provisionToken.GetJoinMethod() != types.JoinMethodIAM {
		return trace.AccessDenied("this token does not support the IAM join method")
	}

	// parse the incoming http request to the sts:GetCallerIdentity endpoint
	identityRequest, err := parseSTSRequest(req.StsIdentityRequest)
	if err != nil {
		return trace.Wrap(err)
	}

	// validate that the host, method, and headers are correct and the expected
	// challenge is included in the signed portion of the request
	if err := validateSTSIdentityRequest(identityRequest, challenge, cfg); err != nil {
		return trace.Wrap(err)
	}

	// send the signed request to the public AWS API and get the node identity
	// from the response
	identity, err := executeSTSIdentityRequest(ctx, identityRequest)
	if err != nil {
		return trace.Wrap(err)
	}

	// check that the node identity matches an allow rule for this token
	if err := checkIAMAllowRules(identity, provisionToken.GetAllowRules()); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func generateChallenge() (string, error) {
	// read 32 crypto-random bytes to generate the challenge
	challengeRawBytes := make([]byte, 32)
	if _, err := rand.Read(challengeRawBytes); err != nil {
		return "", trace.Wrap(err)
	}

	// encode the challenge to base64 so it can be sent in an HTTP header
	return base64.RawStdEncoding.EncodeToString(challengeRawBytes), nil
}

type iamRegisterConfig struct {
	authVersion *semver.Version
	fips        bool
}

func defaultIAMRegisterConfig(fips bool) *iamRegisterConfig {
	return &iamRegisterConfig{
		authVersion: authTeleportVersion,
		fips:        fips,
	}
}

type iamRegisterOption func(cfg *iamRegisterConfig)

func withAuthVersion(v *semver.Version) iamRegisterOption {
	return func(cfg *iamRegisterConfig) {
		cfg.authVersion = v
	}
}

func withFips(fips bool) iamRegisterOption {
	return func(cfg *iamRegisterConfig) {
		cfg.fips = fips
	}
}

// RegisterUsingIAMMethod registers the caller using the IAM join method and
// returns signed certs to join the cluster.
//
// The caller must provide a ChallengeResponseFunc which returns a
// *types.RegisterUsingTokenRequest with a signed sts:GetCallerIdentity request
// including the challenge as a signed header.
func (a *Server) RegisterUsingIAMMethod(ctx context.Context, challengeResponse client.RegisterChallengeResponseFunc, opts ...iamRegisterOption) (*proto.Certs, error) {
	cfg := defaultIAMRegisterConfig(a.fips)
	for _, opt := range opts {
		opt(cfg)
	}

	clientAddr, ok := ctx.Value(ContextClientAddr).(net.Addr)
	if !ok {
		return nil, trace.BadParameter("logic error: client address was not set")
	}

	challenge, err := generateChallenge()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	req, err := challengeResponse(challenge)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// fill in the client remote addr to the register request
	req.RegisterUsingTokenRequest.RemoteAddr = clientAddr.String()
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	// perform common token checks
	provisionToken, err := a.checkTokenJoinRequestCommon(ctx, req.RegisterUsingTokenRequest)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// check that the GetCallerIdentity request is valid and matches the token
	if err := a.checkIAMRequest(ctx, challenge, req, cfg); err != nil {
		return nil, trace.Wrap(err)
	}

	certs, err := a.generateCerts(ctx, provisionToken, req.RegisterUsingTokenRequest, nil)
	return certs, trace.Wrap(err)
}

type stsIdentityRequestConfig struct {
	regionalEndpointOption endpoints.STSRegionalEndpoint
	fipsEndpointOption     endpoints.FIPSEndpointState
}

type stsIdentityRequestOption func(cfg *stsIdentityRequestConfig)

func withRegionalEndpoint(useRegionalEndpoint bool) stsIdentityRequestOption {
	return func(cfg *stsIdentityRequestConfig) {
		if useRegionalEndpoint {
			cfg.regionalEndpointOption = endpoints.RegionalSTSEndpoint
		} else {
			cfg.regionalEndpointOption = endpoints.LegacySTSEndpoint
		}
	}
}

func withFIPSEndpoint(useFIPS bool) stsIdentityRequestOption {
	return func(cfg *stsIdentityRequestConfig) {
		if useFIPS {
			cfg.fipsEndpointOption = endpoints.FIPSEndpointStateEnabled
		} else {
			cfg.fipsEndpointOption = endpoints.FIPSEndpointStateDisabled
		}
	}
}

// createSignedSTSIdentityRequest is called on the client side and returns an
// sts:GetCallerIdentity request signed with the local AWS credentials
func createSignedSTSIdentityRequest(ctx context.Context, challenge string, opts ...stsIdentityRequestOption) ([]byte, error) {
	cfg := &stsIdentityRequestConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	stsClient, err := newSTSClient(ctx, cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	req, _ := stsClient.GetCallerIdentityRequest(&sts.GetCallerIdentityInput{})
	// set challenge header
	req.HTTPRequest.Header.Set(challengeHeaderKey, challenge)
	// request json for simpler parsing
	req.HTTPRequest.Header.Set("Accept", "application/json")
	// sign the request, including headers
	if err := req.Sign(); err != nil {
		return nil, trace.Wrap(err)
	}
	// write the signed HTTP request to a buffer
	var signedRequest bytes.Buffer
	if err := req.HTTPRequest.Write(&signedRequest); err != nil {
		return nil, trace.Wrap(err)
	}
	return signedRequest.Bytes(), nil
}

func newSTSClient(ctx context.Context, cfg *stsIdentityRequestConfig) (*sts.STS, error) {
	awsConfig := awssdk.Config{
		UseFIPSEndpoint:     cfg.fipsEndpointOption,
		STSRegionalEndpoint: cfg.regionalEndpointOption,
	}
	sess, err := session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
		Config:            awsConfig,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	stsClient := sts.New(sess)

	if slices.Contains(globalSTSEndpoints, strings.TrimPrefix(stsClient.Endpoint, "https://")) {
		// If the caller wants to use the regional endpoint but it was not resolved
		// from the environment, attempt to find the region from the EC2 IMDS
		if cfg.regionalEndpointOption == endpoints.RegionalSTSEndpoint {
			region, err := getEC2LocalRegion(ctx)
			if err != nil {
				return nil, trace.Wrap(err, "failed to resolve local AWS region from environment or IMDS")
			}
			stsClient = sts.New(sess, awssdk.NewConfig().WithRegion(region))
		} else {
			log.Info("Attempting to use the global STS endpoint for the IAM join method. " +
				"This will probably fail in non-default AWS partitions such as China or GovCloud, or if FIPS mode is enabled. " +
				"Consider setting the AWS_REGION environment variable, setting the region in ~/.aws/config, or enabling the IMDSv2.")
		}
	}

	if cfg.fipsEndpointOption == endpoints.FIPSEndpointStateEnabled &&
		!slices.Contains(validSTSEndpoints, strings.TrimPrefix(stsClient.Endpoint, "https://")) {
		// The AWS SDK will generate invalid endpoints when attempting to
		// resolve the FIPS endpoint for a region which does not have one.
		// In this case, try to use the FIPS endpoint in us-east-1. This should
		// work for all regions in the standard partition. In GovCloud we should
		// not hit this because all regional endpoints support FIPS. In China or
		// other partitions this will fail and FIPS mode will not be supported.
		log.Infof("AWS SDK resolved FIPS STS endpoint %s, which does not appear to be valid. "+
			"Attempting to use the FIPS STS endpoint for us-east-1.",
			stsClient.Endpoint)
		stsClient = sts.New(sess, awssdk.NewConfig().WithRegion("us-east-1"))
	}

	return stsClient, nil
}

// getEC2LocalRegion returns the AWS region this EC2 instance is running in, or
// a NotFound error if the EC2 IMDS is unavailable.
func getEC2LocalRegion(ctx context.Context) (string, error) {
	imdsClient, err := cloudaws.NewInstanceMetadataClient(ctx)
	if err != nil {
		return "", trace.Wrap(err)
	}

	if !imdsClient.IsAvailable(ctx) {
		return "", trace.NotFound("IMDS is unavailable")
	}

	region, err := imdsClient.GetRegion(ctx)
	return region, trace.Wrap(err)
}
