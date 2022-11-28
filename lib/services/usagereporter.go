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

package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/bufbuild/connect-go"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/defaults"
	prehogapi "github.com/gravitational/teleport/lib/prehog/gen/prehog/v1alpha"
	prehogclient "github.com/gravitational/teleport/lib/prehog/gen/prehog/v1alpha/prehogv1alphaconnect"
	"github.com/gravitational/teleport/lib/services/local"
	"github.com/gravitational/trace"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"time"

	usageevents "github.com/gravitational/teleport/api/gen/proto/go/usageevents/v1"
	prehogv1 "github.com/gravitational/teleport/lib/prehog/gen/prehog/v1alpha"
	"github.com/gravitational/teleport/lib/utils"
)

const (
	// usageReporterMinBatchSize determines the size at which a batch is sent
	// regardless of elapsed time
	usageReporterMinBatchSize = 20

	// usageReporterMaxBatchSize is the largest batch size that will be sent to
	// the server; batches larger than this will be split into multiple
	// requests.
	usageReporterMaxBatchSize = 100

	// usageReporterMaxBatchAge is the maximum age a batch may reach before
	// being flushed, regardless of the batch size
	usageReporterMaxBatchAge = time.Second * 30

	// usageReporterMaxBufferSize is the maximum size to which the event buffer
	// may grow. Events submitted once this limit is reached will be discarded.
	// Events that were in the submission queue that fail to submit may also be
	// discarded when requeued.
	usageReporterMaxBufferSize = 500

	// usageReporterSubmitDelay is a mandatory delay added to each batch submission
	// to avoid spamming the prehog instance.
	usageReporterSubmitDelay = time.Second * 1
)

// UsageAnonymizable is an event that can be anonymized.
type UsageAnonymizable interface {
	// Anonymize uses the given anonymizer to anonymize all fields in place.
	Anonymize(utils.Anonymizer) UsageAnonymizable
}

// UsageReporter is a service that accepts Teleport usage events.
type UsageReporter interface {
	// SubmitAnonymizedUsageEvent submits a usage event. The payload will be
	// anonymized by the reporter implementation.
	SubmitAnonymizedUsageEvents(event ...UsageAnonymizable) error
}

// UsageUserLogin is an event emitted when a user logs into Teleport,
// potentially via SSO.
type UsageUserLogin prehogv1.UserLoginEvent

func (u *UsageUserLogin) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUserLogin{
		UserName:      a.Anonymize([]byte(u.UserName)),
		ConnectorType: u.ConnectorType, // TODO: anonymizer connector type?
	}
}

type UsageSSOCreate prehogv1.SSOCreateEvent

func (u *UsageSSOCreate) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageSSOCreate{
		ConnectorType: u.ConnectorType, // TODO: anonymize connector type?
	}
}

// UsageSessionStart is an event emitted when some Teleport session has started
// (ssh, etc).
type UsageSessionStart prehogv1.SessionStartEvent

func (u *UsageSessionStart) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageSessionStart{
		UserName:    a.Anonymize([]byte(u.UserName)),
		SessionType: u.SessionType,
	}
}

// UsageResourceCreate is an event emitted when various resource types have been
// created.
type UsageResourceCreate prehogv1.ResourceCreateEvent

func (u *UsageResourceCreate) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageResourceCreate{
		ResourceType: u.ResourceType, // TODO: anonymize this?
	}
}

// UsageUIBannerClick is a UI event sent when a banner is clicked.
type UsageUIBannerClick prehogv1.UIBannerClickEvent

func (u *UsageUIBannerClick) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIBannerClick{
		UserName: a.Anonymize([]byte(u.UserName)),
		Alert:    u.Alert,
	}
}

type UsageUIOnboardGetStartedClickEvent prehogv1.UIOnboardGetStartedClickEvent

