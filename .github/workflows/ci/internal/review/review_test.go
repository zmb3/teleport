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

import (
	"testing"

	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"
	"github.com/stretchr/testify/require"
)

// TestGetCodeReviewers checks internal code review assignments.
func TestGetCodeReviewers(t *testing.T) {
	tests := []struct {
		desc        string
		assignments *Assignments
		author      string
		setA        []string
		setB        []string
	}{
		{
			desc: "skip-self-assign",
			assignments: &Assignments{
				c: &Config{
					// Code.
					CodeReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
						"3": Reviewer{Group: "Core", Set: "B"},
						"4": Reviewer{Group: "Core", Set: "B"},
					},
					CodeReviewersOmit: map[string]bool{},
					// Defaults.
					DefaultReviewers: []string{
						"1",
						"2",
					},
				},
			},
			author: "1",
			setA:   []string{"2"},
			setB:   []string{"3", "4"},
		},
		{
			desc: "skip-omitted-user",
			assignments: &Assignments{
				c: &Config{
					// Code.
					CodeReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
						"3": Reviewer{Group: "Core", Set: "B"},
						"4": Reviewer{Group: "Core", Set: "B"},
						"5": Reviewer{Group: "Core", Set: "B"},
					},
					CodeReviewersOmit: map[string]bool{
						"3": true,
					},
					// Defaults.
					DefaultReviewers: []string{
						"1",
						"2",
					},
				},
			},
			author: "5",
			setA:   []string{"1", "2"},
			setB:   []string{"4"},
		},
		{
			desc: "internal-gets-defaults",
			assignments: &Assignments{
				c: &Config{
					// Code.
					CodeReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
						"3": Reviewer{Group: "Core", Set: "B"},
						"4": Reviewer{Group: "Core", Set: "B"},
						"5": Reviewer{Group: "Internal"},
					},
					CodeReviewersOmit: map[string]bool{},
					// Defaults.
					DefaultReviewers: []string{
						"1",
						"2",
					},
				},
			},
			author: "5",
			setA:   []string{"1", "2"},
			setB:   []string{"1", "2"},
		},
		{
			desc: "normal",
			assignments: &Assignments{
				c: &Config{
					// Code.
					CodeReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
						"3": Reviewer{Group: "Core", Set: "A"},
						"4": Reviewer{Group: "Core", Set: "B"},
						"5": Reviewer{Group: "Core", Set: "B"},
						"6": Reviewer{Group: "Core", Set: "B"},
						"7": Reviewer{Group: "Internal", Set: "A"},
					},
					CodeReviewersOmit: map[string]bool{
						"6": true,
					},
					// Docs.
					DocsReviewers:     map[string]Reviewer{},
					DocsReviewersOmit: map[string]bool{},
					// Defaults.
					DefaultReviewers: []string{
						"1",
						"2",
					},
				},
			},
			author: "4",
			setA:   []string{"1", "2", "3"},
			setB:   []string{"5"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			setA, setB := test.assignments.getCodeReviewerSets(test.author)
			require.ElementsMatch(t, setA, test.setA)
			require.ElementsMatch(t, setB, test.setB)
		})
	}
}

// TestGetDocsReviewers checks internal docs review assignments.
func TestGetDocsReviewers(t *testing.T) {
	tests := []struct {
		desc        string
		assignments *Assignments
		author      string
		reviewers   []string
	}{
		{
			desc: "skip-self-assign",
			assignments: &Assignments{
				c: &Config{
					// Docs.
					DocsReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
					},
					DocsReviewersOmit: map[string]bool{},
					// Defaults.
					DefaultReviewers: []string{
						"3",
						"4",
					},
				},
			},
			author:    "1",
			reviewers: []string{"2"},
		},
		{
			desc: "skip-self-assign-with-omit",
			assignments: &Assignments{
				c: &Config{
					// Docs.
					DocsReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
					},
					DocsReviewersOmit: map[string]bool{
						"2": true,
					},
					// Defaults.
					DefaultReviewers: []string{
						"3",
						"4",
					},
				},
			},
			author:    "1",
			reviewers: []string{"3", "4"},
		},
		{
			desc: "normal",
			assignments: &Assignments{
				c: &Config{
					// Docs.
					DocsReviewers: map[string]Reviewer{
						"1": Reviewer{Group: "Core", Set: "A"},
						"2": Reviewer{Group: "Core", Set: "A"},
					},
					DocsReviewersOmit: map[string]bool{},
					// Defaults.
					DefaultReviewers: []string{
						"3",
						"4",
					},
				},
			},
			author:    "3",
			reviewers: []string{"1", "2"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			reviewers := test.assignments.getDocsReviewers(test.author)
			require.ElementsMatch(t, reviewers, test.reviewers)
		})
	}
}

