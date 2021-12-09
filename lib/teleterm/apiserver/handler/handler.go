// Copyright 2021 Gravitational, Inc
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
	"github.com/gravitational/teleport/lib/teleterm/daemon"
	"github.com/gravitational/trace"
)

func New(cfg Config) (*Handler, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Handler{
		cfg,
	}, nil
}

// Config is the terminal service
type Config struct {
	// DaemonService is the instance of daemon service
	DaemonService *daemon.Service
}

func (c *Config) CheckAndSetDefaults() error {
	if c.DaemonService == nil {
		return trace.BadParameter("missing Daemon Service")
	}

	return nil
}

// Handler implements teleterm api service
type Handler struct {
	// Config is the service config
	Config
}