func (u *UsageUIOnboardGetStartedClickEvent) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardGetStartedClickEvent{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardCompleteGoToDashboardClickEvent prehogv1.UIOnboardCompleteGoToDashboardClickEvent

func (u *UsageUIOnboardCompleteGoToDashboardClickEvent) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardCompleteGoToDashboardClickEvent{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardAddFirstResourceClickEvent prehogv1.UIOnboardAddFirstResourceClickEvent

func (u *UsageUIOnboardAddFirstResourceClickEvent) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardAddFirstResourceClickEvent{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardAddFirstResourceLaterClickEvent prehogv1.UIOnboardAddFirstResourceLaterClickEvent

func (u *UsageUIOnboardAddFirstResourceLaterClickEvent) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardAddFirstResourceLaterClickEvent{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardSetCredentialSubmit prehogv1.UIOnboardSetCredentialSubmitEvent

func (u *UsageUIOnboardSetCredentialSubmit) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardSetCredentialSubmit{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardRegisterChallengeSubmit prehogv1.UIOnboardRegisterChallengeSubmitEvent

func (u *UsageUIOnboardRegisterChallengeSubmit) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardRegisterChallengeSubmit{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

type UsageUIOnboardRecoveryCodesContinueClick prehogv1.UIOnboardRecoveryCodesContinueClickEvent

func (u *UsageUIOnboardRecoveryCodesContinueClick) Anonymize(a utils.Anonymizer) UsageAnonymizable {
	return &UsageUIOnboardRecoveryCodesContinueClick{
		UserName: a.Anonymize([]byte(u.UserName)),
	}
}

// ConvertUsageEvent converts a usage event from an API object into an
// anonymizable event. All events that can be submitted externally via the Auth
// API need to be defined here.
func ConvertUsageEvent(event *usageevents.UsageEventOneOf, username string) (UsageAnonymizable, error) {
	switch e := event.GetEvent().(type) {
	case *usageevents.UsageEventOneOf_UiBannerClick:
		return &UsageUIBannerClick{
			UserName: username,
			Alert:    e.UiBannerClick.Alert,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardGetStartedClick:
		return &UsageUIOnboardGetStartedClickEvent{
			UserName: e.UiOnboardGetStartedClick.Username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardCompleteGoToDashboardClick:
		return &UsageUIOnboardCompleteGoToDashboardClickEvent{
			UserName: username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardAddFirstResourceClick:
		return &UsageUIOnboardAddFirstResourceClickEvent{
			UserName: username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardAddFirstResourceLaterClick:
		return &UsageUIOnboardAddFirstResourceLaterClickEvent{
			UserName: username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardSetCredentialSubmit:
		return &UsageUIOnboardSetCredentialSubmit{
			UserName: e.UiOnboardSetCredentialSubmit.Username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardRegisterChallengeSubmit:
		return &UsageUIOnboardRegisterChallengeSubmit{
			UserName: e.UiOnboardRegisterChallengeSubmit.Username,
		}, nil
	case *usageevents.UsageEventOneOf_UiOnboardRecoveryCodesContinueClick:
		return &UsageUIOnboardRecoveryCodesContinueClick{
			UserName: e.UiOnboardRecoveryCodesContinueClick.Username,
		}, nil
	default:
		return nil, trace.BadParameter("invalid usage event type %T", event.GetEvent())
	}
}

type TeleportUsageReporter struct {
	usageReporter *local.UsageReporter[prehogapi.SubmitEventRequest]
	anonymizer    utils.Anonymizer
	clusterName   types.ClusterName
}

func (tur *TeleportUsageReporter) SubmitAnonymizedUsageEvents(events ...UsageAnonymizable) error {
	for _, e := range events {
		err := tur.usageReporter.AddEventToQueue(tur.convertEvent(e))
		if err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

func (tur *TeleportUsageReporter) convertEvent(event UsageAnonymizable) func(timestamp *timestamppb.Timestamp) (*prehogapi.SubmitEventRequest, error) {
	return func(timestamp *timestamppb.Timestamp) (*prehogapi.SubmitEventRequest, error) {
		// Anonymize the event and replace the old value.
		event = event.Anonymize(tur.anonymizer)

		clusterName := tur.anonymizer.Anonymize([]byte(tur.clusterName.GetClusterName()))

		// "Event" can't be named because protoc doesn't export the interface, so
		// instead we have a giant, fallible switch statement for something the
		// compiler could just as well check for us >:(
		switch e := event.(type) {
		case *UsageUserLogin:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UserLogin{
					UserLogin: (*prehogapi.UserLoginEvent)(e),
				},
			}, nil
		case *UsageSSOCreate:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_SsoCreate{
					SsoCreate: (*prehogapi.SSOCreateEvent)(e),
				},
			}, nil
		case *UsageSessionStart:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_SessionStart{
					SessionStart: (*prehogapi.SessionStartEvent)(e),
				},
			}, nil
		case *UsageResourceCreate:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_ResourceCreate{
					ResourceCreate: (*prehogapi.ResourceCreateEvent)(e),
				},
			}, nil
		case *UsageUIBannerClick:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiBannerClick{
					UiBannerClick: (*prehogapi.UIBannerClickEvent)(e),
				},
			}, nil
		case *UsageUIOnboardGetStartedClickEvent:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardGetStartedClick{
					UiOnboardGetStartedClick: (*prehogapi.UIOnboardGetStartedClickEvent)(e),
				},
			}, nil
		case *UsageUIOnboardCompleteGoToDashboardClickEvent:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardCompleteGoToDashboardClick{
					UiOnboardCompleteGoToDashboardClick: (*prehogapi.UIOnboardCompleteGoToDashboardClickEvent)(e),
				},
			}, nil
		case *UsageUIOnboardAddFirstResourceClickEvent:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardAddFirstResourceClick{
					UiOnboardAddFirstResourceClick: (*prehogapi.UIOnboardAddFirstResourceClickEvent)(e),
				},
			}, nil
		case *UsageUIOnboardAddFirstResourceLaterClickEvent:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardAddFirstResourceLaterClick{
					UiOnboardAddFirstResourceLaterClick: (*prehogapi.UIOnboardAddFirstResourceLaterClickEvent)(e),
				},
			}, nil
		case *UsageUIOnboardSetCredentialSubmit:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardSetCredentialSubmit{
					UiOnboardSetCredentialSubmit: (*prehogapi.UIOnboardSetCredentialSubmitEvent)(e),
				},
			}, nil
		case *UsageUIOnboardRegisterChallengeSubmit:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardRegisterChallengeSubmit{
					UiOnboardRegisterChallengeSubmit: (*prehogapi.UIOnboardRegisterChallengeSubmitEvent)(e),
				},
			}, nil
		case *UsageUIOnboardRecoveryCodesContinueClick:
			return &prehogapi.SubmitEventRequest{
				ClusterName: clusterName,
				Timestamp:   timestamp,
				Event: &prehogapi.SubmitEventRequest_UiOnboardRecoveryCodesContinueClick{
					UiOnboardRecoveryCodesContinueClick: (*prehogapi.UIOnboardRecoveryCodesContinueClickEvent)(e),
				},
			}, nil
		default:
			return nil, trace.BadParameter("unexpected event usage type %T", event)
		}
	}
}

// ConvertAndAnonymizeUsageEvent converts a usage event from an API object into an anonymized event.
// All events that can be submitted externally via the Auth API need to be defined here.

func NewUsageReporterTeleport(clusterName types.ClusterName, submitter local.UsageSubmitFunc[prehogapi.SubmitEventRequest]) (*TeleportUsageReporter, error) {
	anonymizer, err := utils.NewHMACAnonymizer(clusterName.GetClusterID())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	//err = metrics.RegisterPrometheusCollectors(usagePrometheusCollectors...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	reporter := local.NewUsageReporter[prehogapi.SubmitEventRequest](&local.UsageReporterOptions[prehogapi.SubmitEventRequest]{
		SubmitFunc:    submitter,
		MinBatchSize:  usageReporterMinBatchSize,
		MaxBatchSize:  usageReporterMaxBatchSize,
		MaxBatchAge:   usageReporterMaxBatchAge,
		MaxBufferSize: usageReporterMaxBufferSize,
		SubmitDelay:   usageReporterSubmitDelay,
	})

	return &TeleportUsageReporter{
		usageReporter: reporter,
		anonymizer:    anonymizer,
		clusterName:   clusterName,
	}, nil
}

func NewPrehogSubmitter(ctx context.Context, prehogEndpoint string, clientCert *tls.Certificate, caCertPEM []byte) (local.UsageSubmitFunc[prehogapi.SubmitEventRequest], error) {
	tlsConfig := &tls.Config{
		// Self-signed test licenses may not have a proper issuer and won't be
		// used if just passed in via Certificates, so we'll use this to
		// explicitly set the client cert we want to use.
		GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return clientCert, nil
		},
	}

	if caCertPEM != nil {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caCertPEM)

		tlsConfig.RootCAs = pool
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     tlsConfig,
			IdleConnTimeout:     defaults.HTTPIdleTimeout,
			MaxIdleConns:        defaults.HTTPMaxIdleConns,
			MaxIdleConnsPerHost: defaults.HTTPMaxConnsPerHost,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 5 * time.Second,
	}

	client := prehogclient.NewTeleportReportingServiceClient(httpClient, prehogEndpoint)

	return func(reporter *local.UsageReporter[prehogapi.SubmitEventRequest], events []*prehogapi.SubmitEventRequest) error {
		// Note: the backend doesn't support batching at the moment.
		for _, event := range events {
			// Note: this results in retrying the entire batch, which probably
			// isn't ideal.
			req := connect.NewRequest(event)
			if _, err := client.SubmitEvent(ctx, req); err != nil {
				return trace.Wrap(err)
			}
		}

		return nil
	}, nil
}