// TestCheckExternal checks external reviews.
func TestCheckExternal(t *testing.T) {
	r := &Assignments{
		c: &Config{
			// Code.
			CodeReviewers: map[string]Reviewer{
				"1": Reviewer{Group: "Core", Set: "A"},
				"2": Reviewer{Group: "Core", Set: "A"},
				"3": Reviewer{Group: "Core", Set: "A"},
				"4": Reviewer{Group: "Core", Set: "B"},
				"5": Reviewer{Group: "Core", Set: "B"},
				"6": Reviewer{Group: "Core", Set: "B"},
			},
			CodeReviewersOmit: map[string]bool{},
			// Default.
			DefaultReviewers: []string{
				"1",
				"2",
			},
		},
	}
	tests := []struct {
		desc    string
		author  string
		reviews map[string]*github.Review
		result  bool
	}{
		{
			desc:    "no-reviews-fail",
			author:  "5",
			reviews: map[string]*github.Review{},
			result:  false,
		},
		{
			desc:   "two-non-admin-reviews-fail",
			author: "5",
			reviews: map[string]*github.Review{
				"3": &github.Review{
					Author: "3",
					State:  "APPROVED",
				},
				"4": &github.Review{
					Author: "4",
					State:  "APPROVED",
				},
			},
			result: false,
		},
		{
			desc:   "one-admin-reviews-fail",
			author: "5",
			reviews: map[string]*github.Review{
				"1": &github.Review{
					Author: "1",
					State:  "APPROVED",
				},
				"4": &github.Review{
					Author: "4",
					State:  "APPROVED",
				},
			},
			result: false,
		},
		{
			desc:   "two-admin-reviews-one-denied-success",
			author: "5",
			reviews: map[string]*github.Review{
				"1": &github.Review{
					Author: "1",
					State:  "CHANGES_REQUESTED",
				},
				"2": &github.Review{
					Author: "2",
					State:  "APPROVED",
				},
			},
			result: false,
		},
		{
			desc:   "two-admin-reviews-success",
			author: "5",
			reviews: map[string]*github.Review{
				"1": &github.Review{
					Author: "1",
					State:  "APPROVED",
				},
				"2": &github.Review{
					Author: "2",
					State:  "APPROVED",
				},
			},
			result: true,
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := r.CheckExternal(test.author, test.reviews)
			if test.result {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestCheckInternal checks internal reviews.
func TestCheckInternal(t *testing.T) {
	r := &Assignments{
		c: &Config{
			// Code.
			CodeReviewers: map[string]Reviewer{
				"1": Reviewer{Group: "Core", Set: "A"},
				"2": Reviewer{Group: "Core", Set: "A"},
				"3": Reviewer{Group: "Core", Set: "A"},
				"4": Reviewer{Group: "Core", Set: "B"},
				"5": Reviewer{Group: "Core", Set: "B"},
				"6": Reviewer{Group: "Core", Set: "B"},
			},
			// Docs.
			DocsReviewers: map[string]Reviewer{
				"7": Reviewer{Group: "Core", Set: "A"},
			},
			DocsReviewersOmit: map[string]bool{},
			CodeReviewersOmit: map[string]bool{},
			// Default.
			DefaultReviewers: []string{
				"1",
				"2",
			},
		},
	}
	tests := []struct {
		desc    string
		author  string
		reviews map[string]*github.Review
		docs    bool
		code    bool
		result  bool
	}{
		{
			desc:    "no-reviews-fail",
			author:  "4",
			reviews: map[string]*github.Review{},
			result:  false,
		},
		{
			desc:    "docs-only-no-reviews-fail",
			author:  "4",
			reviews: map[string]*github.Review{},
			docs:    true,
			code:    false,
			result:  false,
		},
		{
			desc:   "docs-only-non-docs-approval-fail",
			author: "4",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
			},
			docs:   true,
			code:   false,
			result: false,
		},
		{
			desc:   "docs-only-docs-approval-success",
			author: "4",
			reviews: map[string]*github.Review{
				"7": &github.Review{Author: "7", State: "APPROVED"},
			},
			docs:   true,
			code:   false,
			result: true,
		},
		{
			desc:    "code-only-no-reviews-fail",
			author:  "4",
			reviews: map[string]*github.Review{},
			docs:    false,
			code:    true,
			result:  false,
		},
		{
			desc:   "code-only-one-approval-fail",
			author: "4",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
			},
			docs:   false,
			code:   true,
			result: false,
		},
		{
			desc:   "code-only-two-approval-setb-fail",
			author: "4",
			reviews: map[string]*github.Review{
				"5": &github.Review{Author: "5", State: "APPROVED"},
				"6": &github.Review{Author: "6", State: "APPROVED"},
			},
			docs:   false,
			code:   true,
			result: false,
		},
		{
			desc:   "code-only-one-changes-fail",
			author: "4",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
				"4": &github.Review{Author: "4", State: "CHANGES_REQUESTED"},
			},
			docs:   false,
			code:   true,
			result: false,
		},
		{
			desc:   "code-only-two-approvals-success",
			author: "6",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
				"4": &github.Review{Author: "4", State: "APPROVED"},
			},
			docs:   false,
			code:   true,
			result: true,
		},
		{
			desc:   "docs-and-code-only-docs-approval-fail",
			author: "6",
			reviews: map[string]*github.Review{
				"7": &github.Review{Author: "7", State: "APPROVED"},
			},
			docs:   true,
			code:   true,
			result: false,
		},
		{
			desc:   "docs-and-code-only-code-approval-fail",
			author: "6",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
				"4": &github.Review{Author: "4", State: "APPROVED"},
			},
			docs:   true,
			code:   true,
			result: false,
		},
		{
			desc:   "docs-and-code-docs-and-code-approval-success",
			author: "6",
			reviews: map[string]*github.Review{
				"3": &github.Review{Author: "3", State: "APPROVED"},
				"4": &github.Review{Author: "4", State: "APPROVED"},
				"7": &github.Review{Author: "7", State: "APPROVED"},
			},
			docs:   true,
			code:   true,
			result: true,
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := r.CheckInternal(test.author, test.reviews, test.docs, test.code)
			if test.result {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
