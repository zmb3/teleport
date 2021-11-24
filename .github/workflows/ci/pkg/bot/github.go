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
	"context"

	"github.com/gravitational/trace"

	"github.com/google/go-github/v37/github"
)

type gh interface {
	// RequestReviewers is used to assign reviewers to a PR.
	RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers github.ReviewersRequest) error

	// ListFiles is used to list all the files within a PR.
	ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error)

	//ListReviews(context.Context, int) error
	//DismissReview(context.Context) error
}

type ghClient struct {
	client *github.Client
}

func NewGithubClient() (*ghClient, error) {
	return &ghClient{}, nil
}

func (c *ghClient) RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers github.ReviewersRequest) error {
	return nil
}

func (c *ghClient) ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error) {
	var files []string

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

//func (g *ghClient) ListReviews(context.Context, int) error {
//}
//
//
//func (g *ghClient) DismissReview(context.Context) error {
//}
