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

package bot

import (
	"github.com/gravitational/teleport/.github/workflows/ci/internal/env"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/review"

	"github.com/gravitational/trace"
)

type Config struct {
	// GitHub is a GitHub client.
	GitHub github.Client

	// Environment holds information about the workflow run event.
	Environment *env.Environment

	// Reviewer is used to get code and docs reviewers.
	Reviewer *review.Assignments
}

func (c *Config) CheckAndSetDefaults() error {
	if c.GitHub == nil {
		return trace.BadParameter("github client required")
	}
	if c.Environment == nil {
		return trace.BadParameter("environment event required")
	}
	if c.Reviewer == nil {
		return trace.BadParameter("reviewers missing")
	}

	return nil
}

type Bot struct {
	c *Config
}

func New(c *Config) (*Bot, error) {
	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Bot{
		c: c,
	}, nil
}
