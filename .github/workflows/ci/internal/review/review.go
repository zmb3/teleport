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
	"math/rand"
	"time"

	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"
	"github.com/gravitational/trace"
)

type Config struct {
	rand *rand.Rand

	CodeReviewers     map[string]Reviewer
	CodeReviewersOmit map[string]bool

	DocsReviewers     map[string]Reviewer
	DocsReviewersOmit map[string]bool

	DefaultReviewers []string
}

func (c *Config) CheckAndSetDefaults() error {
	if c.rand == nil {
		c.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	if c.CodeReviewers == nil {
		return trace.BadParameter("code reviewers missing")
	}
	if c.CodeReviewersOmit == nil {
		return trace.BadParameter("code reviewers omit missing")
	}

	if c.DocsReviewers == nil {
		return trace.BadParameter("docs reviewers missing")
	}
	if c.DocsReviewersOmit == nil {
		return trace.BadParameter("docs reviewers omit missing")
	}

	if c.DefaultReviewers == nil {
		return trace.BadParameter("default reviewers missing")
	}

	return nil
}

type Assignments struct {
	c *Config
}

func New(c *Config) (*Assignments, error) {
	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Assignments{
		c: c,
	}, nil
}

func (r *Assignments) IsInternal(author string) bool {
	_, ok := r.c.CodeReviewers[author]
	return ok
}

// Get will return a list of code reviewers a given author.
func (r *Assignments) Get(author string, docs bool, code bool) []string {
	var reviewers []string

	switch {
	case docs && code:
		reviewers = append(reviewers, r.getDocsReviewers(author)...)
		reviewers = append(reviewers, r.getCodeReviewers(author)...)
	case !docs && code:
		reviewers = append(reviewers, r.getCodeReviewers(author)...)
	case docs && !code:
		reviewers = append(reviewers, r.getDocsReviewers(author)...)
	// Strange state, an empty commit? Return default code reviewers.
	case !docs && !code:
		reviewers = append(reviewers, r.getCodeReviewers(author)...)
	}

	return reviewers
}

func (r *Assignments) getDocsReviewers(author string) []string {
	setA, setB := getReviewerSets(author, "Core", r.c.DocsReviewers, r.c.DocsReviewersOmit)
	reviewers := append(setA, setB...)

	// If no docs reviewers were assigned, assign default code reviews.
	if len(reviewers) == 0 {
		return r.getDefaultReviewers(author)
	}
	return reviewers
}

func (r *Assignments) getCodeReviewers(author string) []string {
	setA, setB := r.getCodeReviewerSets(author)

	return []string{
		setA[r.c.rand.Intn(len(setA))],
		setB[r.c.rand.Intn(len(setB))],
	}
}

func (r *Assignments) getCodeReviewerSets(author string) ([]string, []string) {
	// Internal non-Core contributors get assigned from the default reviewer set.
	// Default reviewers will triage and re-assign.
	v, ok := r.c.CodeReviewers[author]
	if !ok || v.Group == "Internal" {
		defaultReviewers := r.getDefaultReviewers(author)
		return defaultReviewers, defaultReviewers
	}

	return getReviewerSets(author, v.Group, r.c.CodeReviewers, r.c.CodeReviewersOmit)
}

func getReviewerSets(author string, group string, reviewers map[string]Reviewer, reviewersOmit map[string]bool) ([]string, []string) {
	var setA []string
	var setB []string

	for k, v := range reviewers {
		// Only assign within a group.
		if v.Group != group {
			continue
		}
		// Skip over reviewers that are marked as omit.
		if _, ok := reviewersOmit[k]; ok {
			continue
		}
		// Skip author, can't assign/review own PR.
		if k == author {
			continue
		}

		if v.Set == "A" {
			setA = append(setA, k)
		} else {
			setB = append(setB, k)
		}
	}

	return setA, setB
}

// CheckExternal requires two admins to approve.
func (r *Assignments) CheckExternal(author string, reviews map[string]*github.Review) error {
	reviewers := r.getDefaultReviewers(author)

	if checkN(reviewers, reviews) > 1 {
		return nil
	}
	return trace.BadParameter("at least two approvals required from %v", reviewers)
}

// CheckInternal will verify if required reviewers have approved.
func (r *Assignments) CheckInternal(author string, reviews map[string]*github.Review, docs bool, code bool) error {
	// Skip checks if admins have approved.
	if check(r.getDefaultReviewers(author), reviews) {
		return nil
	}

	switch {
	case docs && code:
		if err := r.checkDocsReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
		if err := r.checkCodeReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
	case !docs && code:
		if err := r.checkCodeReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
	case docs && !code:
		if err := r.checkDocsReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
	// Strange state, an empty commit? Check admins.
	case !docs && !code:
		if checkN(r.getDefaultReviewers(author), reviews) < 2 {
			return trace.BadParameter("requires two admins approvals")
		}
	}

	return nil
}

func (r *Assignments) checkDocsReviews(author string, reviews map[string]*github.Review) error {
	reviewers := r.getDocsReviewers(author)

	if check(reviewers, reviews) {
		return nil
	}

	return trace.BadParameter("requires at least one approval from %v", reviewers)
}

func (r *Assignments) checkCodeReviews(author string, reviews map[string]*github.Review) error {
	setA, setB := r.getCodeReviewerSets(author)

	if check(setA, reviews) && check(setB, reviews) {
		return nil
	}

	return trace.BadParameter("at least one approval required from each set %v %v", setA, setB)
}

func (r *Assignments) getDefaultReviewers(author string) []string {
	var reviewers []string
	for _, v := range r.c.DefaultReviewers {
		if v == author {
			continue
		}
		reviewers = append(reviewers, v)
	}
	return reviewers
}

func check(reviewers []string, reviews map[string]*github.Review) bool {
	return checkN(reviewers, reviews) > 0
}

func checkN(reviewers []string, reviews map[string]*github.Review) int {
	var n int
	for _, review := range reviews {
		for _, reviewer := range reviewers {
			if review.State == "APPROVED" && review.Author == reviewer {
				n++
			}
		}
	}
	return n
}
