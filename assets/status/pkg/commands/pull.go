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
	//PR      *github.PullRequest
	//Reviews []*github.PullRequestReview

	// team is the internal Teleport team this user belongs to.
	team string

	// group is how we categorize PRs. A few examples, "code", "rfd", "docs",
	// "draft", "backport".
	group string

	// openFor is how long the PR has been open.
	//openFor time.Duration
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

//func exclude(pr *github.PullRequest) bool {
//	if pr.GetState() == "draft" {
//		return true
//	}
//	for _, label := range pr.Labels {
//		if *label.Name == "documentation" {
//			return true
//		}
//	}
//	return false
//}
//func isMember(team string, name string) bool {
//	for _, s := range teams[team] {
//		if name == s {
//			return true
//		}
//	}
//	return false
//}
//
//func isAnyTeam(name string) bool {
//	for _, v := range teams {
//		for _, vv := range v {
//			if vv == name {
//				return true
//			}
//		}
//	}
//	return false
//}
//
//func printTeam(team string, prs []*github.PullRequest) {
//	n := 0
//	groups := map[string][]*github.PullRequest{}
//
//	for _, pr := range prs {
//		user := *pr.GetUser().Login
//
//		if exclude(pr) {
//			continue
//		}
//		if !isMember(team, user) {
//			continue
//		}
//
//		n = n + 1
//
//		var ok bool
//		pullslice := []*github.PullRequest{}
//
//		if pullslice, ok = groups[user]; ok {
//			pullslice = groups[user]
//		}
//		pullslice = append(pullslice, pr)
//		groups[user] = pullslice
//	}
//
//	if n == 0 {
//		return
//	}
//
//	fmt.Printf("--------------------------------------------------------------------------------\n")
//	fmt.Printf("Team: %v, Open: %v\n", team, n)
//	fmt.Printf("--------------------------------------------------------------------------------\n")
//
//	for k, v := range groups {
//		for _, vv := range v {
//			duration := time.Now().Sub(*vv.CreatedAt)
//			humanDuration := fmt.Sprintf("%v", duration.Round(24*time.Hour))
//
//			fmt.Printf("%-5v %-20v %-10v %v.\n", *vv.Number, k, humanDuration, *vv.Title)
//		}
//	}
//}

//type summaryView struct {
//	// draft, docs, rfd, code
//	category string
//	openfor  string
//	team     string
//	number   int
//	author   string
//	title    string
//	count    int
//}
//
//func summary(pulls []*pullRequest) {
//	sv := make([]summaryView, 0, len(pulls))
//
//	var cn int
//	var rn int
//	var dn int
//
//	for _, pull := range pulls {
//		var n int
//		for _, review := range pull.reviews {
//			if review.GetState() == "APPROVED" {
//				n += 1
//			}
//		}
//
//		switch getCategory(pull.pr) {
//		case "code":
//			cn += 1
//		case "docs":
//			dn += 1
//		case "rfd":
//			rn += 1
//		}
//
//		team := "external"
//		if n, ok := teams[pull.pr.GetUser().GetLogin()]; ok {
//			team = n
//		}
//
//		duration := time.Now().Sub(pull.pr.GetCreatedAt())
//		humanDuration := fmt.Sprintf("%vd", math.Ceil(duration.Hours()/24))
//
//		sv = append(sv, summaryView{
//			category: getCategory(pull.pr),
//			team:     team,
//			openfor:  humanDuration,
//			number:   pull.pr.GetNumber(),
//			author:   pull.pr.GetUser().GetLogin(),
//			count:    n,
//			title:    pull.pr.GetTitle(),
//		})
//	}
//
//	sort.Slice(sv, func(i, j int) bool {
//		if sv[i].team < sv[j].team {
//			return true
//		}
//		if sv[i].team > sv[j].team {
//			return false
//		}
//		return sv[i].author < sv[j].author
//	})
//
//	fmt.Printf("code: open %v\n", cn)
//	printSummary(sv, "code")
//
//	fmt.Printf("\nrfd: open: %v\n", rn)
//	printSummary(sv, "rfd")
//
//	fmt.Printf("\ndocs: open: %v\n", dn)
//	printSummary(sv, "docs")
//}
//
//func printSummary(sv []summaryView, category string) {
//	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.AlignRight|tabwriter.Debug)
//	for _, v := range sv {
//		if v.category != category {
//			continue
//		}
//		fmt.Fprintln(w, fmt.Sprintf("%v\t%v\t%v\t%v\t%v\t%v\t", v.number, v.count, v.openfor, v.author, v.team, v.title))
//	}
//	w.Flush()
//
//}
//
//func getCategory(pr *github.PullRequest) string {
//	if pr.GetDraft() {
//		return "draft"
//	}
//	if hasLabel(pr, "documentation") {
//		return "docs"
//	}
//	if hasLabel(pr, "rfd") {
//		return "rfd"
//	}
//	return "code"
//}
//
//func isDraft(pr *github.PullRequest) bool {
//	fmt.Printf("%v.\n", pr.GetState())
//	if pr.GetState() == "draft" {
//		return true
//	}
//	return false
//}
//
//func hasLabel(pr *github.PullRequest, name string) bool {
//	for _, label := range pr.Labels {
//		if label.GetName() == name {
//			return true
//		}
//	}
//	return false
//}

//printTeam("security", prs)
//printTeam("scale", prs)
//printTeam("sshkube", prs)
//printTeam("appdb", prs)
//printTeam("release", prs)
