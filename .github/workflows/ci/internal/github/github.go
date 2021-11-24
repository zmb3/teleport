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

	"github.com/gravitational/trace"

	"github.com/google/go-github/v37/github"
)

type Client interface {
	// RequestReviewers is used to assign reviewers to a PR.
	RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers []string) error

	// ListFiles is used to list all the files within a PR.
	ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error)

	// ListReviews is used to list all submitted reviews for a PR.
	ListReviews(ctx context.Context, organization string, repository string, number int) ([]Review, error)

	//DismissReview(context.Context) error
}

type client struct {
	client *github.Client
}

func NewClient() (*client, error) {
	return &client{}, nil
}

func (c *client) RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers []string) error {
	_, _, err := c.client.PullRequests.RequestReviewers(ctx,
		organization,
		repository,
		number,
		github.ReviewersRequest{
			Reviewers: reviewers,
		})
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (c *client) ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error) {
	var files []string

	// TODO(russjones): Break after n iterations to prevent an infinite loop.
	opt := &github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.client.PullRequests.ListFiles(ctx, organization, repository, number, opt)
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

type Review struct {
	Author string
	State  string
	//commitID    string
	//id          int64
	//submittedAt time.Time
}

func (c *client) ListReviews(ctx context.Context, organization string, repository string, number int) ([]Review, error) {
	var reviews []Review

	// TODO(russjones): Break after n iterations to prevent an infinite loop.
	opt := &github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.client.PullRequests.ListReviews(ctx, organization, repository, number, opt)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, r := range page {
			reviews = append(reviews, Review{
				Author: r.GetUser().GetLogin(),
				State:  r.GetState(),
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return reviews, nil
}

//func (g *ghClient) ListReviews(context.Context, int) error {
//}
//
//
//func (g *ghClient) DismissReview(context.Context) error {
//}
