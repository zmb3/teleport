// Copyright 2022 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package common

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/types"
	apiutils "github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/lib/config"
	"github.com/zmb3/teleport/lib/configurators"
	awsconfigurators "github.com/zmb3/teleport/lib/configurators/aws"
	"github.com/zmb3/teleport/lib/configurators/configuratorbuilder"
	"github.com/zmb3/teleport/lib/utils/prompt"
)

// awsDatabaseTypes list of databases supported on the configurator.
var awsDatabaseTypes = []string{
	types.DatabaseTypeRDS,
	types.DatabaseTypeRDSProxy,
	types.DatabaseTypeRedshift,
	types.DatabaseTypeElastiCache,
	types.DatabaseTypeMemoryDB,
}

type installSystemdFlags struct {
	config.SystemdFlags
	// output is the destination to write the systemd unit file to.
	output string
}

type createDatabaseConfigFlags struct {
	config.DatabaseSampleFlags
	// output is the destination to write the configuration to.
	output string
}

// CheckAndSetDefaults checks and sets the defaults
func (flags *installSystemdFlags) CheckAndSetDefaults() error {
	flags.output = normalizeOutput(flags.output)
	return nil
}

// onDumpSystemdUnitFile is the handler of the "install systemd" CLI command.
func onDumpSystemdUnitFile(flags installSystemdFlags) error {
	if err := flags.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}

	buf := new(bytes.Buffer)
	err := config.WriteSystemdUnitFile(flags.SystemdFlags, buf)
	if err != nil {
		return trace.Wrap(err)
	}

	_, err = dumpConfigFile(flags.output, buf.String(), "")
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}

// CheckAndSetDefaults checks and sets the defaults
func (flags *createDatabaseConfigFlags) CheckAndSetDefaults() error {
	flags.output = normalizeOutput(flags.output)
	return nil
}

// onDumpDatabaseConfig is the handler of "db configure create" CLI command.
func onDumpDatabaseConfig(flags createDatabaseConfigFlags) error {
	if err := flags.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}

	sfc, err := config.MakeDatabaseAgentConfigString(flags.DatabaseSampleFlags)
	if err != nil {
		return trace.Wrap(err)
	}

	configPath, err := dumpConfigFile(flags.output, sfc, "")
	if err != nil {
		return trace.Wrap(err)
	}

	if configPath != "" {
		fmt.Printf("Wrote config to file %q. Now you can start the server. Happy Teleporting!\n", configPath)
	}
	return nil
}

// configureDiscoveryBootstrapFlags database configure bootstrap flags.
type configureDiscoveryBootstrapFlags struct {
	config  configurators.BootstrapFlags
	confirm bool
}

// onConfigureDiscoveryBootstrap subcommand that bootstraps configuration for
// discovery  agents.
func onConfigureDiscoveryBootstrap(flags configureDiscoveryBootstrapFlags) error {
	ctx := context.TODO()
	configurators, err := configuratorbuilder.BuildConfigurators(flags.config)
	if err != nil {
		return trace.Wrap(err)
	}

	fmt.Printf("Reading configuration at %q...\n\n", flags.config.ConfigPath)
	if len(configurators) == 0 {
		fmt.Println("The agent doesn't require any extra configuration.")
		return nil
	}

	for _, configurator := range configurators {
		fmt.Println(configurator.Name())
		printDiscoveryConfiguratorActions(configurator.Actions())
	}

	if flags.config.Manual {
		return nil
	}

	fmt.Print("\n")
	if !flags.confirm {
		confirmed, err := prompt.Confirmation(ctx, os.Stdout, prompt.Stdin(), "Confirm?")
		if err != nil {
			return trace.Wrap(err)
		}

		if !confirmed {
			return nil
		}
	}

	for _, configurator := range configurators {
		err = executeDiscoveryConfiguratorActions(ctx, configurator.Name(), configurator.Actions())
		if err != nil {
			return trace.Errorf("bootstrap failed to execute, check logs above to see the cause")
		}
	}

	return nil
}

// configureDatabaseAWSFlags common flags provided to aws DB configurators.
type configureDatabaseAWSFlags struct {
	// types comma-separated list of database types that the policies will give
	// access to.
	types string
	// typesList parsed `types` into list of types.
	typesList []string
	// role the AWS role that policies will be attached to.
	role string
	// user the AWS user that policies will be attached to.
	user string
	// policyName name of the generated policy.
	policyName string
}

func (f *configureDatabaseAWSFlags) CheckAndSetDefaults() error {
	if f.types == "" {
		return trace.BadParameter("at least one --types should be provided: %s", strings.Join(awsDatabaseTypes, ","))
	}

	f.typesList = strings.Split(f.types, ",")
	for _, dbType := range f.typesList {
		if !apiutils.SliceContainsStr(awsDatabaseTypes, dbType) {
			return trace.BadParameter("--types %q not supported. supported types are: %s", dbType, strings.Join(awsDatabaseTypes, ", "))
		}
	}

	return nil
}

// configureDatabaseAWSPrintFlags flags of the "db configure aws print-iam"
// subcommand.
type configureDatabaseAWSPrintFlags struct {
	configureDatabaseAWSFlags
	// policyOnly if "true" will only prints the policy JSON.
	policyOnly bool
	// boundaryOnly if "true" will only prints the policy boundary JSON.
	boundaryOnly bool
}

