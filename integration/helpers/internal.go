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
	"context"
	"time"

	"github.com/gravitational/trace"
	authztypes "k8s.io/client-go/kubernetes/typed/authorization/v1"

	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/utils"
)

func nullImpersonationCheck(context.Context, string, authztypes.SelfSubjectAccessReviewInterface) error {
	return nil
}

func StartAndWait(process *service.TeleportProcess, expectedEvents []string) ([]service.Event, error) {
	// start the process
	err := process.Start()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// wait for all events to arrive or a timeout. if all the expected events
	// from above are not received, this instance will not start
	receivedEvents := make([]service.Event, 0, len(expectedEvents))
	ctx, cancel := context.WithTimeout(process.ExitContext(), 10*time.Second)
	defer cancel()
	for _, eventName := range expectedEvents {
		if event, err := process.WaitForEvent(ctx, eventName); err == nil {
			receivedEvents = append(receivedEvents, event)
		}
	}

	if len(receivedEvents) < len(expectedEvents) {
		return nil, trace.BadParameter("timed out, only %v/%v events received. received: %v, expected: %v",
			len(receivedEvents), len(expectedEvents), receivedEvents, expectedEvents)
	}

	// Not all services follow a non-blocking Start/Wait pattern. This means a
	// *Ready event may be emit slightly before the service actually starts for
	// blocking services. Long term those services should be re-factored, until
	// then sleep for 250ms to handle this situation.
	time.Sleep(250 * time.Millisecond)

	return receivedEvents, nil
}

func EnableDesktopService(config *service.Config) {
	// This config won't actually work, because there is no LDAP server,
	// but it's enough to force desktop service to run.
	config.WindowsDesktop.Enabled = true
	config.WindowsDesktop.ListenAddr = *utils.MustParseAddr("127.0.0.1:0")
	config.WindowsDesktop.Discovery.BaseDN = ""
	config.WindowsDesktop.LDAP = service.LDAPConfig{
		Domain:             "example.com",
		Addr:               "127.0.0.1:636",
		Username:           "test",
		InsecureSkipVerify: true,
	}
}
