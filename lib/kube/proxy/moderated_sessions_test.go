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

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/lib/events"
	testingkubemock "github.com/gravitational/teleport/lib/kube/proxy/testing/kube_server"
	"github.com/gravitational/teleport/lib/modules"
)

func TestModeratedSessions(t *testing.T) {
	// enable enterprise features to have access to ModeratedSessions.
	modules.SetTestModules(t, &modules.TestModules{TestBuildType: modules.BuildEnterprise})
	const (
		moderatorUsername       = "moderator_user"
		moderatorRoleName       = "mod_role"
		userRequiringModeration = "user_wmod"
		roleRequiringModeration = "role_wmod"
		stdinPayload            = "sessionPayload"
		discardPayload          = "discardPayload"
		exitKeyword             = "exit"
	)
	var (
		// numSessionEndEvents represents the number of session.end events received
		// from audit events.
		numSessionEndEvents = new(int64)
		// numberOfExpectedSessionEnds is the number of session ends expected from
		// all tests. This value is incremented each time that a test has
		// tt.want.sessionEndEvent = true.
		numberOfExpectedSessionEnds int64
	)
	// kubeMock is a Kubernetes API mock for the session tests.
	// Once a new session is created, this mock will write to
	// stdout and stdin (if available) the pod name, followed
	// by copying the contents of stdin into both streams.
	kubeMock, err := testingkubemock.NewKubeAPIMock()
	require.NoError(t, err)
	t.Cleanup(func() { kubeMock.Close() })

	// creates a Kubernetes service with a configured cluster pointing to mock api server
	testCtx := setupTestContext(
		context.Background(),
		t,
		testConfig{
			clusters: []kubeClusterConfig{{name: kubeCluster, apiEndpoint: kubeMock.URL}},
			// onEvent is called each time a new event is produced. We only care about
			// sessionEnd events.
			onEvent: func(ae apievents.AuditEvent) {
				if ae.GetType() == events.SessionEndEvent {
					atomic.AddInt64(numSessionEndEvents, 1)
				}
			},
		},
	)
	// close tests
	t.Cleanup(func() { require.NoError(t, testCtx.Close()) })

	t.Cleanup(func() {
		// Set a cleanup function to make sure it runs after every test completes.
		// It will validate if the # of session ends is the same as expected
		// number of session ends.
		require.Eventually(t, func() bool {
			// checks if the # of session ends is the same as expected
			// number of session ends.
			return atomic.LoadInt64(numSessionEndEvents) == numberOfExpectedSessionEnds
		}, 20*time.Second, 500*time.Millisecond)
	})

	// create a user with access to kubernetes that does not require any moderator.
	// (kubernetes_user and kubernetes_groups specified)
	user, _ := testCtx.createUserAndRole(
		testCtx.ctx,
		t,
		username,
		roleSpec{
			name:       roleName,
			kubeUsers:  roleKubeUsers,
			kubeGroups: roleKubeGroups,
		})

	// create a moderator user with access to kubernetes
	// (kubernetes_user and kubernetes_groups specified)
	moderator, modRole := testCtx.createUserAndRole(
		testCtx.ctx,
		t,
		moderatorUsername,
		roleSpec{
			name:       moderatorRoleName,
			kubeUsers:  roleKubeUsers,
			kubeGroups: roleKubeGroups,
			// sessionJoin:
			sessionJoin: []*types.SessionJoinPolicy{
				{
					Name:  "Auditor oversight",
					Roles: []string{"*"},
					Kinds: []string{"k8s"},
					Modes: []string{string(types.SessionModeratorMode)},
				},
			},
		})

	// create a userRequiringModerator with access to kubernetes thar requires
	// one moderator to join the session.
	userRequiringModerator, _ := testCtx.createUserAndRole(
		testCtx.ctx,
		t,
		userRequiringModeration,
		roleSpec{
			name:       roleRequiringModeration,
			kubeUsers:  roleKubeUsers,
			kubeGroups: roleKubeGroups,
			sessionRequire: []*types.SessionRequirePolicy{
				{
					Name:   "Auditor oversight",
					Filter: fmt.Sprintf("contains(user.spec.roles, %q)", modRole.GetName()),
					Kinds:  []string{"k8s"},
					Modes:  []string{string(types.SessionModeratorMode)},
					Count:  1,
				},
			},
		})

	type args struct {
		user                 types.User
		moderator            types.User
		closeSession         bool
		moderatorForcedClose bool
	}
	type want struct {
		sessionEndEvent bool
	}
	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "create session for user without moderation",
			args: args{
				user: user,
			},
			want: want{
				sessionEndEvent: true,
			},
		},
		{
			name: "create session with moderation",
			args: args{
				user:      userRequiringModerator,
				moderator: moderator,
			},
			want: want{
				sessionEndEvent: true,
			},
		},
		{
			name: "create session without needing moderation but leave it without proper closing",
			args: args{
				user:         user,
				closeSession: true,
			},
			want: want{
				sessionEndEvent: true,
			},
		},
		{
			name: "create session with moderation but close connection before moderator joined",
			args: args{
				user:         userRequiringModerator,
				closeSession: true,
			},
			want: want{
				// until moderator joins the session is not started. If the connection
				// is closed, Teleport does not create any start or end events.
				sessionEndEvent: false,
			},
		},
		{
			name: "create session with moderation and once the session is active, the moderator closes it",
			args: args{
				user:                 userRequiringModerator,
				moderator:            moderator,
				moderatorForcedClose: true,
			},
			want: want{
				sessionEndEvent: true,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		if tt.want.sessionEndEvent {
			numberOfExpectedSessionEnds++
		}
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			group := &errgroup.Group{}

			// generate a kube client with user certs for auth
			_, config := testCtx.genTestKubeClientTLSCert(
				t,
				tt.args.user.GetName(),
				kubeCluster,
			)

			// create a user session.
			var (
				stdinReader, stdinWritter   = io.Pipe()
				stdoutReader, stdoutWritter = io.Pipe()
			)
			streamOpts := remotecommand.StreamOptions{
				Stdin:  stdinReader,
				Stdout: stdoutWritter,
				// when Tty is enabled, Stderr must be nil.
				Stderr: nil,
				Tty:    true,
			}
			req, err := generateExecRequest(
				testCtx.KubeServiceAddress(),
				podName,
				podNamespace,
				podContainerName,
				containerCommmandExecute, // placeholder for commands to execute in the dummy pod
				streamOpts,
			)
			require.NoError(t, err)

			exec, err := remotecommand.NewSPDYExecutor(config, http.MethodPost, req.URL())
			require.NoError(t, err)

			// sessionIDC is used to share the sessionID between the user exec request and
			// the moderator. Once the moderator receives it, he can join the session.
			sessionIDC := make(chan string, 1)
			// moderatorJoined is used to syncronize when the moderator joins the session.
			moderatorJoined := make(chan struct{})

			if tt.args.moderator != nil {
				// generate moderator certs
				_, config := testCtx.genTestKubeClientTLSCert(
					t,
					tt.args.moderator.GetName(),
					kubeCluster,
				)

				// Simulate a moderator joining the session.
				group.Go(func() error {
					// waits for user to send the sessionID of his exec request.
					sessionID := <-sessionIDC
					t.Logf("moderator is joining sessionID %q", sessionID)
					// join the session.
					stream, err := testCtx.newJoiningSession(config, sessionID, types.SessionModeratorMode)
					if err != nil {
						return trace.Wrap(err)
					}
					// always send the force terminate even when the session is normally closed.
					defer func() {
						stream.ForceTerminate()
					}()

					// moderator waits for the user informed that he joined the session.
					<-moderatorJoined
					for {
						p := make([]byte, 1024)
						n, err := stream.Read(p)
						if err != nil {
							return trace.Wrap(err)
						}
						stringData := string(p[:n])
						// discardPayload is sent before the moderator joined the session.
						// If the moderator sees it, it means that the payload was not correctly
						// discarded.
						if strings.Contains(stringData, discardPayload) {
							return trace.Wrap(errors.New("discardPayload was not properly discarded"))
						}

						// stdinPayload is sent by the user after the session started.
						if strings.Contains(stringData, stdinPayload) {
							break
						}

						// podContainerName is returned by the kubemock server and it's used
						// to control that the session has effectively started.
						// return to force the defer to run.
						if strings.Contains(stringData, podContainerName) && tt.args.moderatorForcedClose {
							return nil
						}
					}
					return nil
				})
			}

			// Simulate a user creating a session.
			group.Go(func() (err error) {
				// sessionIDRegex is used to parse the sessionID from the payload sent
				// to the user when moderation is needed
				sessionIDRegex := regexp.MustCompile(`(?m)ID: (.*)\.\.\.`)
				// identifiedData is used to control if the stdin data was correctly
				// received from the session TermManager.
				identifiedData := false
				defer func() {
					// close pipes.
					stdinWritter.Close()
					stdoutReader.Close()
					// validate if the data payload was received.
					// When force closing (user or moderator) we do not expect identifiedData
					// to be true.
					if err == nil && !identifiedData && !tt.args.closeSession && !tt.args.moderatorForcedClose {
						err = errors.New("data payload was not identified")
					}
				}()
				for {
					data := make([]byte, 1024)
					n, err := stdoutReader.Read(data)
					// ignore EOF and ErrClosedPipe errors
					if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
						return nil
					} else if err != nil {
						return trace.Wrap(err)
					}

					if tt.args.closeSession {
						return nil
					}

					stringData := string(data[:n])
					if sessionIDRegex.MatchString(stringData) {
						matches := sessionIDRegex.FindAllStringSubmatch(stringData, -1)
						sessionID := matches[0][1]
						t.Logf("sessionID identified %q", sessionID)

						// if sessionID is identified it means that the user waits for
						// a moderator to join. In the meanwhile we write to stdin to make sure it's
						// discarded.
						_, err := stdinWritter.Write([]byte(discardPayload))
						if err != nil {
							return trace.Wrap(err)
						}

						// send sessionID to moderator.
						sessionIDC <- sessionID
					}

					// checks if moderator has joined the session.
					// Each time a user joins a session the following message is broadcasted
					// User <user> joined the session.
					if strings.Contains(stringData, moderatorUsername) {
						t.Logf("identified that moderator joined the session")
						// inform moderator goroutine that the user detected that he joined the
						// session.
						close(moderatorJoined)
					}

					// if podContainerName is received, it means the session has already reached
					// the mock server. If we expect the moderated to force close the session
					// we don't send the stdinPayload data and the session will remain active.
					if strings.Contains(stringData, podContainerName) && !tt.args.moderatorForcedClose {
						// successfully connected to
						_, err := stdinWritter.Write([]byte(stdinPayload))
						if err != nil {
							return trace.Wrap(err)
						}
					}

					// check if we received the data we wrote into the stdin pipe.
					if strings.Contains(stringData, stdinPayload) {
						t.Logf("received the payload written to stdin")
						identifiedData = true
						break
					}
				}
				return nil
			},
			)

			group.Go(func() error {
				defer func() {
					// once stream is finished close the pipes.
					stdinReader.Close()
					stdoutWritter.Close()
				}()
				// start user session.
				err := exec.StreamWithContext(testCtx.ctx, streamOpts)
				// ignore closed pipes error.
				if errors.Is(err, io.ErrClosedPipe) {
					return nil
				}
				return trace.Wrap(err)
			})
			// wait for every go-routine to finish without errors returned.
			require.NoError(t, group.Wait())
		})
	}
}
