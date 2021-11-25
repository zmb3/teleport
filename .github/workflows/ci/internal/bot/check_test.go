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

// TODO(russjones): Test cross-team approvals.

func TestCheckInternal(t *testing.T) {
	r, err := review.NewAssignments(&review.Config{
		// Code reviewers.
		CodeReviewers: map[string]review.Reviewer{
			"1": review.Reviewer{Group: "Core", Set: "A"},
			"2": review.Reviewer{Group: "Core", Set: "A"},
			"3": review.Reviewer{Group: "Core", Set: "A"},
			"4": review.Reviewer{Group: "Core", Set: "B"},
			"5": review.Reviewer{Group: "Core", Set: "B"},
			"6": review.Reviewer{Group: "Core", Set: "B"},
		},
		// Docs reviewers.
		DocsReviewers: map[string]review.Reviewer{
			"6": review.Reviewer{Group: "Core", Set: "A"},
		},
		DocsReviewersOmit: map[string]bool{},
		// Default reviewers.
		DefaultReviewers: []string{"1", "2"},
	})
	require.NoError(t, err)

	tests := []struct {
		desc    string
		reviews []github.Review
		author  string
		err     bool
	}{
		{
			desc:    "no-reviews",
			author:  "1",
			reviews: []github.Review{},
			err:     true,
		},
		{
			desc:   "no-approvals",
			author: "1",
			reviews: []github.Review{
				github.Review{Author: "1", State: ""},
				github.Review{Author: "2", State: ""},
			},
			err: true,
		},
		{
			desc:   "one-approval-non-admin",
			author: "1",
			reviews: []github.Review{
				github.Review{Author: "3", State: "APPROVED"},
				github.Review{Author: "4", State: ""},
			},
			err: true,
		},
		{
			desc:   "one-approval-admin",
			author: "1",
			reviews: []github.Review{
				github.Review{Author: "2", State: "APPROVED"},
				github.Review{Author: "3", State: ""},
				github.Review{Author: "4", State: ""},
			},
			err: false,
		},
		{
			desc:   "two-approvals",
			author: "1",
			reviews: []github.Review{
				github.Review{Author: "2", State: "APPROVED"},
				github.Review{Author: "3", State: "APPROVED"},
			},
			err: false,
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
					files:   []string{"code.go"},
					reviews: test.reviews,
				},
				r: r,
			})
			require.NoError(t, err)

			err = b.checkInternal(context.Background())
			if test.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

