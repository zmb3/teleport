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

package github

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/gravitational/trace"

	go_github "github.com/google/go-github/v37/github"
	"golang.org/x/oauth2"
)

type Client interface {
	// RequestReviewers is used to assign reviewers to a PR.
	RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers []string) error

	// ListReviews is used to list all submitted reviews for a PR.
	ListReviews(ctx context.Context, organization string, repository string, number int) (map[string]*Review, error)

	// ListPullRequests returns a list of Pull Requests.
	ListPullRequests(ctx context.Context, organization string, repository string, state string) ([]PullRequest, error)

	// ListFiles is used to list all the files within a PR.
	ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error)

	// ListWorkflows lists all workflows within a repository.
	ListWorkflows(ctx context.Context, organization string, repository string) ([]Workflow, error)

	// ListWorkflowRuns is used to list all workflow runs for an ID.
	ListWorkflowRuns(ctx context.Context, organization string, repository string, branch string, workflowID int64) ([]Run, error)

	// DeleteWorkflowRun is used to delete a workflow run.
	DeleteWorkflowRun(ctx context.Context, organization string, repository string, runID int64) error
}

type client struct {
	client *go_github.Client
}

func New(ctx context.Context, token string) (*client, error) {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	return &client{
		client: go_github.NewClient(oauth2.NewClient(ctx, ts)),
	}, nil
}

func (c *client) RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers []string) error {
	_, _, err := c.client.PullRequests.RequestReviewers(ctx,
		organization,
		repository,
		number,
		go_github.ReviewersRequest{
			Reviewers: reviewers,
		})
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

type Review struct {
	Author      string
	State       string
	CommitID    string
	SubmittedAt time.Time
}

// TODO(russjones): Field validation.
// TODO(russjones): Break after n iterations to prevent an infinite loop.
func (c *client) ListReviews(ctx context.Context, organization string, repository string, number int) (map[string]*Review, error) {
	var reviews map[string]*Review

	opt := &go_github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.client.PullRequests.ListReviews(ctx,
			organization,
			repository,
			number,
			opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, r := range page {
			// Always pick up the last submitted review.
			review, ok := reviews[r.GetUser().GetLogin()]
			if ok {
				if r.GetSubmittedAt().After(review.SubmittedAt) {
					review.State = r.GetState()
					review.CommitID = r.GetCommitID()
					review.SubmittedAt = r.GetSubmittedAt()
				}
			}

			reviews[r.GetUser().GetLogin()] = &Review{
				Author:      r.GetUser().GetLogin(),
				State:       r.GetState(),
				CommitID:    r.GetCommitID(),
				SubmittedAt: r.GetSubmittedAt(),
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return reviews, nil
}

type PullRequest struct {
	Author     string
	Repository string
	Head       string
}

// TODO(russjones): Field validation.
// TODO(russjones): Break after n iterations to prevent an infinite loop.
func (c *client) ListPullRequests(ctx context.Context, organization string, repository string, state string) ([]PullRequest, error) {
	var pulls []PullRequest

	opt := &go_github.PullRequestListOptions{
		State: state,
		ListOptions: go_github.ListOptions{
			Page:    0,
			PerPage: 100,
		},
	}
	for {
		page, resp, err := c.client.PullRequests.List(ctx,
			organization,
			repository,
			opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, pr := range page {
			pulls = append(pulls, PullRequest{
				Author:     pr.GetUser().GetLogin(),
				Repository: repository,
				Head:       pr.GetHead().GetRef(),
			})

			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	return pulls, nil
}

// TODO(russjones): Field validation.
// TODO(russjones): Break after n iterations to prevent an infinite loop.
func (c *client) ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error) {
	var files []string

	opt := &go_github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.client.PullRequests.ListFiles(ctx,
			organization,
			repository,
			number,
			opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, file := range page {
			files = append(files, file.GetFilename())
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return files, nil
}

type Workflow struct {
	ID   int64
	Name string
}

// TODO(russjones): Field validation.
// TODO(russjones): Break after n iterations to prevent an infinite loop.
func (c *client) ListWorkflows(ctx context.Context, organization string, repository string) ([]Workflow, error) {
	var workflows []Workflow

	opt := &go_github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.client.Actions.ListWorkflows(ctx,
			organization,
			repository,
			opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if page.Workflows == nil {
			log.Printf("Got empty page of workflows for %v.", repository)
			continue
		}

		for _, workflow := range page.Workflows {
			workflows = append(workflows, Workflow{
				Name: workflow.GetName(),
				ID:   workflow.GetID(),
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return workflows, nil
}

type Run struct {
	ID        int64
	CreatedAt time.Time
}

// TODO(russjones): Field validation.
// TODO(russjones): Break after n iterations to prevent an infinite loop.
func (c *client) ListWorkflowRuns(ctx context.Context, organization string, repository string, branch string, workflowID int64) ([]Run, error) {
	var runs []Run

	opt := &go_github.ListWorkflowRunsOptions{
		Branch: branch,
		ListOptions: go_github.ListOptions{
			Page:    0,
			PerPage: 100,
		},
	}
	for {
		page, resp, err := c.client.Actions.ListWorkflowRunsByID(ctx,
			organization,
			repository,
			workflowID,
			opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if page.WorkflowRuns == nil {
			log.Printf("Got empty page of workflow runs for branch: %v, workflowID: %v.", branch, workflowID)
			continue
		}

		for _, run := range page.WorkflowRuns {
			runs = append(runs, Run{
				ID:        run.GetID(),
				CreatedAt: run.GetCreatedAt().Time,
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return runs, nil
}

// DeleteWorkflowRun is directly implemented because it is missing from go-github.
//
// https://docs.github.com/en/rest/reference/actions#delete-a-workflow-run
func (c *client) DeleteWorkflowRun(ctx context.Context, organization string, repository string, runID int64) error {
	url := url.URL{
		Scheme: "https",
		Host:   "api.github.com",
		Path:   path.Join("repos", organization, repository, "actions", "runs", strconv.FormatInt(runID, 10)),
	}
	req, err := c.client.NewRequest(http.MethodDelete, url.String(), nil)
	if err != nil {
		return trace.Wrap(err)
	}
	_, err = c.client.Do(ctx, req, nil)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}
