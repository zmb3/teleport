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

package daemon

import (
	"context"
	"fmt"
	"github.com/bufbuild/connect-go"
	"github.com/gravitational/teleport/lib/defaults"
	prehogapi "github.com/gravitational/teleport/lib/prehog/gen/prehog/v1alpha"
	prehogclient "github.com/gravitational/teleport/lib/prehog/gen/prehog/v1alpha/prehogv1alphaconnect"
	"github.com/gravitational/teleport/lib/services/local"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"time"

	api "github.com/gravitational/teleport/lib/teleterm/api/protogen/golang/v1"
)

func NewConnectUsageReporter(ctx context.Context) (*local.UsageReporter[prehogapi.ConnectSubmitEventRequest], error) {
	submitter, err := newStdoutPrehogSubmitter()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return local.NewUsageReporter(&local.UsageReporterOptions[prehogapi.ConnectSubmitEventRequest]{
		Ctx:           ctx,
		SubmitFunc:    submitter,
		MinBatchSize:  1,
		MaxBatchSize:  1,
		MaxBufferSize: 1,
		SubmitDelay:   time.Second * 30,
	}), nil
}

func newRealPrehogSubmitter(ctx context.Context, prehogEndpoint string) (local.UsageSubmitFunc[prehogapi.ConnectSubmitEventRequest], error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			Proxy:             http.ProxyFromEnvironment,
			//TLSClientConfig:     tlsConfig,
			IdleConnTimeout:     defaults.HTTPIdleTimeout,
			MaxIdleConns:        defaults.HTTPMaxIdleConns,
			MaxIdleConnsPerHost: defaults.HTTPMaxConnsPerHost,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 5 * time.Second,
	}

	client := prehogclient.NewConnectReportingServiceClient(httpClient, prehogEndpoint)

	return func(reporter *local.UsageReporter[prehogapi.ConnectSubmitEventRequest], events []*prehogapi.ConnectSubmitEventRequest) error {
		// Note: the backend doesn't support batching at the moment.
		for _, event := range events {
			// Note: this results in retrying the entire batch, which probably
			// isn't ideal.
			req := connect.NewRequest(event)
			if _, err := client.SubmitConnectEvent(ctx, req); err != nil {
				return trace.Wrap(err)
			}
		}

		return nil
	}, nil
}

// TODO remove
func newStdoutPrehogSubmitter() (local.UsageSubmitFunc[prehogapi.ConnectSubmitEventRequest], error) {
	return func(reporter *local.UsageReporter[prehogapi.ConnectSubmitEventRequest], events []*prehogapi.ConnectSubmitEventRequest) error {
		for _, event := range events {
			fmt.Println(event)
		}
		return nil
	}, nil
}

func (s *Service) ReportUsageEvent(ctx context.Context, req *api.ReportEventRequest) error {
	getClusterAnonymizerByClusterUri := func(clusterUri string) (utils.Anonymizer, error) {
		cluster, err := s.GetCluster(ctx, clusterUri)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return cluster.Anonymizer, nil
	}

	event := convertAndAnonymizeApiEvent(req, getClusterAnonymizerByClusterUri)
	err := s.usageReporter.AddEventToQueue(event)
	return trace.Wrap(err)
}

type AnonymizedConnectEvent = func(timestamp *timestamppb.Timestamp) (*prehogapi.ConnectSubmitEventRequest, error)

func convertAndAnonymizeApiEvent(event *api.ReportEventRequest, getClusterAnonymizerByClusterUri func(clusterUri string) (utils.Anonymizer, error)) AnonymizedConnectEvent {
	return func(timestamp *timestamppb.Timestamp) (*prehogapi.ConnectSubmitEventRequest, error) {
		switch e := event.GetEvent().GetEvent().(type) {
		case *api.ConnectUsageEventOneOf_LoginEvent:
			anonymizer, err := getClusterAnonymizerByClusterUri(e.LoginEvent.GetClusterUri())
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return &prehogapi.ConnectSubmitEventRequest{
				DistinctId: event.GetDistinctId(),
				Timestamp:  timestamp,
				Event:      convertLoginEvent(e.LoginEvent, anonymizer),
			}, nil
		case *api.ConnectUsageEventOneOf_ConnectToProtocolEvent:
			anonymizer, err := getClusterAnonymizerByClusterUri(e.ConnectToProtocolEvent.GetClusterUri())
			if err != nil {
				return nil, trace.Wrap(err)
			}
			return &prehogapi.ConnectSubmitEventRequest{
				DistinctId: event.GetDistinctId(),
				Timestamp:  timestamp,
				Event:      convertProtocolConnectEvent(e.ConnectToProtocolEvent, anonymizer),
			}, nil

		default:
			return nil, trace.BadParameter("unexpected Event usage type %T", event)
		}
	}
}

func convertLoginEvent(event *api.LoginEvent, anonymizer utils.Anonymizer) *prehogapi.ConnectSubmitEventRequest_UserLogin {
	return &prehogapi.ConnectSubmitEventRequest_UserLogin{
		UserLogin: &prehogapi.ConnectUserLoginEvent{
			ClusterName:   anonymizer.Anonymize([]byte(event.GetClusterName())),
			UserName:      anonymizer.Anonymize([]byte(event.GetUserName())),
			ConnectorType: event.UserName,
		},
	}
}

// TODO: use proper event
func convertProtocolConnectEvent(event *api.ConnectToProtocolEvent, anonymizer utils.Anonymizer) *prehogapi.ConnectSubmitEventRequest_UserLogin {
	return &prehogapi.ConnectSubmitEventRequest_UserLogin{
		UserLogin: &prehogapi.ConnectUserLoginEvent{
			ClusterName:   anonymizer.Anonymize([]byte(event.GetClusterName())),
			UserName:      anonymizer.Anonymize([]byte(event.GetUserName())),
			ConnectorType: "github",
		},
	}
}
