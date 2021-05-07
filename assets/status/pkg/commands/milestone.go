package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
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
	group    string
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
						group:    groupIssue(i),
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
	template := strings.Repeat("%v\t", 5) + "\n"
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 0, ' ', tabwriter.Debug)

	titles := []string{
		"Milestone",
		"Issue",
		"Group",
		"Assignee",
		"Title",
	}

	separator := []int{
		len(titles[0]),
		len(titles[1]),
		len(titles[2]),
		len(titles[3]),
		len(titles[4]),
	}

	for _, milestone := range milestones {
		for _, issue := range milestone.issues {
			separator[0] = max(separator[0], len(milestone.version))
			separator[1] = max(separator[1], len(strconv.Itoa(issue.number)))
			separator[2] = max(separator[2], len(issue.group))
			separator[3] = max(separator[3], len(issue.assignee))
			separator[4] = max(separator[4], len(issue.title))
		}
	}

	// Print header.
	fmt.Fprintf(w, template, titles[0], titles[1], titles[2], titles[3], titles[4])

	// Print separator.
	fmt.Fprintf(w, template,
		strings.Repeat("-", separator[0]),
		strings.Repeat("-", separator[1]),
		strings.Repeat("-", separator[2]),
		strings.Repeat("-", separator[3]),
		strings.Repeat("-", separator[4]))

	// Print Milestone.
	for _, milestone := range milestones {
		sort.Slice(milestone.issues, func(i, j int) bool {
			switch strings.Compare(milestone.issues[i].group, milestone.issues[j].group) {
			case -1:
				return true
			case 1:
				return false
			}
			return milestone.issues[i].assignee > milestone.issues[j].assignee
		})

		for _, issue := range milestone.issues {
			fmt.Fprintf(w, template, milestone.version, issue.number, issue.group, issue.assignee, issue.title)
		}

		// Print separator.
		fmt.Fprintf(w, template,
			strings.Repeat("-", separator[0]),
			strings.Repeat("-", separator[1]),
			strings.Repeat("-", separator[2]),
			strings.Repeat("-", separator[3]),
			strings.Repeat("-", separator[4]))
	}

	w.Flush()
	return nil
}

func groupIssue(issue *github.Issue) string {
	if hasIssueLabel(issue, constants.Documentation) {
		return constants.Documentation
	}
	return constants.Code
}

func hasIssueLabel(issue *github.Issue, labelName string) bool {
	for _, label := range issue.Labels {
		if label.GetName() == labelName {
			return true
		}
	}
	return false
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
