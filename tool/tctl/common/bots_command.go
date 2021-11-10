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

package common

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/gravitational/kingpin"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/service"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/trace"
)

type BotsCommand struct {
	format string

	lockExpires string
	lockTTL     time.Duration

	botName  string
	botRoles string

	botsList   *kingpin.CmdClause
	botsAdd    *kingpin.CmdClause
	botsRemove *kingpin.CmdClause
	botsLock   *kingpin.CmdClause
	botsUnlock *kingpin.CmdClause
}

// Initialize sets up the "tctl bots" command.
func (c *BotsCommand) Initialize(app *kingpin.Application, config *service.Config) {
	bots := app.Command("bots", "Operate on certificate renewal bots registered with the cluster.")

	c.botsList = bots.Command("ls", "List all certificate renewal bots registered with the cluster.")
	c.botsList.Flag("format", "Output format, 'text', 'json', or 'yaml'").Default("text").StringVar(&c.format)

	c.botsAdd = bots.Command("add", "Add a new certificate renewal bot to the cluster.")
	c.botsAdd.Flag("name", "A name to uniquely identify this bot in the cluster.").Required().StringVar(&c.botName)
	c.botsAdd.Flag("roles", "Roles the bot is able to assume.").Required().StringVar(&c.botRoles)
	// TODO: --token for optionally specifying the join token to use?
	// TODO: --ttl for setting a ttl on the join oken

	c.botsRemove = bots.Command("rm", "Permanently remove a certificate renewal bot from the cluster.")

	c.botsLock = bots.Command("lock", "Prevent a bot from renewing its certificates.")
	c.botsLock.Flag("expires", "Time point (RFC3339) when the lock expires.").StringVar(&c.lockExpires)
	c.botsLock.Flag("ttl", "Time duration after which the lock expires.").DurationVar(&c.lockTTL)
	// TODO: id/name flag or arg instead? what do other commands do?

	c.botsUnlock = bots.Command("unlock", "Unlock a locked bot, allowing it to resume renewing certificates.")
}

// TryRun attemps to run subcommands.
func (c *BotsCommand) TryRun(cmd string, client auth.ClientI) (match bool, err error) {
	// TODO: create a smaller interface - we don't need all of ClientI

	switch cmd {
	case c.botsList.FullCommand():
		err = c.ListBots(client)
	case c.botsAdd.FullCommand():
		err = c.AddBot(client)
	case c.botsRemove.FullCommand():
		err = c.RemoveBot(client)
	case c.botsLock.FullCommand():
		err = c.LockBot(client)
	case c.botsUnlock.FullCommand():
		err = c.UnlockBot(client)
	default:
		return false, nil
	}

	return true, trace.Wrap(err)
}

// TODO: define a smaller interface than auth.ClientI for the CLI commands
// (we only use a small subset of the giant ClientI interface)

