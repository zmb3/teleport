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

package srv

import (
	"context"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"

	"github.com/zmb3/teleport/api/client/proto"
	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/retryutils"
	"github.com/zmb3/teleport/lib/services"
)

// SessionTracker is a session tracker for a specific session. It tracks
// the session in memory and broadcasts updates to the given service (backend).
type SessionTracker struct {
	closeC chan struct{}
	// tracker is the in memory session tracker
	tracker types.SessionTracker
	// trackerCond is used to provide synchronized access to tracker
	// and to broadcast state changes.
	trackerCond *sync.Cond
	// service is used to share session tracker updates with the service
	service services.SessionTrackerService
}

// NewSessionTracker returns a new SessionTracker for the given types.SessionTracker
func NewSessionTracker(ctx context.Context, trackerSpec types.SessionTrackerSpecV1, service services.SessionTrackerService) (*SessionTracker, error) {
	t, err := types.NewSessionTracker(trackerSpec)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if service != nil {
		if t, err = service.CreateSessionTracker(ctx, t); err != nil {
			return nil, trace.Wrap(err)
		}
	}

	return &SessionTracker{
		service:     service,
		tracker:     t,
		trackerCond: sync.NewCond(&sync.Mutex{}),
		closeC:      make(chan struct{}),
	}, nil
}

// Close closes the session tracker and sets the tracker state to terminated
func (s *SessionTracker) Close(ctx context.Context) error {
	close(s.closeC)
	err := s.UpdateState(ctx, types.SessionState_SessionStateTerminated)
	return trace.Wrap(err)
}

const sessionTrackerExpirationUpdateInterval = apidefaults.SessionTrackerTTL / 3

// UpdateExpirationLoop extends the session tracker expiration by 30 minutes every 10 minutes
// until the SessionTracker or ctx is closed. If there is a failure to write the updated
// SessionTracker to the backend, the write is retried with exponential backoff up until the original
// SessionTracker expiry.
func (s *SessionTracker) UpdateExpirationLoop(ctx context.Context, clock clockwork.Clock) error {
	ticker := clock.NewTicker(sessionTrackerExpirationUpdateInterval)
	defer func() {
		// ensure the correct ticker is stopped due to reassignment below
		ticker.Stop()
	}()

	for {
		select {
		case t := <-ticker.Chan():
			expiry := t.Add(apidefaults.SessionTrackerTTL)
			if err := s.UpdateExpiration(ctx, expiry); err != nil {
				// If the tracker doesn't exist in the backend then
				// the update loop will never succeed.
				if trace.IsNotFound(err) {
					return trace.Wrap(err)
				}

				// Stop the ticker so that it doesn't
				// keep accumulating ticks while we are retrying.
				ticker.Stop()

				if err := s.retryUpdate(ctx, clock); err != nil {
					return trace.Wrap(err)
				}

				// Tracker was updated, create a new ticker and proceed with the update
				// loop.
				// Note: clockwork.Ticker doesn't support Reset, if and when it does
				// we should use that instead of creating a new ticker.
				ticker = clock.NewTicker(sessionTrackerExpirationUpdateInterval)
			}
		case <-ctx.Done():
			return nil
		case <-s.closeC:
			return nil
		}
	}
}

// retryUpdate attempts to periodically retry updating the session tracker
func (s *SessionTracker) retryUpdate(ctx context.Context, clock clockwork.Clock) error {
	retry, err := retryutils.NewLinear(retryutils.LinearConfig{
		Clock:  clock,
		Max:    3 * time.Minute,
		Step:   time.Minute,
		Jitter: retryutils.NewHalfJitter(),
	})
	if err != nil {
		return trace.Wrap(err)
	}

	originalExpiry := s.tracker.Expiry()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.closeC:
			return nil
		case <-retry.After():
			retry.Inc()

			// try sending another update
			err := s.UpdateExpiration(ctx, clock.Now().Add(apidefaults.SessionTrackerTTL))

			// update was successful return
			if err == nil {
				return nil
			}

			// the tracker wasn't found which means we were
			// able to reach the auth server, but the tracker
			// no longer exists and likely expired
			if trace.IsNotFound(err) {
				return trace.Wrap(err)
			}

			// the tracker has grown stale and retrying
			// can be aborted
			if clock.Now().UTC().After(originalExpiry.UTC()) {
				return trace.Wrap(err)
			}
		}
	}
}

