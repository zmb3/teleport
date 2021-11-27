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
