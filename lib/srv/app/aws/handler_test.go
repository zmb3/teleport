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
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/events/eventstest"
	"github.com/zmb3/teleport/lib/srv/app/common"
	"github.com/zmb3/teleport/lib/tlsca"
	awsutils "github.com/zmb3/teleport/lib/utils/aws"
)

// TestAWSSignerHandler test the AWS SigningService APP handler logic with mocked STS signing credentials.
func TestAWSSignerHandler(t *testing.T) {
	type check func(t *testing.T, resp *s3.ListBucketsOutput, err error)
	checks := func(chs ...check) []check { return chs }

	hasNoErr := func() check {
		return func(t *testing.T, resp *s3.ListBucketsOutput, err error) {
			require.NoError(t, err)
		}
	}

	hasStatusCode := func(wantStatusCode int) check {
		return func(t *testing.T, resp *s3.ListBucketsOutput, err error) {
			require.Error(t, err)
			apiErr, ok := err.(awserr.RequestFailure)
			if !ok {
				t.Errorf("invalid error type: %T", err)
			}
			require.Equal(t, wantStatusCode, apiErr.StatusCode())
		}
	}

	tests := []struct {
		name                string
		awsClientSession    *session.Session
		wantHost            string
		wantAuthCredService string
		wantAuthCredRegion  string
		wantAuthCredKeyID   string
		checks              []check
	}{
		{
			name: "s3 access",
			awsClientSession: session.Must(session.NewSession(&aws.Config{
				Credentials: credentials.NewCredentials(&credentials.StaticProvider{Value: credentials.Value{
					AccessKeyID:     "fakeClientKeyID",
					SecretAccessKey: "fakeClientSecret",
				}}),
				Region: aws.String("us-west-2"),
			})),
			wantHost:            "s3.us-west-2.amazonaws.com",
			wantAuthCredKeyID:   "AKIDl",
			wantAuthCredService: "s3",
			wantAuthCredRegion:  "us-west-2",
			checks: checks(
				hasNoErr(),
			),
		},
		{
			name: "s3 access with different region",
			awsClientSession: session.Must(session.NewSession(&aws.Config{
				Credentials: credentials.NewCredentials(&credentials.StaticProvider{Value: credentials.Value{
					AccessKeyID:     "fakeClientKeyID",
					SecretAccessKey: "fakeClientSecret",
				}}),
				Region: aws.String("us-west-1"),
			})),
			wantHost:            "s3.us-west-1.amazonaws.com",
			wantAuthCredKeyID:   "AKIDl",
			wantAuthCredService: "s3",
			wantAuthCredRegion:  "us-west-1",
			checks: checks(
				hasNoErr(),
			),
		},
		{
			name: "s3 access missing credentials",
			awsClientSession: session.Must(session.NewSession(&aws.Config{
				Credentials: credentials.AnonymousCredentials,
				Region:      aws.String("us-west-1"),
			})),
			checks: checks(
				hasStatusCode(http.StatusBadRequest),
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, tc.wantHost, request.Host)
				awsAuthHeader, err := awsutils.ParseSigV4(request.Header.Get(awsutils.AuthorizationHeader))
				require.NoError(t, err)
				require.Equal(t, tc.wantAuthCredRegion, awsAuthHeader.Region)
				require.Equal(t, tc.wantAuthCredKeyID, awsAuthHeader.KeyID)
				require.Equal(t, tc.wantAuthCredService, awsAuthHeader.Service)
			}

			suite := createSuite(t, handler)

			s3Client := s3.New(tc.awsClientSession, &aws.Config{
				Endpoint: &suite.URL,
			})
			resp, err := s3Client.ListBuckets(&s3.ListBucketsInput{})
			for _, check := range tc.checks {
				check(t, resp, err)
			}

			// Validate audit event.
			if err == nil {
				require.Len(t, suite.emitter.C(), 1)

				event := <-suite.emitter.C()
				appSessionEvent, ok := event.(*events.AppSessionRequest)
				require.True(t, ok)
				require.Equal(t, tc.wantHost, appSessionEvent.AWSHost)
				require.Equal(t, tc.wantAuthCredService, appSessionEvent.AWSService)
				require.Equal(t, tc.wantAuthCredRegion, appSessionEvent.AWSRegion)
			} else {
				require.Len(t, suite.emitter.C(), 0)
			}
		})
	}
}

func TestURLForResolvedEndpoint(t *testing.T) {
	tests := []struct {
		name                 string
		inputReq             *http.Request
		inputResolvedEnpoint *endpoints.ResolvedEndpoint
		requireError         require.ErrorAssertionFunc
		expectURL            string
	}{
		{
			name:     "bad resolved endpoint",
			inputReq: mustNewRequest(t, "GET", "http://1.2.3.4/hello/world?aa=2", nil),
			inputResolvedEnpoint: &endpoints.ResolvedEndpoint{
				URL: string([]byte{0x05}),
			},
			requireError: require.Error,
		},
		{
			name:     "replaced host and scheme",
			inputReq: mustNewRequest(t, "GET", "http://1.2.3.4/hello/world?aa=2", nil),
			inputResolvedEnpoint: &endpoints.ResolvedEndpoint{
				URL: "https://local.test.com",
			},
			expectURL:    "https://local.test.com/hello/world?aa=2",
			requireError: require.NoError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actualURL, err := urlForResolvedEndpoint(test.inputReq, test.inputResolvedEnpoint)
			require.Equal(t, test.expectURL, actualURL)
			test.requireError(t, err)
		})
	}
}

func mustNewRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()

	r, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	return r
}

func staticAWSCredentials(client.ConfigProvider, *common.SessionContext) *credentials.Credentials {
	return credentials.NewStaticCredentials("AKIDl", "SECRET", "SESSION")
}

type suite struct {
	*httptest.Server
	identity *tlsca.Identity
	app      types.Application
	emitter  *eventstest.ChannelEmitter
}

func createSuite(t *testing.T, handler http.HandlerFunc) *suite {
	emitter := eventstest.NewChannelEmitter(1)
	user := auth.LocalUser{Username: "user"}
	app, err := types.NewAppV3(types.Metadata{
		Name: "awsconsole",
	}, types.AppSpecV3{
		URI:        constants.AWSConsoleURL,
		PublicAddr: "test.local",
	})
	require.NoError(t, err)

	awsAPIMock := httptest.NewUnstartedServer(handler)
	awsAPIMock.StartTLS()
	t.Cleanup(func() {
		awsAPIMock.Close()
	})

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial(awsAPIMock.Listener.Addr().Network(), awsAPIMock.Listener.Addr().String())
			},
		},
	}

	svc, err := NewSigningService(SigningServiceConfig{
		getSigningCredentials: staticAWSCredentials,
		Client:                client,
		Clock:                 clockwork.NewFakeClock(),
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		request = common.WithSessionContext(request, &common.SessionContext{
			Identity: &user.Identity,
			App:      app,
			Emitter:  emitter,
		})

		svc.ServeHTTP(writer, request)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
	})

	return &suite{
		Server:   server,
		identity: &user.Identity,
		app:      app,
		emitter:  emitter,
	}
}
