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

func TestGetCodeReviewerSets(t *testing.T) {
	r, err := NewAssignments(&Config{
		CodeReviewers: map[string]reviewer{
			// Teleport Terminal.
			"t-foo": reviewer{group: "Terminal", set: "A"},
			"t-bar": reviewer{group: "Terminal", set: "A"},
			"t-baz": reviewer{group: "Terminal", set: "B"},
			"t-qux": reviewer{group: "Terminal", set: "B"},
			// Database Access.
			"d-foo": reviewer{group: "Database Access", set: "A"},
			"d-bar": reviewer{group: "Database Access", set: "A"},
			"d-baz": reviewer{group: "Database Access", set: "B"},
			"d-qux": reviewer{group: "Database Access", set: "B"},
			// Core.
			"c-foo": reviewer{group: "Core", set: "A"},
			"c-bar": reviewer{group: "Core", set: "A"},
			"c-baz": reviewer{group: "Core", set: "B"},
			"c-qux": reviewer{group: "Core", set: "B"},
		},
		CodeReviewersOmit:    map[string]bool{},
		DefaultCodeReviewers: []string{},
		DefaultDocsReviewers: []string{},
	})
	require.NoError(t, err)

	// Team only assignment.
	setA, setB := r.GetCodeReviewerSets("t-foo")
	require.ElementsMatch(t, setA, []string{"t-bar"})
	require.ElementsMatch(t, setB, []string{"t-baz", "t-qux"})

	// TODO(russjones): Fix to get deterministic behavior.
	// Cross-team assignment.
	setA, setB = r.GetCodeReviewerSets("d-foo")
	require.ElementsMatch(t, setA, []string{"d-bar", "c-bar", "c-foo"})
	require.ElementsMatch(t, setB, []string{"d-baz", "d-qux", "c-baz", "c-qux"})
}
