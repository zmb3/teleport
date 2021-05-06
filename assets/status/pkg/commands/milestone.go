package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/mod/semver"

	"github.com/gravitational/teleport/assets/statusctl/pkg/constants"
	"github.com/gravitational/trace"

	"github.com/google/go-github/v35/github"
)

func (c *Client) Milestones(ctx context.Context) error {
	milestones, err := c.fetchMilestones(ctx)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := c.displayMilestones(ctx, milestones); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

type milestone struct {
	version string
	issues  []issue
}

type issue struct {
	number   int
	title    string
	assignee string
	team     string
}

func (c *Client) fetchMilestones(ctx context.Context) ([]*milestone, error) {
	var milestones []*milestone

	mopts := &github.MilestoneListOptions{
		State:     constants.Open,
		Sort:      constants.DueOn,
		Direction: constants.Ascending,
		ListOptions: github.ListOptions{
			PerPage: constants.PageSize,
		},
	}
	for {
		page, resp, err := c.client.Issues.ListMilestones(ctx, constants.Organization, constants.Repository, mopts)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, m := range page {
			parts := strings.Fields(m.GetTitle())
			if len(parts) == 0 {
				continue
			}
			version := semver.Canonical("v" + parts[0])
			if !semver.IsValid(version) {
				continue
			}

			var issues []issue

			iopts := &github.IssueListByRepoOptions{
				Milestone: strconv.Itoa(m.GetNumber()),
				State:     constants.Open,
				ListOptions: github.ListOptions{
					PerPage: constants.PageSize,
				},
			}
			for {
				issuePage, issueResp, err := c.client.Issues.ListByRepo(ctx, constants.Organization, constants.Repository, iopts)
				if err != nil {
					return nil, trace.Wrap(err)
				}

				for _, i := range issuePage {
					issues = append(issues, issue{
						number:   i.GetNumber(),
						assignee: i.GetAssignee().GetLogin(),
						title:    i.GetTitle(),
					})
				}

				if issueResp.NextPage == 0 {
					break
				}
				iopts.Page = issueResp.NextPage
			}

			milestones = append(milestones, &milestone{
				version: semver.MajorMinor(version),
				issues:  issues,
			})
		}

		// If the last page has been fetched, exit the loop, otherwise continue to
		// fetch the next page of results.
		if resp.NextPage == 0 {
			break
		}
		mopts.Page = resp.NextPage
	}

	return milestones, nil
}

func (c *Client) displayMilestones(ctx context.Context, milestones []*milestone) error {
	template := strings.Repeat("%v\t", 4) + "\n"
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.Debug)

	// Print header.
	fmt.Fprintf(w, template, "Milestone", "Issue", "Assignee", "Title")

	for _, milestone := range milestones {
		for _, issue := range milestone.issues {
			fmt.Fprintf(w, template, milestone.version, issue.number, issue.assignee, issue.title)
		}
	}

	w.Flush()
	return nil
}
