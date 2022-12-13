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
	"github.com/gravitational/trace"

	api "github.com/zmb3/teleport/lib/teleterm/api/protogen/golang/v1"
)

func (s *Handler) TransferFile(request *api.FileTransferRequest, server api.TerminalService_TransferFileServer) error {
	err := s.DaemonService.TransferFile(server.Context(), request, server.Send)
	return trace.Wrap(err)
}
