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

package github

import (
	"context"
	"log"
	"time"

	"github.com/google/go-github/v41/github"
	"github.com/gravitational/trace"
)

func setsAreEqual(a, b map[int64]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, present := b[k]; !present {
			return false
		}
	}
	return true
}

func firstUniqueInSet(newSet, baseSet map[int64]struct{}) (int64, bool) {
	for k, _ := range newSet {
		if _, present := baseSet[k]; !present {
			return k, true
		}
	}
	return 0, false
}

// ListWorkflowRuns returns a set of RunIDs, representing the set of all for
// workflow runs created since the supplied start time.
func (gh *ghClient) ListWorkflowRuns(ctx context.Context, owner, repo, path, ref string, since time.Time) (map[int64]struct{}, error) {
	listOptions := github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
		Branch:  ref,
		Created: ">" + since.Format(time.RFC3339),
	}

	runIDs := make(map[int64]struct{})

	for {
		runs, resp, err := gh.client.Actions.ListWorkflowRunsByFileName(ctx, owner, repo, path, &listOptions)
		if err != nil {
			return nil, trace.Wrap(err, "Failed to fetch runs")
		}

		for _, r := range runs.WorkflowRuns {
			runIDs[r.GetID()] = struct{}{}
		}

		if resp.NextPage == 0 {
			break
		}

		listOptions.Page = resp.NextPage
	}

	return runIDs, nil
}

// TriggerDispatchEvent triggers a workflow_dispatch event in the target
// repository and waits for a workflow to be started in response. Note that
// this method requires that the GitHub and client clocks are roughly in sync.
func (gh *ghClient) TriggerDispatchEvent(ctx context.Context, owner, repo, workflow, ref string, inputs map[string]interface{}) (*github.WorkflowRun, error) {
	// There is no way that I know of to 100% accurately detect which workflow runs
	// are created in response to a workflow_dispatch event. We can make a vey good
	// guess, though, by looking at what workflow runs (with matching filename and
	// source references) start immediately after we issue the event - so that's
	// what we do here.

	// Determine what workflows runs have already been created before we start, so
	// we can exclude them when trying to detect a new run started in response to
	// our dispatch event. Note that we pick a time slightly in the past to handle
	// any clock skew.
	baselineTime := time.Now().Add(-2 * time.Minute)
	oldRuns, err := gh.ListWorkflowRuns(ctx, owner, repo, workflow, ref, baselineTime)
	if err != nil {
		return nil, trace.Wrap(err, "Failed to fetch task list")
	}

	log.Printf("Attempting to trigger %s/%s %s at ref %s\n", owner, repo, workflow, ref)
	triggerArgs := github.CreateWorkflowDispatchEventRequest{
		Ref:    ref,
		Inputs: inputs,
	}

	// Issue the workflow_dispatch event.
	_, err = gh.client.Actions.CreateWorkflowDispatchEventByFileName(ctx, owner, repo, workflow, triggerArgs)
	if err != nil {
		return nil, trace.Wrap(err, "Failed to issue request")
	}

	// Now we poll the GitHub API to see if any new Workflow Runs appear.We do this until
	// the caller-supplied context expires, so be sure to set a timeout.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	newRuns := make(map[int64]struct{})
	for k, _ := range oldRuns {
		newRuns[k] = struct{}{}
	}

	log.Printf("Waiting for new workflow run to start")

	// Remember that the set of RunIDs includes completed runs as well as any
	// in flight, so we don't have to account for expiring run IDs in our "old"
	// set.
	for setsAreEqual(newRuns, oldRuns) {
		select {
		case <-ticker.C:
			newRuns, err = gh.ListWorkflowRuns(ctx, owner, repo, workflow, ref, baselineTime)
			if err != nil {
				return nil, trace.Wrap(err, "Failed to fetch task list")
			}

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Find the first runID in the new set that is not in the old set and deem
	// that to be our workflow of interest.
	runID, ok := firstUniqueInSet(newRuns, oldRuns)
	if !ok {
		return nil, trace.Errorf("Unable to detect new run ID")
	}

	log.Printf("Started workflow run ID %d", runID)

	// Fetch the run info
	run, _, err := gh.client.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
	if err != nil {
		return nil, trace.Wrap(err, "Failed polling run")
	}

	return run, nil
}

func (gh *ghClient) WaitForRun(ctx context.Context, owner, repo, path, ref string, runID int64) (string, error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			run, _, err := gh.client.Actions.GetWorkflowRunByID(ctx, owner, repo, runID)
			if err != nil {
				return "", trace.Wrap(err, "Failed polling run")
			}

			log.Printf("Workflow status: %s", run.GetStatus())

			if run.GetStatus() == "completed" {
				return run.GetConclusion(), nil
			}

		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}