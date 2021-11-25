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

	"github.com/gravitational/teleport/.github/workflows/ci/internal/github"

	"github.com/gravitational/trace"
)

// Check checks if all the reviewers have approved the pull request in the current context.
func (b *Bot) Check(ctx context.Context) error {
	//pr := c.Environment.Metadata
	//if c.Environment.IsInternal(pr.Author) {
	//}
	//return c.checkExternal(ctx)
	return b.checkInternal(ctx)
}

// checkInternal is called to check if a PR reviewed and approved by the
// required reviewers for internal contributors. Unlike approvals for
// external contributors, approvals from internal team members will not be
// invalidated when new changes are pushed to the PR.
func (b *Bot) checkInternal(ctx context.Context) error {
	// Get list of all reviews that have been submitted from GitHub.
	reviews, err := b.c.gh.ListReviews(ctx,
		b.c.env.Organization,
		b.c.env.Repository,
		b.c.env.Number)
	if err != nil {
		return trace.Wrap(err)
	}

	// If an admin has has approved the PR, pass check right away.
	if err := b.checkAdmins(reviews); err == nil {
		return nil
	}

	// Go through regular approval process.
	if err := b.checkReviewers(ctx, b.c.env.Author, reviews); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func (b *Bot) checkAdmins(reviews []github.Review) error {
	if check(b.c.r.GetDefaultReviewers(), reviews) {
		return nil
	}
	return trace.BadParameter("missing admin approval")
}

func (b *Bot) checkReviewers(ctx context.Context, author string, reviews []github.Review) error {
	docs, code, err := b.parseChanges(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	if docs {
		if err := b.checkDocsReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
	}
	if code {
		if err = b.checkCodeReviews(author, reviews); err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

func (b *Bot) checkDocsReviews(author string, reviews []github.Review) error {
	reviewers := b.c.r.GetDocsReviewers(author)

	if check(reviewers, reviews) {
		return nil
	}

	return trace.BadParameter("requires at least one approval from %v", reviewers)
}

func (b *Bot) checkCodeReviews(author string, reviews []github.Review) error {
	setA, setB := b.c.r.GetCodeReviewers(author)

	if check(setA, reviews) && check(setB, reviews) {
		return nil
	}

	return trace.BadParameter("at least one approval required from each set %v %v", setA, setB)
}

func check(reviewers []string, reviews []github.Review) bool {
	for _, review := range reviews {
		for _, reviewer := range reviewers {
			if review.State == "APPROVED" && review.Author == reviewer {
				return true
			}
		}
	}
	return false
}

func (c *Bot) checkExternal(ctx context.Context) error {
	return nil
}

//// checkExternal is called to check if a PR reviewed and approved by the
//// required reviewers for external contributors. Approvals for external
//// contributors are dismissed when new changes are pushed to the PR. The only
//// case in which reviews are not dismissed is if they are from GitHub and
//// only update the PR.
//func (c *Bot) checkExternal(ctx context.Context) error {
//	pr := c.Environment.Metadata
//	mostRecentReviews, err := c.getMostRecentReviews(ctx)
//	if err != nil {
//		return trace.Wrap(err)
//	}
//	// External contributions require tighter scrutiny than team
//	// contributions. As such reviews from previous pushes must
//	// not carry over to when new changes are added. Github does
//	// not do this automatically, so we must dismiss the reviews
//	// manually if there is a file change.
//	staleReviews, err := c.getStaleReviews(ctx, mostRecentReviews)
//	if err != nil {
//		return trace.Wrap(err)
//	}
//	// Delete invalid reviews from map that will be
//	// checked for required approvals.
//	for _, staleReview := range staleReviews {
//		delete(mostRecentReviews, staleReview.name)
//	}
//	if len(staleReviews) != 0 {
//		err = c.invalidateApprovals(ctx, staleReviews)
//		if err != nil {
//			return trace.Wrap(err)
//		}
//	}
//
//	log.Printf("Checking if %v has approvals from the required reviewers %+v", pr.Author, c.Environment.GetReviewersForAuthor(pr.Author))
//	err = hasRequiredApprovals(mostRecentReviews, c.Environment.GetReviewersForAuthor(pr.Author))
//	if err != nil {
//		return trace.Wrap(err)
//	}
//	return nil
//}
//
//// getStaleReviews gets reviews that were submitted before a new non-empty commit was pushed.
//func (c *Bot) getStaleReviews(ctx context.Context, reviews map[string]review) (map[string]review, error) {
//	headSHA := c.Environment.Metadata.HeadSHA
//	staleReviews := map[string]review{}
//	for _, review := range reviews {
//		detectedFileChange, err := c.hasFileDiff(ctx, review.commitID, headSHA, c.compareCommits)
//		if err != nil {
//			return nil, trace.Wrap(err)
//		}
//		if detectedFileChange {
//			staleReviews[review.name] = review
//		}
//	}
//	return staleReviews, nil
//}
//
//// splitReviews splits a list of reviews into two lists: `valid` (those reviews that refer to
//// the current PR head revision) and `obsolete` (those that do not)
//func splitReviews(headSHA string, reviews map[string]review) (valid, obsolete map[string]review) {
//	valid = make(map[string]review)
//	obsolete = make(map[string]review)
//	for _, r := range reviews {
//		if r.commitID == headSHA {
//			valid[r.name] = r
//		} else {
//			obsolete[r.name] = r
//		}
//	}
//	return valid, obsolete
//}
//
//
//// validateReviewFields validates required fields exist and passes them
//// through a restrictive allow list (alphanumerics only). This is done to
//// mitigate impact of attacker controlled input (the PR).
//func validateReviewFields(review *github.PullRequestReview) error {
//	switch {
//	case review.ID == nil:
//		return trace.Errorf("review ID is nil. review: %+v", review)
//	case review.State == nil:
//		return trace.Errorf("review State is nil. review: %+v", review)
//	case review.CommitID == nil:
//		return trace.Errorf("review CommitID is nil. review: %+v", review)
//	case review.SubmittedAt == nil:
//		return trace.Errorf("review SubmittedAt is nil. review: %+v", review)
//	case review.User.Login == nil:
//		return trace.Errorf("reviewer User.Login is nil. review: %+v", review)
//	}
//	if err := validateField(*review.State); err != nil {
//		return trace.Errorf("review ID err: %v", err)
//	}
//	if err := validateField(*review.CommitID); err != nil {
//		return trace.Errorf("commit ID err: %v", err)
//	}
//	if err := validateField(*review.User.Login); err != nil {
//		return trace.Errorf("user login err: %v", err)
//	}
//	return nil
//}
//
//// mostRecent returns a list of the most recent review from each required reviewer.
//func mostRecent(currentReviews []review) map[string]review {
//	mostRecentReviews := make(map[string]review)
//	for _, rev := range currentReviews {
//		val, ok := mostRecentReviews[rev.name]
//		if !ok {
//			mostRecentReviews[rev.name] = rev
//		} else {
//			setTime := val.submittedAt
//			currTime := rev.submittedAt
//			if currTime.After(*setTime) {
//				mostRecentReviews[rev.name] = rev
//			}
//		}
//	}
//	return mostRecentReviews
//}
//
//// hasApproved determines if the reviewer has submitted an approval
//// for the pull request.
//func hasApproved(reviewer string, reviews map[string]review) bool {
//	for _, rev := range reviews {
//		if rev.name == reviewer && rev.status == ci.Approved {
//			return true
//		}
//	}
//	return false
//}
//
//// dimissMessage returns the dimiss message when a review is dismissed
//func dismissMessage(pr *environment.Metadata, required []string) string {
//	var sb strings.Builder
//	sb.WriteString("New commit pushed, please re-review ")
//	for _, reviewer := range required {
//		sb.WriteString(fmt.Sprintf("@%s ", reviewer))
//	}
//	return strings.TrimSpace(sb.String())
//}
//
//// hasFileDiff compares two commits and checks if there are changes.
//func (c *Bot) hasFileDiff(ctx context.Context, base, head string, compare commitComparer) (bool, error) {
//	pr := c.Environment.Metadata
//	comparison, _, err := compare.CompareCommits(ctx, pr.RepoOwner, pr.RepoName, base, head)
//	if err != nil {
//		return true, trace.Wrap(err)
//	}
//	if len(comparison.Files) != 0 {
//		return true, nil
//	}
//	return false, nil
//}
//
//// invalidateApprovals dismisses the specified reviews on a pull request.
//func (c *Bot) invalidateApprovals(ctx context.Context, reviews map[string]review) error {
//	pr := c.Environment.Metadata
//	clt := c.Environment.Client
//	msg := dismissMessage(pr, c.Environment.GetReviewersForAuthor(pr.Author))
//	for _, v := range reviews {
//		if v.status != ci.Commented {
//			_, _, err := clt.PullRequests.DismissReview(ctx,
//				pr.RepoOwner,
//				pr.RepoName,
//				pr.Number,
//				v.id,
//				&github.PullRequestReviewDismissalRequest{Message: &msg},
//			)
//			if err != nil {
//				return trace.Wrap(err)
//			}
//		}
//	}
//	// Re-assign reviewers when dismissing so the
//	// pull request shows up in their review requests again.
//	return c.Assign(ctx)
//}

//func contains(s []string, e string) bool {
//	for _, a := range s {
//		if a == e {
//			return true
//		}
//	}
//	return false
//}