//func TestApproved(t *testing.T) {
//	bot := &Bot{Environment: &environment.PullRequestEnvironment{}}
//	pull := &environment.Metadata{Author: "test"}
//	tests := []struct {
//		botInstance    *Bot
//		pr             *environment.Metadata
//		required       []string
//		currentReviews map[string]review
//		desc           string
//		checkErr       require.ErrorAssertionFunc
//	}{
//		{
//			botInstance: bot,
//			pr:          pull,
//			required:    []string{"foo", "bar", "baz"},
//			currentReviews: map[string]review{
//				"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//				"bar": {name: "bar", status: "Commented", commitID: "fe324c", id: 2},
//				"baz": {name: "baz", status: "APPROVED", commitID: "ba0d35", id: 3},
//			},
//			desc:     "PR does not have all required approvals",
//			checkErr: require.Error,
//		},
//		{
//			botInstance: bot,
//
//			pr:       pull,
//			required: []string{"foo", "bar", "baz"},
//			currentReviews: map[string]review{
//				"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//				"bar": {name: "bar", status: "APPROVED", commitID: "12ga34", id: 2},
//				"baz": {name: "baz", status: "APPROVED", commitID: "12ga34", id: 3},
//			},
//			desc:     "PR has required approvals, commit shas match",
//			checkErr: require.NoError,
//		},
//		{
//			botInstance: bot,
//			pr:          pull,
//			required:    []string{"foo", "bar"},
//			currentReviews: map[string]review{
//				"foo": {name: "foo", status: "APPROVED", commitID: "fe324c", id: 1},
//			},
//			desc:     "PR does not have all required approvals",
//			checkErr: require.Error,
//		},
//	}
//
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			err := hasRequiredApprovals(test.currentReviews, test.required)
//			test.checkErr(t, err)
//		})
//	}
//}
//
//func TestContainsApprovalReview(t *testing.T) {
//	reviews := map[string]review{
//		"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//		"bar": {name: "bar", status: "Commented", commitID: "fe324c", id: 2},
//		"baz": {name: "baz", status: "APPROVED", commitID: "ba0d35", id: 1},
//	}
//	// Has a review but no approval
//	ok := hasApproved("bar", reviews)
//	require.Equal(t, false, ok)
//
//	// Does not have revire from reviewer
//	ok = hasApproved("car", reviews)
//	require.Equal(t, false, ok)
//
//	// Has review and is approved
//	ok = hasApproved("foo", reviews)
//	require.Equal(t, true, ok)
//}
//
//func TestSplitReviews(t *testing.T) {
//	reviews := map[string]review{
//		"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//		"bar": {name: "bar", status: "Commented", commitID: "fe324c", id: 2},
//		"baz": {name: "baz", status: "APPROVED", commitID: "ba0d35", id: 3},
//	}
//	valid, obs := splitReviews("fe324c", reviews)
//	expectedValid := map[string]review{
//		"bar": {name: "bar", status: "Commented", commitID: "fe324c", id: 2},
//	}
//	expectedObsolete := map[string]review{
//		"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//		"baz": {name: "baz", status: "APPROVED", commitID: "ba0d35", id: 3},
//	}
//	require.Equal(t, expectedValid, valid)
//	require.Equal(t, expectedObsolete, obs)
//}
//
//func TestHasRequiredApprovals(t *testing.T) {
//	reviews := map[string]review{
//		"foo": {name: "foo", status: "APPROVED", commitID: "12ga34", id: 1},
//		"bar": {name: "bar", status: "APPROVED", commitID: "ba0d35", id: 3},
//	}
//	required := []string{"foo", "bar"}
//	err := hasRequiredApprovals(reviews, required)
//	require.NoError(t, err)
//
//	reviews = map[string]review{
//		"foo": {name: "foo", status: "APPROVED", commitID: "fe324c", id: 1},
//		"bar": {name: "bar", status: "Commented", commitID: "fe324c", id: 2},
//		"baz": {name: "baz", status: "APPROVED", commitID: "fe324c", id: 3},
//	}
//	required = []string{"foo", "reviewer"}
//	err = hasRequiredApprovals(reviews, required)
//	require.Error(t, err)
//
//}
//
//func TestGetStaleReviews(t *testing.T) {
//	metadata := &environment.Metadata{Author: "quinqu",
//		RepoName:  "test-name",
//		RepoOwner: "test-owner",
//		HeadSHA:   "ecabd9d",
//	}
//	env := &environment.PullRequestEnvironment{Metadata: metadata}
//	bot := Bot{Environment: env}
//	tests := []struct {
//		mockC    mockCommitComparer
//		reviews  map[string]review
//		expected []string
//		desc     string
//	}{
//		{
//			mockC: mockCommitComparer{},
//			reviews: map[string]review{
//				"foo": {commitID: "ReviewHasFileChangeFromHead", name: "foo"},
//				"bar": {commitID: "ReviewHasFileChangeFromHead", name: "bar"},
//			},
//			expected: []string{"foo", "bar"},
//			desc:     "All pull request reviews are stale.",
//		},
//		{
//			mockC: mockCommitComparer{},
//			reviews: map[string]review{
//				"foo": {commitID: "ecabd94", name: "foo"},
//				"bar": {commitID: "abcde67", name: "bar"},
//			},
//			expected: []string{},
//			desc:     "Pull request has no stale reviews.",
//		},
//		{
//			mockC: mockCommitComparer{},
//			reviews: map[string]review{
//				"foo":  {commitID: "ReviewHasFileChangeFromHead", name: "foo"},
//				"bar":  {commitID: "ReviewHasFileChangeFromHead", name: "bar"},
//				"fizz": {commitID: "ecabd9d", name: "fizz"},
//			},
//			expected: []string{"foo", "bar"},
//			desc:     "Pull request has two stale reviews.",
//		},
//	}
//
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			bot.compareCommits = &test.mockC
//			staleReviews, _ := bot.getStaleReviews(context.TODO(), test.reviews)
//			for _, name := range test.expected {
//				_, ok := staleReviews[name]
//				require.Equal(t, true, ok)
//			}
//			require.Equal(t, len(test.expected), len(staleReviews))
//		})
//	}
//
//}
//
//type mockCommitComparer struct {
//}
//
//func (m *mockCommitComparer) CompareCommits(ctx context.Context, repoOwner, repoName, base, head string) (*github.CommitsComparison, *github.Response, error) {
//	// FOR TESTS ONLY: Using the string "ReviewHasFileChangeFromHead" as an indicator that this test method should
//	// return a non-empty CommitFile list in the CommitComparison.
//	if base == "ReviewHasFileChangeFromHead" {
//		return &github.CommitsComparison{Files: []*github.CommitFile{{}, {}}}, nil, nil
//	}
//	return &github.CommitsComparison{Files: []*github.CommitFile{}}, nil, nil
//}
