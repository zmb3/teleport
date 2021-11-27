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

	"github.com/gravitational/trace"
)

// Check checks if required reviewers have approved the PR.
//
// checkInternal is called to check if a PR reviewed and approved by the
// required reviewers for internal contributors. Unlike approvals for
// external contributors, approvals from internal team members will not be
// invalidated when new changes are pushed to the PR.
func (b *Bot) Check(ctx context.Context) error {
	if b.c.reviewer.IsInternal(b.c.env.Author) {
		err := b.dismissStaleWorkflowRuns(ctx,
			b.c.env.Organization,
			b.c.env.Repository,
			b.c.env.UnsafeBranch)
		if err != nil {
			return trace.Wrap(err)
		}
	}
	return b.check(ctx)
}

func (b *Bot) check(ctx context.Context) error {
	reviews, err := b.c.gh.ListReviews(ctx,
		b.c.env.Organization,
		b.c.env.Repository,
		b.c.env.Number)
	if err != nil {
		return trace.Wrap(err)
	}

	docs, code, err := b.parseChanges(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := b.c.reviewer.Check(reviews, b.c.env.Author, docs, code); err != nil {
		return trace.Wrap(err)
	}

	return nil
}
