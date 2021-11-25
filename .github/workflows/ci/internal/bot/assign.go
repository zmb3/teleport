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
	"math/rand"
	"strings"

	"github.com/gravitational/teleport/.github/workflows/ci/internal"

	"github.com/gravitational/trace"
)

// Assign will assign reviewers for this PR.
//
// Assign works by parsing the PR, discovering the changes, and returning a
// set of reviewers determined by: content of the PR, if the author is internal
// or external, and team they are on.
func (b *Bot) Assign(ctx context.Context) error {
	reviewers, err := b.getReviewers(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	// Request GitHub assign reviewers to this PR.
	err = b.c.gh.RequestReviewers(ctx,
		b.c.env.Organization,
		b.c.env.Repository,
		b.c.env.Number,
		reviewers)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (b *Bot) getReviewers(ctx context.Context) ([]string, error) {
	docs, code, err := b.parseChanges(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var reviewers []string

	switch {
	case docs && code:
		reviewers = append(reviewers, b.getDocsReviewers(b.c.env.Author)...)
		reviewers = append(reviewers, b.getCodeReviewers(b.c.env.Author)...)
	case !docs && code:
		reviewers = append(reviewers, b.getCodeReviewers(b.c.env.Author)...)
	case docs && !code:
		reviewers = append(reviewers, b.getDocsReviewers(b.c.env.Author)...)
	case !docs && !code:
		reviewers = append(reviewers, b.getCodeReviewers(b.c.env.Author)...)
	}

	return reviewers, nil

}

func (b *Bot) getDocsReviewers(author string) []string {
	return b.c.r.GetDocsReviewers(author)
}

func (b *Bot) getCodeReviewers(author string) []string {
	setA, setB := b.c.r.GetAssigningSets(author)

	return []string{
		setA[rand.Intn(len(setA))],
		setB[rand.Intn(len(setB))],
	}
}

func (b *Bot) parseChanges(ctx context.Context) (bool, bool, error) {
	var docs bool
	var code bool

	files, err := b.c.gh.ListFiles(ctx,
		b.c.env.Organization,
		b.c.env.Repository,
		b.c.env.Number)
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

func hasDocChanges(filename string) bool {
	if strings.HasPrefix(filename, internal.VendorPrefix) {
		return false
	}
	return strings.HasPrefix(filename, internal.DocsPrefix) ||
		strings.HasSuffix(filename, internal.MdSuffix) ||
		strings.HasSuffix(filename, internal.MdxSuffix) ||
		strings.HasPrefix(filename, internal.RfdPrefix)
}
