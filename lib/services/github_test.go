/*
Copyright 2017-2021 Gravitational, Inc.

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

package services

import (
	check "gopkg.in/check.v1"

	"github.com/gravitational/teleport/api/types"
)

type GithubSuite struct{}

var _ = check.Suite(&GithubSuite{})

func (g *GithubSuite) TestUnmarshal(c *check.C) {
	data := []byte(`{"kind": "github",
"version": "v3",
"metadata": {
  "name": "github"
},
"spec": {
  "client_id": "aaa",
  "client_secret": "bbb",
  "display": "Github",
  "redirect_url": "https://localhost:3080/v1/webapi/github/callback",
  "teams_to_logins": [{
    "organization": "gravitational",
    "team": "admins",
    "logins": ["admin"]
  }]
}}`)
	connector, err := UnmarshalGithubConnector(data)
	c.Assert(err, check.IsNil)
	expected, err := types.NewGithubConnector("github", types.GithubConnectorSpecV3{
		ClientID:     "aaa",
		ClientSecret: "bbb",
		RedirectURL:  "https://localhost:3080/v1/webapi/github/callback",
		Display:      "Github",
		TeamsToLogins: []types.TeamMapping{
			{
				Organization: "gravitational",
				Team:         "admins",
				Logins:       []string{"admin"},
			},
		},
	})
	c.Assert(err, check.IsNil)
	c.Assert(expected, check.DeepEquals, connector)
}

func (g *GithubSuite) TestMapClaims(c *check.C) {
	connector, err := types.NewGithubConnector("github", types.GithubConnectorSpecV3{
		ClientID:     "aaa",
		ClientSecret: "bbb",
		RedirectURL:  "https://localhost:3080/v1/webapi/github/callback",
		Display:      "Github",
		TeamsToLogins: []types.TeamMapping{
			{
				Organization: "gravitational",
				Team:         "admins",
				Logins:       []string{"admin", "dev"},
				KubeGroups:   []string{"system:masters", "kube-devs"},
				KubeUsers:    []string{"alice@example.com"},
			},
			{
				Organization: "gravitational",
				Team:         "devs",
				Logins:       []string{"dev", "test"},
				KubeGroups:   []string{"kube-devs"},
			},
		},
		TeamsToRoles: []types.TeamRolesMapping{
			{
				Organization: "gravitational",
				Team:         "admins",
				Roles:        []string{"system"},
			},
		},
	})
	c.Assert(err, check.IsNil)

	roles, kubeGroups, kubeUsers := connector.MapClaims(types.GithubClaims{
		OrganizationToTeams: map[string][]string{
			"gravitational": {"admins"},
		},
	})
	c.Assert(roles, check.DeepEquals, []string{"admin", "dev", "system"})
	c.Assert(kubeGroups, check.DeepEquals, []string{"system:masters", "kube-devs"})
	c.Assert(kubeUsers, check.DeepEquals, []string{"alice@example.com"})

	roles, kubeGroups, kubeUsers = connector.MapClaims(types.GithubClaims{
		OrganizationToTeams: map[string][]string{
			"gravitational": {"devs"},
		},
	})

	c.Assert(roles, check.DeepEquals, []string{"dev", "test"})
	c.Assert(kubeGroups, check.DeepEquals, []string{"kube-devs"})
	c.Assert(kubeUsers, check.DeepEquals, []string(nil))

	roles, kubeGroups, kubeUsers = connector.MapClaims(types.GithubClaims{
		OrganizationToTeams: map[string][]string{
			"gravitational": {"admins", "devs"},
		},
	})
	c.Assert(roles, check.DeepEquals, []string{"admin", "dev", "test", "system"})
	c.Assert(kubeGroups, check.DeepEquals, []string{"system:masters", "kube-devs"})
	c.Assert(kubeUsers, check.DeepEquals, []string{"alice@example.com"})
}