// buildAWSConfigurator builds the database configurator used on AWS-specific
// commands.
func buildAWSConfigurator(manual bool, flags configureDatabaseAWSFlags) (configurators.Configurator, error) {
	err := flags.CheckAndSetDefaults()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	fileConfig := &config.FileConfig{}
	configuratorFlags := configurators.BootstrapFlags{
		Manual:       manual,
		PolicyName:   flags.policyName,
		AttachToUser: flags.user,
		AttachToRole: flags.role,
	}

	for _, dbType := range flags.typesList {
		switch dbType {
		case types.DatabaseTypeRDS:
			configuratorFlags.ForceRDSPermissions = true
		case types.DatabaseTypeRDSProxy:
			configuratorFlags.ForceRDSProxyPermissions = true
		case types.DatabaseTypeRedshift:
			configuratorFlags.ForceRedshiftPermissions = true
		case types.DatabaseTypeElastiCache:
			configuratorFlags.ForceElastiCachePermissions = true
		case types.DatabaseTypeMemoryDB:
			configuratorFlags.ForceMemoryDBPermissions = true
		}
	}

	configurator, err := awsconfigurators.NewAWSConfigurator(awsconfigurators.ConfiguratorConfig{
		Flags:      configuratorFlags,
		FileConfig: fileConfig,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return configurator, nil
}

// onConfigureDatabasesAWSPrint is a subcommand used to print AWS IAM access
// Teleport requires to run databases discovery on AWS.
func onConfigureDatabasesAWSPrint(flags configureDatabaseAWSPrintFlags) error {
	configurator, err := buildAWSConfigurator(true, flags.configureDatabaseAWSFlags)
	if err != nil {
		return trace.Wrap(err)
	}

	// Check if configurator actions is empty.
	if configurator.IsEmpty() {
		fmt.Println("The agent doesn't require any extra configuration.")
		return nil
	}

	actions := configurator.Actions()
	if flags.policyOnly {
		// Policy is present at the details of the first action.
		fmt.Println(actions[0].Details())
		return nil
	}

	if flags.boundaryOnly {
		// Policy boundary is present at the details of the second instruction.
		fmt.Println(actions[1].Details())
		return nil
	}

	printDiscoveryConfiguratorActions(actions)
	return nil
}

// configureDatabaseAWSPrintFlags flags of the "db configure aws create-iam"
// subcommand.
type configureDatabaseAWSCreateFlags struct {
	configureDatabaseAWSFlags
	attach  bool
	confirm bool
}

// onConfigureDatabasesAWSCreates is a subcommand used to create AWS IAM access
// for Teleport to run databases discovery on AWS.
func onConfigureDatabasesAWSCreate(flags configureDatabaseAWSCreateFlags) error {
	ctx := context.TODO()
	configurator, err := buildAWSConfigurator(false, flags.configureDatabaseAWSFlags)
	if err != nil {
		return trace.Wrap(err)
	}

	actions := configurator.Actions()
	printDiscoveryConfiguratorActions(actions)
	fmt.Print("\n")

	if !flags.confirm {
		confirmed, err := prompt.Confirmation(ctx, os.Stdout, prompt.Stdin(), "Confirm?")
		if err != nil {
			return trace.Wrap(err)
		}

		if !confirmed {
			return nil
		}
	}

	// Check if configurator actions is empty.
	if configurator.IsEmpty() {
		fmt.Println("The agent doesn't require any extra configuration.")
		return nil
	}

	err = executeDiscoveryConfiguratorActions(ctx, configurator.Name(), actions)
	if err != nil {
		return trace.Errorf("bootstrap failed to execute, check logs above to see the cause")
	}

	return nil
}

// printDiscoveryConfiguratorActions prints the database configurator actions.
func printDiscoveryConfiguratorActions(actions []configurators.ConfiguratorAction) {
	for i, action := range actions {
		fmt.Printf("%d. %s", i+1, action.Description())
		if len(action.Details()) > 0 {
			fmt.Printf(":\n%s\n\n", action.Details())
		} else {
			fmt.Println(".")
		}
	}
}

// executeDiscoveryConfiguratorActions iterate over all actions, executing and printing
// their results.
func executeDiscoveryConfiguratorActions(ctx context.Context, configuratorName string, actions []configurators.ConfiguratorAction) error {
	actionContext := &configurators.ConfiguratorActionContext{}
	for _, action := range actions {
		err := action.Execute(ctx, actionContext)
		printDiscoveryBootstrapActionResult(configuratorName, action, err)
		if err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

// printDiscoveryBootstrapActionResult human-readable print of the action result (error
// or success).
func printDiscoveryBootstrapActionResult(configuratorName string, action configurators.ConfiguratorAction, err error) {
	leadSymbol := "✅"
	endText := "done"
	if err != nil {
		leadSymbol = "❌"
		endText = "failed"
	}

	fmt.Printf("%s[%s] %s... %s.\n", leadSymbol, configuratorName, action.Description(), endText)
	if err != nil {
		fmt.Printf("Failure reason: %s\n", err)
	}
}
