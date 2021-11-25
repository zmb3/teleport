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

	"github.com/stretchr/testify/require"
)

// TestGetCodeReviewers tests code review assignments.
func TestGetCodeReviewers(t *testing.T) {
	r, err := NewAssignments(&Config{
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
			"7",
		},
	})
	require.NoError(t, err)

	tests := []struct {
		desc   string
		author string
		setA   []string
		setB   []string
	}{
		{
			desc:   "skip-self-assign",
			author: "1",
			setA:   []string{"2", "3"},
			setB:   []string{"4", "5"},
		},
		{
			desc:   "skip-omitted-user",
			author: "3",
			setA:   []string{"1", "2"},
			setB:   []string{"4", "5"},
		},
		{
			desc:   "external-gets-default",
			author: "10",
			setA:   []string{"1", "2", "7"},
			setB:   []string{"1", "2", "7"},
		},
		{
			desc:   "internal-gets-defaults",
			author: "7",
			setA:   []string{"1", "2"},
			setB:   []string{"1", "2"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			setA, setB := r.GetCodeReviewers(test.author)
			require.ElementsMatch(t, setA, test.setA)
			require.ElementsMatch(t, setB, test.setB)
		})
	}
}

// TestGetDocsReviewers tests docs assignments.
func TestGetDocsReviewers(t *testing.T) {
	r, err := NewAssignments(&Config{
		// Code.
		CodeReviewers:     map[string]Reviewer{},
		CodeReviewersOmit: map[string]bool{},
		// Docs.
		DocsReviewers: map[string]Reviewer{
			"1": Reviewer{Group: "Core", Set: "A"},
		},
		DocsReviewersOmit: map[string]bool{
			"2": true,
		},
		// Defaults.
		DefaultReviewers: []string{
			"3",
			"4",
		},
	})
	require.NoError(t, err)

	tests := []struct {
		desc      string
		author    string
		reviewers []string
	}{
		{
			desc:      "self-review-gets-defaults",
			author:    "1",
			reviewers: []string{"3", "4"},
		},
		{
			desc:      "normal-assign",
			author:    "5",
			reviewers: []string{"1"},
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			reviewers := r.GetDocsReviewers(test.author)
			require.ElementsMatch(t, reviewers, test.reviewers)
		})
	}
}
