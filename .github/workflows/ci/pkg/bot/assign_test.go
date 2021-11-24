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

	"github.com/gravitational/teleport/.github/workflows/ci"
	"github.com/gravitational/teleport/.github/workflows/ci/pkg/environment"
	"github.com/gravitational/teleport/.github/workflows/ci/pkg/reviewer"

	"github.com/google/go-github/v37/github"
	"github.com/stretchr/testify/require"
)

// TestGetReviewers checks if a PR can be parsed and appropriately assigned a
// docs or code reviewer.
func TestGetReviewers(t *testing.T) {
	tests := []struct {
		desc      string
		files     []string
		author    string
		reviewers []string
	}{
		{
			desc:      "docs",
			files:     []string{"docs/docs.md"},
			author:    "foo",
			reviewers: []string{},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			r, err := reviewer.NewReviewers(&reviewer.Config{
				CodeReviewers: map[string]ci.Reviewer{
					"foo": ci.Reviewers{},
				},
			})
			// Build the bot itself.
			b, err := New(&Config{
				env: &environment.Environment{
					Author:       test.author,
					Organization: "foo",
					Repository:   "bar",
					Number:       0,
				},
				gh: &fakeGithub{
					files: test.files,
				},
				r: r,
			})
			require.NoError(t, err)

			// Run assignment and make sure assigned reviewers match expected reviewers.
			reviewers, err := b.getReviewers(context.Background())
			require.NoError(t, err)
			require.ElementsMatch(t, reviewers, test.reviewers)
		})
	}
}

type fakeGithub struct {
	files []string
}

func (f *fakeGithub) RequestReviewers(ctx context.Context, organization string, repository string, number int, reviewers github.ReviewersRequest) error {
	return nil
}

func (f *fakeGithub) ListFiles(ctx context.Context, organization string, repository string, number int) ([]string, error) {
	return f.files, nil
}
