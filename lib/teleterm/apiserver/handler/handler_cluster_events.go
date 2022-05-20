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

package handler

import (
	// "context"
	"fmt"
	"io"

	api "github.com/gravitational/teleport/lib/teleterm/api/protogen/golang/v1"
	// "github.com/gravitational/teleport/lib/teleterm/clusters"

	"github.com/gravitational/trace"
)

// TODO: So this place needs to handle all incoming/outgoing stream messages. It should coordinate
// with the rest of the daemon through channels probably.
//
// TODO: RetryWithRelogin needs to be able to send CertExpired through the stream.
//
// There probably needs to be one goroutine that sends the messages to the stream and one goroutine
// that reads the messages from the stream and propagates them further.
//
// Maybe this will be helpful?
// https://dev.to/techschoolguru/implement-bidirectional-streaming-grpc-go-4kgn
func (s *Handler) ClusterEvents(stream api.TerminalService_ClusterEventsServer) error {
	ctx := stream.Context()
	count := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return trace.Wrap(err)
		}

		count += 1
		fmt.Printf("handler_cluster_events: %+v\n", req)

		stream.Send(&api.ClusterServerEvent{
			ClusterUri: "/clusters/teleport-local",
			Event:      &api.ClusterServerEvent_CertExpired{},
		})
	}
}
