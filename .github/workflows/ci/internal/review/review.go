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
	//if c.CodeReviewers == nil {
	//	return trace.BadParameter("code reviewers missing")
	//}
	//if c.CodeReviewersOmit == nil {
	//	return trace.BadParameter("code reviewers omit missing")
	//}
	//if c.DefaultCodeReviewers == nil {
	//	return trace.BadParameter("default code reviewers missing")
	//}
	//if c.DefaultDocsReviewers == nil {
	//	return trace.BadParameter("default docs reviewers missing")
	//}
	return nil
}

type Assignments struct {
	c *Config
}

func NewAssignments(c *Config) (*Assignments, error) {
	rand.Seed(time.Now().UnixNano())

	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Assignments{
		c: c,
	}, nil
}

func (r *Assignments) GetDefaultReviewers() []string {
	return r.c.DefaultReviewers
}

// GetDocsReviewers returns a list of docs reviewers.
func (r *Assignments) GetDocsReviewers(author string) []string {
	var reviewers []string
	for k, _ := range r.c.DocsReviewers {
		// Skip author, can't review own PR.
		if k == author {
			continue
		}
		reviewers = append(reviewers, k)
	}

	// If no docs reviewers were assigned, assign default reviews.
	if len(reviewers) == 0 {
		return r.c.DefaultReviewers
	}
	return reviewers
}

// GetCodeReviewers returns a list of code reviewers for this author.
func (r *Assignments) GetCodeReviewers(author string) []string {
	// Get code reviewer sets for this PR author.
	setA, setB := r.GetCodeReviewerSets(author, 30)

	// Randomly select a reviewer from each set and return a pair of reviewers.
	return []string{
		setA[rand.Intn(len(setA))],
		setB[rand.Intn(len(setB))],
	}
}

func (r *Assignments) GetCodeReviewerSets(name string, percentage int) ([]string, []string) {
	// External contributors get assigned from the default reviewer set. Default
	// reviewers will triage and re-assign.
	v, ok := r.c.CodeReviewers[name]
	if !ok {
		return r.c.DefaultReviewers, r.c.DefaultReviewers
	}

	switch v.Group {
	// Terminal team does own reviews.
	case "Terminal":
		return r.getReviewerSets(name, v.Group)
	// Core and Database Access does internal team reviews most of the time,
	// however 30% of the time reviews are cross-team.
	case "Database Access", "Core":
		if rand.Intn(100) < percentage {
			return r.getReviewerSets(name, "Core", "Database Access")
		}
		return r.getReviewerSets(name, v.Group)
	// Non-Core, but internal Teleport authors, get assigned default reviews who
	// will re-assign to appropriate reviewers.
	default:
		return r.c.DefaultReviewers, r.c.DefaultReviewers
	}
}

func (r *Assignments) getReviewerSets(name string, selectGroup ...string) ([]string, []string) {
	var setA []string
	var setB []string

	for k, v := range r.c.CodeReviewers {
		if skipGroup(v.Group, selectGroup) {
			continue
		}
		if _, ok := r.c.CodeReviewersOmit[k]; ok {
			continue
		}
		// Skip author, can't review own PR.
		if k == name {
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

func skipGroup(group string, selectGroup []string) bool {
	for _, s := range selectGroup {
		if group == s {
			return false
		}
	}
	return true
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
