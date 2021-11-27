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

package review

//// TestGetCodeReviewers tests code review assignments.
//func TestGetCodeReviewers(t *testing.T) {
//	r, err := NewAssignments(&Config{
//		// Code.
//		CodeReviewers: map[string]Reviewer{
//			"1": Reviewer{Group: "Core", Set: "A"},
//			"2": Reviewer{Group: "Core", Set: "A"},
//			"3": Reviewer{Group: "Core", Set: "A"},
//			"4": Reviewer{Group: "Core", Set: "B"},
//			"5": Reviewer{Group: "Core", Set: "B"},
//			"6": Reviewer{Group: "Core", Set: "B"},
//			"7": Reviewer{Group: "Internal", Set: "A"},
//		},
//		CodeReviewersOmit: map[string]bool{
//			"6": true,
//		},
//		// Docs.
//		DocsReviewers:     map[string]Reviewer{},
//		DocsReviewersOmit: map[string]bool{},
//		// Defaults.
//		DefaultReviewers: []string{
//			"1",
//			"2",
//			"7",
//		},
//	})
//	require.NoError(t, err)
//
//	tests := []struct {
//		desc   string
//		author string
//		setA   []string
//		setB   []string
//	}{
//		{
//			desc:   "skip-self-assign",
//			author: "1",
//			setA:   []string{"2", "3"},
//			setB:   []string{"4", "5"},
//		},
//		{
//			desc:   "skip-omitted-user",
//			author: "3",
//			setA:   []string{"1", "2"},
//			setB:   []string{"4", "5"},
//		},
//		{
//			desc:   "external-gets-default",
//			author: "10",
//			setA:   []string{"1", "2", "7"},
//			setB:   []string{"1", "2", "7"},
//		},
//		{
//			desc:   "internal-gets-defaults",
//			author: "7",
//			setA:   []string{"1", "2"},
//			setB:   []string{"1", "2"},
//		},
//	}
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			setA, setB := r.GetCodeReviewers(test.author)
//			require.ElementsMatch(t, setA, test.setA)
//			require.ElementsMatch(t, setB, test.setB)
//		})
//	}
//}

//// TestGetDocsReviewers tests docs assignments.
//func TestGetDocsReviewers(t *testing.T) {
//	r, err := NewAssignments(&Config{
//		// Code.
//		CodeReviewers:     map[string]Reviewer{},
//		CodeReviewersOmit: map[string]bool{},
//		// Docs.
//		DocsReviewers: map[string]Reviewer{
//			"1": Reviewer{Group: "Core", Set: "A"},
//		},
//		DocsReviewersOmit: map[string]bool{
//			"2": true,
//		},
//		// Defaults.
//		DefaultReviewers: []string{
//			"3",
//			"4",
//		},
//	})
//	require.NoError(t, err)
//
//	tests := []struct {
//		desc      string
//		author    string
//		reviewers []string
//	}{
//		{
//			desc:      "self-review-gets-defaults",
//			author:    "1",
//			reviewers: []string{"3", "4"},
//		},
//		{
//			desc:      "normal-assign",
//			author:    "5",
//			reviewers: []string{"1"},
//		},
//	}
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			reviewers := r.GetDocsReviewers(test.author)
//			require.ElementsMatch(t, reviewers, test.reviewers)
//		})
//	}
//}

