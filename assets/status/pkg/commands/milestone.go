package commands

import "context"

func (c *Client) Milestones(ctx context.Context) error {
	return nil
}

//func fetchm() ([]*pullRequest, error) {
//	var pulls []*pullRequest
//
//	tc := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
//		&oauth2.Token{AccessToken: ""},
//	))
//	client := github.NewClient(tc)
//
//	iopts := &github.IssueListByRepoOptions{
//		Milestone: "55",
//		State:     "open",
//	}
//	milestone, resp, err := client.Issues.ListByRepo(context.Background(), "gravitational", "teleport", iopts)
//	if err != nil {
//		return nil, err
//	}
//
//	for {
//		page, resp, err := client.Issues.GetMilestone(context.Background(), "gravitational", "teleport", 55)
//		if err != nil {
//			return nil, err
//		}
//
//		for _, pr := range page {
//			lopts := &github.ListOptions{
//				PerPage: 20,
//			}
//			reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", pr.GetNumber(), lopts)
//			if err != nil {
//				return nil, err
//			}
//
//			pulls = append(pulls, &pullRequest{
//				pr:      pr,
//				reviews: reviews,
//			})
//		}
//
//		if resp.NextPage == 0 {
//			break
//		}
//		ropts.Page = resp.NextPage
//	}
//
//	return pulls, nil
//}
