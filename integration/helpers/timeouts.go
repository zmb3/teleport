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
	"time"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/lib/defaults"
)

// SetTestTimeouts affects global timeouts inside Teleport, making connections
// work faster but consuming more CPU (useful for integration testing).
// NOTE: This function modifies global values for timeouts, etc. If your tests
// call this function, they MUST NOT BE RUN IN PARALLEL, as they may stomp on
// other tests.
func SetTestTimeouts(t time.Duration) {
	// TODO(tcsc): Remove this altogether and replace with per-test timeout
	//             config (as per #8913)

	apidefaults.SetTestTimeouts(t, t)

	defaults.ResyncInterval = t
	defaults.SessionRefreshPeriod = t
	defaults.HeartbeatCheckPeriod = t
}
