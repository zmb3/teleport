package commands

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gravitational/teleport/assets/statusctl/pkg/constants"

	"github.com/gravitational/trace"

	"github.com/google/go-github/v35/github"
)

func (c *Client) Pulls(ctx context.Context) error {
	prs, err := c.fetchPulls(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := c.displayPulls(ctx, prs); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

type pullRequest struct {
	// team is the internal Teleport team this user belongs to.
	team string

	// group is how we categorize PRs. A few examples, "code", "rfd", "docs",
	// "draft", "backport", "ux".
	group string

	// openFor is how long the PR has been open.
	openFor string

	// number is the GitHub PR number, like #1234.
	number int

	// author is the GitHub handle of the PR author.
	author string

	// title is the title of the PR.
	title string

	// approvers is a slice of GitHub handles that have approved the PR. Only
	// available in verbose mode.
	approvers []string
}

func (c *Client) fetchPulls(ctx context.Context) ([]pullRequest, error) {
	var prs []pullRequest

	teams, err := c.Teams(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// ?
	popts := &github.PullRequestListOptions{
		State: constants.Open,
		ListOptions: github.ListOptions{
			PerPage: constants.PageSize,
		},
	}

	// Paginate and get all PRs.
	for {
		page, resp, err := c.client.PullRequests.List(ctx,
			constants.Organization, constants.Repository, popts)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, pr := range page {
			//ropts := &github.ListOptions{
			//	PerPage: 20,
			//}
			//reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", pr.GetNumber(), ropts)
			//if err != nil {
			//	return nil, err
			//}

			duration := time.Now().Sub(pr.GetCreatedAt())
			humanDuration := fmt.Sprintf("%vd", math.Ceil(duration.Hours()/24))

			prs = append(prs, pullRequest{
				number:    pr.GetNumber(),
				approvers: []string{},
				author:    pr.GetUser().GetLogin(),
				team:      teamForUser(teams, pr.GetUser().GetLogin()),
				group:     group(pr),
				openFor:   humanDuration,
				title:     pr.GetTitle(),
			})
		}

		// If the last page has been fetched, exit the loop, otherwise continue to
		// fetch the next page of results.
		if resp.NextPage == 0 {
			break
		}
		popts.Page = resp.NextPage
	}

	return prs, nil
}

func (c *Client) displayPulls(ctx context.Context, prs []pullRequest) error {
	template := strings.Repeat("%v\t", 6) + "\n"

	// Closures for sorting, then sort.
	group := func(c1, c2 *pullRequest) bool {
		return c1.group < c2.group
	}
	author := func(c1, c2 *pullRequest) bool {
		return c1.author < c2.author
	}
	team := func(c1, c2 *pullRequest) bool {
		return c1.team < c2.team
	}
	OrderedBy(group, team, author).Sort(prs)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.Debug)

	// Print header.
	fmt.Fprintf(w, template, "PR", "Author", "Team", "Group", "Open For", "Title")
	for _, pr := range prs {
		fmt.Fprintf(w, template, pr.number, pr.author, pr.team, pr.group, pr.openFor, pr.title)
	}
	w.Flush()

	return nil
}

func group(pr *github.PullRequest) string {
	if pr.GetDraft() {
		return constants.Draft
	}

	if hasLabel(pr, constants.RFD) {
		return constants.RFD
	}
	if hasLabel(pr, constants.Documentation) {
		return constants.Documentation
	}
	if hasLabel(pr, constants.Backport) {
		return constants.Backport
	}
	if hasLabel(pr, constants.UX) {
		return constants.UX
	}

	return constants.Code
}

func hasLabel(pr *github.PullRequest, labelName string) bool {
	for _, label := range pr.Labels {
		if label.GetName() == labelName {
			return true
		}
	}
	return false
}
