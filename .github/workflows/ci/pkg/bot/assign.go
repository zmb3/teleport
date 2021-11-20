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
	"strings"

	"github.com/gravitational/teleport/.github/workflows/ci"

	"github.com/gravitational/trace"

	"github.com/google/go-github/v37/github"
)

func (b *Bot) Assign(ctx context.Context) error {
	c := b.Environment.Client
	pr := b.Environment.Metadata

	reviewers, err := b.getReviewers()
	if err != nil {
		return trace.Wrap(err)
	}

	_, _, err = c.PullRequests.RequestReviewers(ctx,
		pr.RepoOwner, pr.RepoName, pullReq.Number,
		github.ReviewersRequest{
			Reviewers: reviewers,
		})
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// getReviewers will parse the PR, discover the changes, and return a set of
// reviewers determined by the content of the PR, if the author is internal or
// external, and team they are on.
func (b *Bot) getReviewers(ctx context.Context) ([]string, error) {
	pr := b.Environment.Metadata

	docs, code, err := b.parseChanges(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var reviewers []string

	switch {
	case docs && code:
		reviewers = append(reviewers, docsReviewers)
		reviewers = append(reviewers, GetCodeReviewers(pr.Author))
	case !docs && code:
		reviewers = append(reviewers, GetCodeReviewers(pr.Author))
	case docs && !code:
		reviewers = append(reviewers, docsReviewers)
	case !docs && !code:
		return defaultReviewers, nil
	}

	return reviewers, nil

}

func (b *Bot) parseChanges(ctx context.Context) (bool, bool, error) {
	var docs bool
	var code bool

	files, err := getFiles(ctx)
	if err != nil {
		return false, true, trace.Wrap(err)
	}

	for _, file := range files {
		if hasDocChanges(file) {
			docs = true
		} else {
			code = true
		}

	}
	return docs, code, nil
}

// getFiles returns a slice of files within the PR.
func (b *Bot) getFiles() ([]string, error) {
	c := b.Environment.Client
	pr := b.Environment.Metadata

	var files []string

	opt := &github.ListOptions{
		Page:    0,
		PerPage: 100,
	}
	for {
		page, resp, err := c.PullRequests.ListFiles(ctx, pr.RepoOwner, pr.RepoName, pr.Number, opt)
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

	return file, nil
}

func hasDocChanges(filename string) bool {
	if strings.HasPrefix(filename, ci.VendorPrefix) {
		return false
	}
	return strings.HasPrefix(filename, ci.DocsPrefix) ||
		strings.HasSuffix(filename, ci.MdSuffix) ||
		strings.HasSuffix(filename, ci.MdxSuffix) ||
		strings.HasPrefix(filename, ci.RfdPrefix)
}
