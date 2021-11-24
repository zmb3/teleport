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
	CodeReviewers        map[string]reviewer
	CodeReviewersOmit    map[string]bool
	DefaultCodeReviewers []string

	DefaultDocsReviewers []string
}

func (c *Config) CheckAndSetDefaults() error {
	if c.CodeReviewers == nil {
		return trace.BadParameter("code reviewers missing")
	}
	if c.CodeReviewersOmit == nil {
		return trace.BadParameter("code reviewers omit missing")
	}
	if c.DefaultCodeReviewers == nil {
		return trace.BadParameter("default code reviewers missing")
	}
	if c.DefaultDocsReviewers == nil {
		return trace.BadParameter("default docs reviewers missing")
	}
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

// GetDocsReviewers returns a list of docs reviewers.
func (r *Assignments) GetDocsReviewers() []string {
	return r.c.DefaultDocsReviewers
}

// GetCodeReviewers returns a list of code reviewers for this author.
func (r *Assignments) GetCodeReviewers(name string) []string {
	// Get code reviewer sets for this PR author.
	setA, setB := r.GetCodeReviewerSets(name)

	// Randomly select a reviewer from each set and return a pair of reviewers.
	return []string{
		setA[rand.Intn(len(setA))],
		setB[rand.Intn(len(setB))],
	}
}

func (r *Assignments) GetCodeReviewerSets(name string) ([]string, []string) {
	// External contributors get assigned from the default reviewer set. Default
	// reviewers will triage and re-assign.
	v, ok := r.c.CodeReviewers[name]
	if !ok {
		return r.c.DefaultCodeReviewers, r.c.DefaultCodeReviewers
	}

	switch v.group {
	// Terminal team does own reviews.
	case "Terminal":
		return r.getReviewerSets(name, v.group)
	// Core and Database Access does internal team reviews most of the time,
	// however 30% of the time reviews are cross-team.
	case "Database Access", "Core":
		if rand.Intn(10) > 7 {
			return r.getReviewerSets(name, "Core", "Database Access")
		}
		return r.getReviewerSets(name, v.group)
	// Non-Core, but internal Teleport authors, get assigned default reviews who
	// will re-assign to appropriate reviewers.
	default:
		return r.c.DefaultCodeReviewers, r.c.DefaultCodeReviewers
	}
}

func (r *Assignments) getReviewerSets(name string, selectGroup ...string) ([]string, []string) {
	var setA []string
	var setB []string

	for k, v := range r.c.CodeReviewers {
		if skipGroup(v.group, selectGroup) {
			continue
		}
		if _, ok := r.c.CodeReviewersOmit[k]; ok {
			continue
		}
		// Can not review own PR.
		if k == name {
			continue
		}

		if v.set == "A" {
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
