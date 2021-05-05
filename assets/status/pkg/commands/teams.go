package commands

import (
	"context"
	"strings"

	"github.com/gravitational/teleport/assets/statusctl/pkg/constants"

	"github.com/gravitational/trace"

	"github.com/google/go-github/v35/github"
)

func (c *Client) Teams(ctx context.Context) (map[string][]string, error) {
	teams := make(map[string][]string)

	topts := &github.ListOptions{
		PerPage: constants.PageSize,
	}

	for {
		teamPages, resp, err := c.client.Teams.ListTeams(ctx,
			constants.Organization, topts)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		for _, team := range teamPages {
			if !strings.HasPrefix(team.GetName(), constants.TeamPrefix) {
				continue
			}

			var members []string

			lopts := &github.TeamListTeamMembersOptions{
				ListOptions: github.ListOptions{
					PerPage: constants.PageSize,
				},
			}
			for {
				memberPages, resp, err := c.client.Teams.ListTeamMembersBySlug(ctx,
					constants.Organization, team.GetSlug(), lopts)
				if err != nil {
					return nil, trace.Wrap(err)
				}

				for _, member := range memberPages {
					members = append(members, member.GetLogin())
				}
				if resp.NextPage == 0 {
					break
				}
				lopts.Page = resp.NextPage
			}

			teams[team.GetName()] = members
		}

		if resp.NextPage == 0 {
			break
		}
		topts.Page = resp.NextPage
	}

	return teams, nil
}

func teamForUser(teams map[string][]string, login string) string {
	for team, members := range teams {
		if contains(members, login) {
			return strings.TrimPrefix(team, constants.TeamPrefix)
		}
	}
	return "unknown"
}

func contains(s []string, t string) bool {
	for _, a := range s {
		if a == t {
			return true
		}
	}
	return false
}