//func TestGetReviewersForAuthors(t *testing.T) {
//	testReviewerMap := map[string][]string{
//		"*":   {"foo"},
//		"foo": {"bar", "baz"},
//	}
//	tests := []struct {
//		env      *PullRequestEnvironment
//		desc     string
//		user     string
//		expected []string
//	}{
//		{
//			env:      &PullRequestEnvironment{HasDocsChanges: true, HasCodeChanges: true, reviewers: testReviewerMap},
//			desc:     "pull request has both code and docs changes",
//			user:     "foo",
//			expected: []string{"klizhentas", "bar", "baz"},
//		},
//		{
//			env:      &PullRequestEnvironment{HasDocsChanges: false, HasCodeChanges: true, reviewers: testReviewerMap},
//			desc:     "pull request has only code changes",
//			user:     "foo",
//			expected: []string{"bar", "baz"},
//		},
//		{
//			env:      &PullRequestEnvironment{HasDocsChanges: true, HasCodeChanges: false, reviewers: testReviewerMap},
//			desc:     "pull request has only docs changes",
//			user:     "foo",
//			expected: []string{"klizhentas"},
//		},
//		{
//			env:      &PullRequestEnvironment{HasDocsChanges: false, HasCodeChanges: false, reviewers: testReviewerMap},
//			desc:     "pull request has no changes",
//			user:     "foo",
//			expected: []string{"bar", "baz"},
//		},
//	}
//
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			reviewerSlice := test.env.GetReviewersForAuthor(test.user)
//			require.Equal(t, test.expected, reviewerSlice)
//		})
//	}
//}
//
//func TestHasDocsChanges(t *testing.T) {
//	tests := []struct {
//		input    string
//		expected bool
//	}{
//		{input: "docs/some-file.txt", expected: true},
//		{input: "lib/auth/auth.go", expected: false},
//		{input: "lib/some-file.mdx", expected: true},
//		{input: "some/random/path.md", expected: true},
//		{input: "rfd/new-proposal.txt", expected: true},
//		{input: "doc/file.txt", expected: false},
//		{input: "", expected: false},
//		{input: "vendor/file.md", expected: false},
//	}
//
//	for _, test := range tests {
//		result := hasDocChanges(test.input)
//		require.Equal(t, test.expected, result)
//	}
//}
//
//func TestCheckAndSetDefaults(t *testing.T) {
//	testReviewerMapValid := map[string][]string{
//		"*":   {"foo"},
//		"foo": {"bar", "baz"},
//	}
//	testReviewerMapInvalid := map[string][]string{
//		"foo": {"bar", "baz"},
//	}
//
//	client := github.NewClient(nil)
//	ctx := context.Background()
//	os.Setenv(ci.GithubEventPath, "path/to/event.json")
//	tests := []struct {
//		cfg      Config
//		desc     string
//		expected Config
//		checkErr require.ErrorAssertionFunc
//	}{
//		{
//			cfg:      Config{Client: nil, Reviewers: testReviewerMapValid, Context: ctx, EventPath: "test/path"},
//			desc:     "Invalid config, Client is nil.",
//			expected: Config{Client: nil, Reviewers: testReviewerMapValid, Context: ctx, EventPath: "test/path"},
//			checkErr: require.Error,
//		},
//		{
//			cfg:      Config{Client: client, Reviewers: testReviewerMapInvalid, Context: ctx, EventPath: "test/path"},
//			desc:     "Invalid config, invalid Reviewer map, missing wildcard key.",
//			expected: Config{Client: client, Reviewers: testReviewerMapInvalid, Context: ctx, EventPath: "test/path"},
//			checkErr: require.Error,
//		},
//		{
//			cfg:      Config{Client: client, Context: ctx, EventPath: "test/path"},
//			desc:     "Invalid config, missing Reviewer map.",
//			expected: Config{Client: client, Context: ctx, EventPath: "test/path"},
//			checkErr: require.Error,
//		},
//		{
//			cfg:      Config{Client: client, Context: ctx, Reviewers: testReviewerMapValid},
//			desc:     "Valid config, EventPath not set.  ",
//			expected: Config{Client: client, Context: ctx, EventPath: "path/to/event.json", Reviewers: testReviewerMapValid},
//			checkErr: require.NoError,
//		},
//	}
//	for _, test := range tests {
//		t.Run(test.desc, func(t *testing.T) {
//			err := test.cfg.CheckAndSetDefaults()
//			test.checkErr(t, err)
//			require.Equal(t, test.expected, test.cfg)
//		})
//	}
//}

/*
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
*/

/*
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
*/
