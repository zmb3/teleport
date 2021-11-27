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

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/gravitational/teleport/.github/workflows/ci/internal/bot"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/env"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/review"

	"github.com/gravitational/trace"
)

func main() {
	var token = flag.String("token", "", "token is the Github authentication token.")
	flag.Parse()

	if len(os.Args) < 2 {
		log.Fatalf("Subcommand required. %s\n", usage)
	}
	subcommand := os.Args[len(os.Args)-1]

	// Cancel run if it takes longer than `workflowRunTimeout`.
	// Note: To re-run a job go to the Actions tab in the Github repo,
	// go to the run that failed, and click the `Re-run all jobs` button
	// in the top right corner.
	ctx, cancel := context.WithTimeout(context.Background(), workflowTimeout)
	defer cancel()

	b, err := createBot(ctx, *token)
	if err != nil {
		log.Fatalf("Failed to create bot: %v.", err)
	}

	switch subcommand {
	case "assign":
		err = b.Assign(ctx)
	case "check":
		err = b.Check(ctx)
	case "dismiss":
		err = b.Dimiss(ctx)
	default:
		err = trace.BadParameter("unknown subcommand: %v", subcommand)
	}
	if err != nil {
		log.Fatal("Subcommand %v failed: %v.", subcommand, err)
	}
}

func createBot(ctx context.Context, token string) (*bot.Bot, error) {
	gh, err := github.New(ctx, token)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	environment, err := env.New()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	reviewer, err := review.New(review.DefaultConfig)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	b, err := bot.New(&bot.Config{
		GitHub:      gh,
		Environment: environment,
		Reviewer:    reviewer,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	return b, nil
}

const (
	usage = `The following subcommands are supported:
  assign     assigns reviewers to a pull request
  check      checks pull request for required reviewers
  dismiss    dismisses stale workflow runs for external contributors`

	workflowTimeout = 1 * time.Minute
)
