/*
Copyright 2015-2021 Gravitational, Inc.

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
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gravitational/kingpin"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/asciitable"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/service"
)

// UserCommand implements `tctl users` set of commands
// It implements CLICommand interface
type UserCommand struct {
	config               *service.Config
	login                string
	allowedLogins        []string
	allowedWindowsLogins []string
	allowedKubeUsers     []string
	allowedKubeGroups    []string
	allowedDatabaseUsers []string
	allowedDatabaseNames []string
	allowedAWSRoleARNs   []string
	allowedRoles         []string

	ttl time.Duration

	// format is the output format, e.g. text or json
	format string

	userAdd           *kingpin.CmdClause
	userUpdate        *kingpin.CmdClause
	userList          *kingpin.CmdClause
	userDelete        *kingpin.CmdClause
	userResetPassword *kingpin.CmdClause
}

// Initialize allows UserCommand to plug itself into the CLI parser
func (u *UserCommand) Initialize(app *kingpin.Application, config *service.Config) {
	const helpPrefix string = "[Teleport DB users only]"

	u.config = config
	users := app.Command("users", "Manage user accounts")

	u.userAdd = users.Command("add", "Generate a user invitation token "+helpPrefix)
	u.userAdd.Arg("account", "Teleport user account name").Required().StringVar(&u.login)

	u.userAdd.Flag("logins", "List of allowed SSH logins for the new user").StringsVar(&u.allowedLogins)
	u.userAdd.Flag("windows-logins", "List of allowed Windows logins for the new user").StringsVar(&u.allowedWindowsLogins)
	u.userAdd.Flag("kubernetes-users", "List of allowed Kubernetes users for the new user").StringsVar(&u.allowedKubeUsers)
	u.userAdd.Flag("kubernetes-groups", "List of allowed Kubernetes groups for the new user").StringsVar(&u.allowedKubeGroups)
	u.userAdd.Flag("db-users", "List of allowed database users for the new user").StringsVar(&u.allowedDatabaseUsers)
	u.userAdd.Flag("db-names", "List of allowed database names for the new user").StringsVar(&u.allowedDatabaseNames)
	u.userAdd.Flag("aws-role-arns", "List of allowed AWS role ARNs for the new user").StringsVar(&u.allowedAWSRoleARNs)

	u.userAdd.Flag("roles", "List of roles for the new user to assume").Required().StringsVar(&u.allowedRoles)

	u.userAdd.Flag("ttl", fmt.Sprintf("Set expiration time for token, default is %v, maximum is %v",
		defaults.SignupTokenTTL, defaults.MaxSignupTokenTTL)).
		Default(fmt.Sprintf("%v", defaults.SignupTokenTTL)).DurationVar(&u.ttl)
	u.userAdd.Flag("format", "Output format, 'text' or 'json'").Hidden().Default(teleport.Text).StringVar(&u.format)
	u.userAdd.Alias(AddUserHelp)

	u.userUpdate = users.Command("update", "Update user account")
	u.userUpdate.Arg("account", "Teleport user account name").Required().StringVar(&u.login)
	u.userUpdate.Flag("set-roles", "List of roles for the user to assume, replaces current roles").
		StringsVar(&u.allowedRoles)
	u.userUpdate.Flag("set-logins", "List of allowed SSH logins for the user, replaces current logins").
		StringsVar(&u.allowedLogins)
	u.userUpdate.Flag("set-windows-logins", "List of allowed Windows logins for the user, replaces current Windows logins").
		StringsVar(&u.allowedWindowsLogins)
	u.userUpdate.Flag("set-kubernetes-users", "List of allowed Kubernetes users for the user, replaces current Kubernetes users").
		StringsVar(&u.allowedKubeUsers)
	u.userUpdate.Flag("set-kubernetes-groups", "List of allowed Kubernetes groups for the user, replaces current Kubernetes groups").
		StringsVar(&u.allowedKubeGroups)
	u.userUpdate.Flag("set-db-users", "List of allowed database users for the user, replaces current database users").
		StringsVar(&u.allowedDatabaseUsers)
	u.userUpdate.Flag("set-db-names", "List of allowed database names for the user, replaces current database names").
		StringsVar(&u.allowedDatabaseNames)
	u.userUpdate.Flag("set-aws-role-arns", "List of allowed AWS role ARNs for the user, replaces current AWS role ARNs").
		StringsVar(&u.allowedAWSRoleARNs)

	u.userList = users.Command("ls", "Lists all user accounts.")
	u.userList.Flag("format", "Output format, 'text' or 'json'").Hidden().Default(teleport.Text).StringVar(&u.format)

	u.userDelete = users.Command("rm", "Deletes user accounts").Alias("del")
	u.userDelete.Arg("logins", "Comma-separated list of user logins to delete").
		Required().StringVar(&u.login)

	u.userResetPassword = users.Command("reset", "Reset user password and generate a new token "+helpPrefix)
	u.userResetPassword.Arg("account", "Teleport user account name").Required().StringVar(&u.login)
	u.userResetPassword.Flag("ttl", fmt.Sprintf("Set expiration time for token, default is %v, maximum is %v",
		defaults.ChangePasswordTokenTTL, defaults.MaxChangePasswordTokenTTL)).
		Default(fmt.Sprintf("%v", defaults.ChangePasswordTokenTTL)).DurationVar(&u.ttl)
	u.userResetPassword.Flag("format", "Output format, 'text' or 'json'").Hidden().Default(teleport.Text).StringVar(&u.format)
}

// TryRun takes the CLI command as an argument (like "users add") and executes it.
func (u *UserCommand) TryRun(ctx context.Context, cmd string, client auth.ClientI) (match bool, err error) {
	switch cmd {
	case u.userAdd.FullCommand():
		err = u.Add(ctx, client)
	case u.userUpdate.FullCommand():
		err = u.Update(ctx, client)
	case u.userList.FullCommand():
		err = u.List(ctx, client)
	case u.userDelete.FullCommand():
		err = u.Delete(ctx, client)
	case u.userResetPassword.FullCommand():
		err = u.ResetPassword(ctx, client)
	default:
		return false, nil
	}
	return true, trace.Wrap(err)
}

// ResetPassword resets user password and generates a token to setup new password
func (u *UserCommand) ResetPassword(ctx context.Context, client auth.ClientI) error {
	req := auth.CreateUserTokenRequest{
		Name: u.login,
		TTL:  u.ttl,
		Type: auth.UserTokenTypeResetPassword,
	}
	token, err := client.CreateResetPasswordToken(ctx, req)
	if err != nil {
		return err
	}

	err = u.PrintResetPasswordToken(token, u.format)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// PrintResetPasswordToken prints ResetPasswordToken
func (u *UserCommand) PrintResetPasswordToken(token types.UserToken, format string) error {
	err := u.printResetPasswordToken(token,
		format,
		"User %q has been reset. Share this URL with the user to complete password reset, link is valid for %v:\n%v\n\n",
	)
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// PrintResetPasswordTokenAsInvite prints ResetPasswordToken as Invite
func (u *UserCommand) PrintResetPasswordTokenAsInvite(token types.UserToken, format string) error {
	err := u.printResetPasswordToken(token,
		format,
		"User %q has been created but requires a password. Share this URL with the user to complete user setup, link is valid for %v:\n%v\n\n")
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// PrintResetPasswordToken prints ResetPasswordToken
func (u *UserCommand) printResetPasswordToken(token types.UserToken, format string, messageFormat string) (err error) {
	switch strings.ToLower(u.format) {
	case teleport.JSON:
		err = printTokenAsJSON(token)
	case teleport.Text:
		err = printTokenAsText(token, messageFormat)
	default:
		err = printTokenAsText(token, messageFormat)
	}

	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// Add implements `tctl users add` for the enterprise edition. Unlike the OSS
// version, this one requires --roles flag to be set
func (u *UserCommand) Add(ctx context.Context, client auth.ClientI) error {
	u.allowedRoles = flattenSlice(u.allowedRoles)
	u.allowedLogins = flattenSlice(u.allowedLogins)
	u.allowedWindowsLogins = flattenSlice(u.allowedWindowsLogins)

	// Validate roles (server does not do this yet).
	for _, roleName := range u.allowedRoles {
		if _, err := client.GetRole(ctx, roleName); err != nil {
			return trace.Wrap(err)
		}
	}

	traits := map[string][]string{
		constants.TraitLogins:        u.allowedLogins,
		constants.TraitWindowsLogins: u.allowedWindowsLogins,
		constants.TraitKubeUsers:     flattenSlice(u.allowedKubeUsers),
		constants.TraitKubeGroups:    flattenSlice(u.allowedKubeGroups),
		constants.TraitDBUsers:       flattenSlice(u.allowedDatabaseUsers),
		constants.TraitDBNames:       flattenSlice(u.allowedDatabaseNames),
		constants.TraitAWSRoleARNs:   flattenSlice(u.allowedAWSRoleARNs),
	}

	user, err := types.NewUser(u.login)
	if err != nil {
		return trace.Wrap(err)
	}

	user.SetTraits(traits)
	user.SetRoles(u.allowedRoles)

	if err := client.CreateUser(ctx, user); err != nil {
		return trace.Wrap(err)
	}

	token, err := client.CreateResetPasswordToken(ctx, auth.CreateUserTokenRequest{
		Name: u.login,
		TTL:  u.ttl,
		Type: auth.UserTokenTypeResetPasswordInvite,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	if err := u.PrintResetPasswordTokenAsInvite(token, u.format); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// flattenSlice takes a slice of strings like ["one,two", "three"] and returns
// ["one", "two", "three"]
func flattenSlice(slice []string) (retval []string) {
	for i := range slice {
		for _, role := range strings.Split(slice[i], ",") {
			retval = append(retval, strings.TrimSpace(role))
		}
	}
	return retval
}

func printTokenAsJSON(token types.UserToken) error {
	out, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return trace.Wrap(err, "failed to marshal reset password token")
	}
	fmt.Print(string(out))
	return nil
}

func printTokenAsText(token types.UserToken, messageFormat string) error {
	url, err := url.Parse(token.GetURL())
	if err != nil {
		return trace.Wrap(err, "failed to parse reset password token url")
	}

	ttl := trimDurationZeroSuffix(token.Expiry().Sub(time.Now().UTC()))
	fmt.Printf(messageFormat, token.GetUser(), ttl, url)
	fmt.Printf("NOTE: Make sure %v points at a Teleport proxy which users can access.\n", url.Host)
	return nil
}

// Update updates existing user
func (u *UserCommand) Update(ctx context.Context, client auth.ClientI) error {
	user, err := client.GetUser(u.login, false)
	if err != nil {
		return trace.Wrap(err)
	}

	updateMessages := make(map[string][]string)
	if len(u.allowedRoles) > 0 {
		roles := flattenSlice(u.allowedRoles)
		for _, role := range roles {
			if _, err := client.GetRole(ctx, role); err != nil {
				return trace.Wrap(err)
			}
		}
		user.SetRoles(roles)
		updateMessages["roles"] = roles
	}
	if len(u.allowedLogins) > 0 {
		logins := flattenSlice(u.allowedLogins)
		user.SetLogins(logins)
		updateMessages["logins"] = logins
	}
	if len(u.allowedWindowsLogins) > 0 {
		windowsLogins := flattenSlice(u.allowedWindowsLogins)
		user.SetWindowsLogins(windowsLogins)
		updateMessages["Windows logins"] = windowsLogins
	}
	if len(u.allowedKubeUsers) > 0 {
		kubeUsers := flattenSlice(u.allowedKubeUsers)
		user.SetKubeUsers(kubeUsers)
		updateMessages["Kubernetes users"] = kubeUsers
	}
	if len(u.allowedKubeGroups) > 0 {
		kubeGroups := flattenSlice(u.allowedKubeGroups)
		user.SetKubeGroups(kubeGroups)
		updateMessages["Kubernetes groups"] = kubeGroups
	}
	if len(u.allowedDatabaseUsers) > 0 {
		dbUsers := flattenSlice(u.allowedDatabaseUsers)
		user.SetDatabaseUsers(dbUsers)
		updateMessages["database users"] = dbUsers
	}
	if len(u.allowedDatabaseNames) > 0 {
		dbNames := flattenSlice(u.allowedDatabaseNames)
		user.SetDatabaseNames(dbNames)
		updateMessages["database names"] = dbNames
	}
	if len(u.allowedAWSRoleARNs) > 0 {
		awsRoleARNs := flattenSlice(u.allowedAWSRoleARNs)
		user.SetAWSRoleARNs(awsRoleARNs)
		updateMessages["AWS role ARNs"] = awsRoleARNs
	}

	if len(updateMessages) == 0 {
		return trace.BadParameter("Nothing to update. Please provide at least one --set flag.")
	}

	if err := client.UpsertUser(user); err != nil {
		return trace.Wrap(err)
	}
	fmt.Printf("User %v has been updated:\n", user.GetName())
	for field, values := range updateMessages {
		fmt.Printf("\tNew %v: %v\n", field, strings.Join(values, ","))
	}
	return nil
}

// List prints all existing user accounts
func (u *UserCommand) List(ctx context.Context, client auth.ClientI) error {
	users, err := client.GetUsers(false)
	if err != nil {
		return trace.Wrap(err)
	}
	if u.format == teleport.Text {
		if len(users) == 0 {
			fmt.Println("No users found")
			return nil
		}
		t := asciitable.MakeTable([]string{"User", "Roles"})
		for _, u := range users {
			t.AddRow([]string{
				u.GetName(), strings.Join(u.GetRoles(), ","),
			})
		}
		fmt.Println(t.AsBuffer().String())
	} else {
		out, err := json.MarshalIndent(users, "", "  ")
		if err != nil {
			return trace.Wrap(err, "failed to marshal users")
		}
		fmt.Print(string(out))
	}
	return nil
}

// Delete deletes teleport user(s). User IDs are passed as a comma-separated
// list in UserCommand.login
func (u *UserCommand) Delete(ctx context.Context, client auth.ClientI) error {
	for _, l := range strings.Split(u.login, ",") {
		if err := client.DeleteUser(ctx, l); err != nil {
			return trace.Wrap(err)
		}
		fmt.Printf("User %q has been deleted\n", l)
	}
	return nil
}

func trimDurationZeroSuffix(d time.Duration) string {
	s := d.Round(time.Second).String()
	switch {
	case strings.HasSuffix(s, "h0m0s"):
		return strings.TrimSuffix(s, "0m0s")
	case strings.HasSuffix(s, "m0s"):
		return strings.TrimSuffix(s, "0s")
	default:
		return s
	}
}
