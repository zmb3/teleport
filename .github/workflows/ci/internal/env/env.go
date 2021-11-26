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

package env

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/gravitational/trace"
)

type Environment struct {
	Organization string
	Repository   string
	Number       int
	Author       string
	Branch       string
}

func Read() (*Environment, error) {
	e, err := readEvent()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var en Environment

	// Read in the organization and repository name from the workflow event. If
	// it's missing (like in a cron workflow), then read it in from GITHUB_REPOSITORY.
	en.Organization = e.Repository.Owner.Login
	en.Repository = e.Repository.Name
	if en.Organization == "" || en.Repository == "" {
		en.Organization, en.Repository, err = readEnvironment()
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	en.Number = e.PullRequest.Number
	en.Author = e.PullRequest.User.Login

	// TODO(russjones): Can this field be trusted?
	en.Branch = e.PullRequest.Head.Ref

	return &en, nil
}

//
// https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions#understanding-the-risk-of-script-injections
func readEvent() (*Event, error) {
	f, err := os.Open(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer f.Close()

	var e Event
	if err := json.NewDecoder(f).Decode(&e); err != nil {
		return nil, trace.Wrap(err)
	}

	return &e, nil
}

func readEnvironment() (string, string, error) {
	parts := strings.Split(os.Getenv("GITHUB_REPOSITORY"), "/")
	if len(parts) != 2 {
		return "", "", trace.BadParameter("failed to get organization and/or repository")
	}
	if parts[0] == "" || parts[1] == "" {
		return "", "", trace.BadParameter("invalid organization and/or repository")
	}
	return parts[0], parts[1], nil
}
