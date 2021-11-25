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
	"testing"

	"github.com/gravitational/teleport/.github/workflows/ci/internal/env"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"
	"github.com/gravitational/teleport/.github/workflows/ci/internal/review"

	"github.com/stretchr/testify/require"
)

// TestGetReviewers checks if a PR can be parsed and appropriately assigned a
// docs or code reviewer.
func TestGetReviewers(t *testing.T) {
	r, err := review.NewAssignments(&review.Config{
		// Code reviewers.
		CodeReviewers: map[string]review.Reviewer{
			"1": review.Reviewer{Group: "Core", Set: "A"},
			"2": review.Reviewer{Group: "Core", Set: "A"},
			"3": review.Reviewer{Group: "Core", Set: "B"},
			"4": review.Reviewer{Group: "Core", Set: "B"},
		},
		CodeReviewersOmit: map[string]bool{
			"4": true,
		},
		// Docs reviewers.
		DocsReviewers: map[string]review.Reviewer{
			"5": review.Reviewer{Group: "Core", Set: "A"},
		},
		DocsReviewersOmit: map[string]bool{},
		// Default reviewers.
		DefaultReviewers: []string{"1", "2"},
	})
	require.NoError(t, err)

	tests := []struct {
		desc      string
		files     []string
		author    string
		reviewers []string
	}{
		{
			desc: "code-only",
			files: []string{
				"file.go",
			},
			author:    "1",
			reviewers: []string{"2", "3"},
		},
		{
			desc:      "docs-only",
			files:     []string{"docs/docs.md"},
			author:    "1",
			reviewers: []string{"5"},
		},
		{
			desc:      "docs-only-self-review",
			files:     []string{"docs/docs.md"},
			author:    "5",
			reviewers: []string{"1", "2"},
		},
		{
			desc: "docs-and-code",
			files: []string{
				"docs/docs.md",
				"file.go",
			},
			author:    "1",
			reviewers: []string{"5", "2", "3"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			b, err := New(&Config{
				env: &env.Event{
					Author:       test.author,
					Organization: "",
					Repository:   "",
					Number:       0,
				},
				gh: &fakeGithub{
					files: test.files,
				},
				r: r,
			})
			require.NoError(t, err)

			// Run and check assignment was correct.
			reviewers, err := b.getReviewers(context.Background())
			require.NoError(t, err)
			require.ElementsMatch(t, reviewers, test.reviewers)
		})
	}
}

type fakeGithub struct {
	files   []string
	reviews []github.Review
}

func (f *fakeGithub) RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers []string) error {
	return nil
}

func (f *fakeGithub) ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error) {
	return f.files, nil
}

func (f *fakeGithub) ListReviews(ctx context.Context, organization string, repository string, number int) ([]github.Review, error) {
	return f.reviews, nil
}
