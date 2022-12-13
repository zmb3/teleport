/*
Copyright 2020 Gravitational, Inc.

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

package common

import (
	"context"
	"os"
	"text/template"

	"github.com/gravitational/kingpin"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	libclient "github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/utils"
)

// AppsCommand implements "tctl apps" group of commands.
type AppsCommand struct {
	config *service.Config

	// format is the output format (text, json, or yaml)
	format string

	searchKeywords string
	predicateExpr  string
	labels         string

	// verbose sets whether full table output should be shown for labels
	verbose bool

	// appsList implements the "tctl apps ls" subcommand.
	appsList *kingpin.CmdClause
}

// Initialize allows AppsCommand to plug itself into the CLI parser
func (c *AppsCommand) Initialize(app *kingpin.Application, config *service.Config) {
	c.config = config

	apps := app.Command("apps", "Operate on applications registered with the cluster.")
	c.appsList = apps.Command("ls", "List all applications registered with the cluster.")
	c.appsList.Flag("format", "Output format, 'text', 'json', or 'yaml'").Default(teleport.Text).StringVar(&c.format)
	c.appsList.Arg("labels", labelHelp).StringVar(&c.labels)
	c.appsList.Flag("search", searchHelp).StringVar(&c.searchKeywords)
	c.appsList.Flag("query", queryHelp).StringVar(&c.predicateExpr)
	c.appsList.Flag("verbose", "Verbose table output, shows full label output").Short('v').BoolVar(&c.verbose)
}

// TryRun attempts to run subcommands like "apps ls".
func (c *AppsCommand) TryRun(ctx context.Context, cmd string, client auth.ClientI) (match bool, err error) {
	switch cmd {
	case c.appsList.FullCommand():
		err = c.ListApps(ctx, client)
	default:
		return false, nil
	}
	return true, trace.Wrap(err)
}

// ListApps prints the list of applications that have recently sent heartbeats
// to the cluster.
func (c *AppsCommand) ListApps(ctx context.Context, clt auth.ClientI) error {
	labels, err := libclient.ParseLabelSpec(c.labels)
	if err != nil {
		return trace.Wrap(err)
	}

	var servers []types.AppServer
	resources, err := client.GetResourcesWithFilters(ctx, clt, proto.ListResourcesRequest{
		ResourceType:        types.KindAppServer,
		Labels:              labels,
		PredicateExpression: c.predicateExpr,
		SearchKeywords:      libclient.ParseSearchKeywords(c.searchKeywords, ','),
	})
	switch {
	case err != nil:
		if utils.IsPredicateError(err) {
			return trace.Wrap(utils.PredicateError{Err: err})
		}
		return trace.Wrap(err)
	default:
		servers, err = types.ResourcesWithLabels(resources).AsAppServers()
		if err != nil {
			return trace.Wrap(err)
		}
	}

	coll := &appServerCollection{servers: servers, verbose: c.verbose}

	switch c.format {
	case teleport.Text:
		return trace.Wrap(coll.writeText(os.Stdout))
	case teleport.JSON:
		return trace.Wrap(coll.writeJSON(os.Stdout))
	case teleport.YAML:
		return trace.Wrap(coll.writeYAML(os.Stdout))
	default:
		return trace.BadParameter("unknown format %q", c.format)
	}
}

var appMessageTemplate = template.Must(template.New("app").Parse(`The invite token: {{.token}}
This token will expire in {{.minutes}} minutes.

Fill out and run this command on a node to make the application available:

> teleport app start \
   --token={{.token}} \{{range .ca_pins}}
   --ca-pin={{.}} \{{end}}
   --auth-server={{.auth_server}} \
   --name={{printf "%-30v" .app_name}} ` + "`" + `# Change "{{.app_name}}" to the name of your application.` + "`" + ` \
   --uri={{printf "%-31v" .app_uri}} ` + "`" + `# Change "{{.app_uri}}" to the address of your application.` + "`" + `

Your application will be available at {{.app_public_addr}}.

Please note:

  - This invitation token will expire in {{.minutes}} minutes.
  - {{.auth_server}} must be reachable from the new application service.
  - Update DNS to point {{.app_public_addr}} to the Teleport proxy.
  - Add a TLS certificate for {{.app_public_addr}} to the Teleport proxy under "https_keypairs".
`))