// ListBots writes a listing of the cluster's certificate renewal bots
// to standard out.
func (c *BotsCommand) ListBots(client auth.ClientI) error {
	bots, err := client.GetBots(context.TODO(), apidefaults.Namespace)
	if err != nil {
		return trace.Wrap(err)
	}

	// TODO: handle format (JSON, etc)
	// TODO: collection is going to also need locks so it can write that status
	err = (&botCollection{bots: bots}).writeText(os.Stdout)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

var startMessageTemplate = template.Must(template.New("node").Parse(`The bot token: {{.token}}.
This token will expire in {{.minutes}} minutes.

Run this on the new bot node to join the cluster:

> tbot start \
   --token={{.token}} \{{range .ca_pins}}
   --ca-pin={{.}} \{{end}}
   --auth-server={{.auth_server}}

Please note:

  - This invitation token will expire in {{.minutes}} minutes
  - {{.auth_server}} must be reachable from the new node
`))

// AddBot adds a new certificate renewal bot to the cluster.
func (c *BotsCommand) AddBot(client auth.ClientI) error {
	// At this point, we don't know whether the bot will be
	// used to generate user certs, host certs, or both.
	// We create a user and a host join token so the bot will
	// just work in either mode.
	userName := "bot-" + strings.ReplaceAll(c.botName, " ", "-")
	roleName := userName + "-impersonator"

	_, err := client.GetRole(context.Background(), roleName)
	if err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	if roleExists := (err == nil); roleExists {
		return trace.AlreadyExists("cannot add bot: role %q already exists", roleName)
	}

	if err := c.addImpersonatorRole(client, userName, roleName); err != nil {
		return trace.Wrap(err)
	}

	user, err := types.NewUser(userName)
	if err != nil {
		return trace.Wrap(err)
	}

	// TODO: originally this gives the user an impersonation role for... itself
	// (probably in hopes of being able to issue certs for a named role rather
	// than a user, which appears to be impossible)
	user.SetRoles([]string{roleName})

	// user.SetTraits(nil)

	if err := client.CreateUser(context.TODO(), user); err != nil {
		return trace.Wrap(err)
	}
	fmt.Println("created user", userName)

	// TODO: make this user configurable via CLI?
	ttl := time.Hour * 24 * 7

	// TODO: we create a User for the bot. CreateBotJoinToken authorizes for
	// Update/Bot, even though we then create a token for the associated User.
	// Is this sane?

	// Create the user token, used by the bot to generate user SSH certificates.
	userToken, err := client.CreateBotJoinToken(context.TODO(), auth.CreateUserTokenRequest{
		Name: userName,
		TTL:  time.Hour,
		Type: auth.UserTokenTypeBot,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	fmt.Println("user token: ", userToken)

	// Create the node join token, used by the bot to join the cluster and fetch
	// host certificates.
	// To ease the UX, we'll now create a host token that explicitly re-uses
	// the user token.
	// TODO: can the bot join as only one type (auto-expiring the other), or
	// should it always join as both?
	token, err := client.GenerateToken(context.Background(), auth.GenerateTokenRequest{
		Roles:  types.SystemRoles{types.RoleProvisionToken, types.RoleNode},
		TTL:    ttl,
		Token:  userToken.GetName(),
		Labels: map[string]string{"bot": c.botName},
	})
	if err != nil {
		return trace.Wrap(err)
	}
	fmt.Println("generated token for bot:", token)

	// Calculate the CA pins for this cluster. The CA pins are used by the
	// client to verify the identity of the Auth Server.
	localCAResponse, err := client.GetClusterCACert()
	if err != nil {
		return trace.Wrap(err)
	}
	caPins, err := tlsca.CalculatePins(localCAResponse.TLSCA)
	if err != nil {
		return trace.Wrap(err)
	}

	authServers, err := client.GetAuthServers()
	if err != nil {
		return trace.Wrap(err)
	}
	if len(authServers) == 0 {
		return trace.Errorf("This cluster does not have any auth servers running.")
	}

	return startMessageTemplate.Execute(os.Stdout, map[string]interface{}{
		"token":       token,
		"minutes":     int(ttl.Minutes()),
		"ca_pins":     caPins,
		"auth_server": authServers[0].GetAddr(),
	})
}

func (c *BotsCommand) addImpersonatorRole(client auth.ClientI, userName, roleName string) error {
	return client.UpsertRole(context.Background(), &types.RoleV4{
		Kind:    types.KindRole,
		Version: types.V4,
		Metadata: types.Metadata{
			Name:        roleName,
			Description: fmt.Sprintf("Automatically generated impersonator role for certificate renewal bot %s", c.botName),
		},
		Spec: types.RoleSpecV4{
			Options: types.RoleOptions{
				MaxSessionTTL: types.Duration(12 * time.Hour),
			},
			Allow: types.RoleConditions{
				Rules: []types.Rule{
					// read certificate authorities to watch for CA rotations
					types.NewRule(types.KindCertAuthority, []string{types.VerbReadNoSecrets}),
				},
				Impersonate: &types.ImpersonateConditions{
					Roles: splitRoles(c.botRoles),
					Users: []string{userName},
				},
			},
		},
	})
}

func (c *BotsCommand) RemoveBot(client auth.ClientI) error {
	// TODO:
	// remove the bot's associated impersonator role
	// remove any locks for the bot's impersonator role?
	// remove the bot's user
	return trace.NotImplemented("")
}

func (c *BotsCommand) LockBot(client auth.ClientI) error {
	lockExpiry, err := computeLockExpiry(c.lockExpires, c.lockTTL)
	if err != nil {
		return trace.Wrap(err)
	}

	lock, err := types.NewLock(uuid.New().String(), types.LockSpecV2{
		Target:  types.LockTarget{}, // TODO: fill in role for impersonator
		Expires: lockExpiry,
		Message: "The certificate renewal bot associated with this role has been locked.",
	})
	if err != nil {
		return trace.Wrap(err)
	}

	if err := client.UpsertLock(context.Background(), lock); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func (c *BotsCommand) UnlockBot(client auth.ClientI) error {
	// find the lock with a target role corresponding to this bot and remove it
	return trace.NotImplemented("")
}

func splitRoles(flag string) []string {
	var roles []string
	for _, s := range strings.Split(flag, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		roles = append(roles, s)
	}
	return roles
}
