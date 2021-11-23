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

import "context"

type github interface {
	ListFiles(context.Context, int) ([]string, error)
	ListReviews(context.Context, int) error
	RequestReviewers(context.Context)
	DismissReview(context.Context) error
}

type githubClient struct {
}

func newGithubClient() (*githubClient, error) {
	return nil, nil
}

func (g *github) ListFiles(context.Context, int) ([]string, error) {
}

func (g *github) ListReviews(context.Context, int) error {
}

func (g *github) RequestReviewers(context.Context) {
}

func (g *github) DismissReview(context.Context) error {
}
