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
	"github.com/gravitational/trace"
)

type Config struct {
	CodeReviewers     map[string]Reviewer
	CodeReviewersOmit map[string]bool

	DocsReviewers     map[string]Reviewer
	DocsReviewersOmit map[string]bool

	DefaultReviewers []string
}

func (c *Config) CheckAndSetDefaults() error {
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

func NewAssignments(c *Config) (*Assignments, error) {
	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Assignments{
		c: c,
	}, nil
}

func (r *Assignments) GetCodeReviewers(author string) ([]string, []string) {
	defaultReviewers := r.GetDefaultReviewers(author)

	// External contributors get assigned from the default reviewer set. Default
	// reviewers will triage and re-assign.
	v, ok := r.c.CodeReviewers[author]
	if !ok {
		return defaultReviewers, defaultReviewers
	}

	switch v.Group {
	case "Terminal", "Core":
		return getReviewerSets(author, v.Group, r.c.CodeReviewers, r.c.CodeReviewersOmit)
	// Non-Core, but internal Teleport authors, get assigned default reviews who
	// will re-assign to appropriate reviewers.
	default:
		return defaultReviewers, defaultReviewers
	}
}

func (r *Assignments) GetDocsReviewers(author string) []string {
	setA, setB := getReviewerSets(author, "Core", r.c.DocsReviewers, r.c.DocsReviewersOmit)
	reviewers := append(setA, setB...)

	// If no docs reviewers were assigned, assign default reviews.
	if len(reviewers) == 0 {
		return r.GetDefaultReviewers(author)
	}
	return reviewers
}

func (r *Assignments) GetDefaultReviewers(author string) []string {
	var reviewers []string
	for _, v := range r.c.DefaultReviewers {
		if v == author {
			continue
		}
		reviewers = append(reviewers, v)
	}
	return reviewers
}

func (r *Assignments) IsInternal(author string) bool {
	_, ok := r.c.CodeReviewers[author]
	return ok
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
		// Skip author, can't review own PR.
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