func (s *SessionTracker) UpdateExpiration(ctx context.Context, expiry time.Time) error {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	s.tracker.SetExpiry(expiry)
	s.trackerCond.Broadcast()

	if s.service != nil {
		err := s.service.UpdateSessionTracker(ctx, &proto.UpdateSessionTrackerRequest{
			SessionID: s.tracker.GetSessionID(),
			Update: &proto.UpdateSessionTrackerRequest_UpdateExpiry{
				UpdateExpiry: &proto.SessionTrackerUpdateExpiry{
					Expires: &expiry,
				},
			},
		})
		return trace.Wrap(err)
	}
	return nil
}

func (s *SessionTracker) AddParticipant(ctx context.Context, p *types.Participant) error {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	s.tracker.AddParticipant(*p)
	s.trackerCond.Broadcast()

	if s.service != nil {
		err := s.service.UpdateSessionTracker(ctx, &proto.UpdateSessionTrackerRequest{
			SessionID: s.tracker.GetSessionID(),
			Update: &proto.UpdateSessionTrackerRequest_AddParticipant{
				AddParticipant: &proto.SessionTrackerAddParticipant{
					Participant: p,
				},
			},
		})
		return trace.Wrap(err)
	}

	return nil
}

func (s *SessionTracker) RemoveParticipant(ctx context.Context, participantID string) error {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	s.tracker.RemoveParticipant(participantID)
	s.trackerCond.Broadcast()

	if s.service != nil {
		err := s.service.UpdateSessionTracker(ctx, &proto.UpdateSessionTrackerRequest{
			SessionID: s.tracker.GetSessionID(),
			Update: &proto.UpdateSessionTrackerRequest_RemoveParticipant{
				RemoveParticipant: &proto.SessionTrackerRemoveParticipant{
					ParticipantID: participantID,
				},
			},
		})
		return trace.Wrap(err)
	}

	return nil
}

func (s *SessionTracker) UpdateState(ctx context.Context, state types.SessionState) error {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	s.tracker.SetState(state)
	s.trackerCond.Broadcast()

	if s.service != nil {
		err := s.service.UpdateSessionTracker(ctx, &proto.UpdateSessionTrackerRequest{
			SessionID: s.tracker.GetSessionID(),
			Update: &proto.UpdateSessionTrackerRequest_UpdateState{
				UpdateState: &proto.SessionTrackerUpdateState{
					State: state,
				},
			},
		})
		return trace.Wrap(err)
	}

	return nil
}

// WaitForStateUpdate waits for the tracker's state to be updated and returns the new state.
func (s *SessionTracker) WaitForStateUpdate(initialState types.SessionState) types.SessionState {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()

	for {
		if state := s.tracker.GetState(); state != initialState {
			return state
		}
		s.trackerCond.Wait()
	}
}

// WaitOnState waits until the desired state is reached or the context is canceled.
func (s *SessionTracker) WaitOnState(ctx context.Context, wanted types.SessionState) error {
	go func() {
		<-ctx.Done()
		s.trackerCond.Broadcast()
	}()

	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if s.tracker.GetState() == wanted {
				return nil
			}

			s.trackerCond.Wait()
		}
	}
}

func (s *SessionTracker) GetState() types.SessionState {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	return s.tracker.GetState()
}

func (s *SessionTracker) GetParticipants() []types.Participant {
	s.trackerCond.L.Lock()
	defer s.trackerCond.L.Unlock()
	return s.tracker.GetParticipants()
}
