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
	"os/exec"
	"strconv"
	"strings"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/lib/client/identityfile"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
)

// Options combines init/start teleport options
type Options struct {
	// Args is a list of command-line args passed from main()
	Args []string
}

type CommandLineFlags struct {
	// --auth-server flag
	AuthServerAddr []string
	// --token flag
	AuthToken string
	// -d flag
	Debug bool
	// --cert flags
	Certs []string
}

type Config struct {
	Addrs           []utils.NetAddr
	AuthToken       string
	CertStoreParams []CertStoreParams
}

type CertStoreParams struct {
	format identityfile.Format
	path   string
	reload func() error
}

// Run inits/starts the process according to the provided options
func Run(ctx context.Context, options Options) {
	var err error

	// configure trace's errors to produce full stack traces
	isDebug, _ := strconv.ParseBool(os.Getenv(teleport.VerboseLogsEnvVar))
	if isDebug {
		trace.SetDebug(true)
	}
	// configure logger for a typical CLI scenario until configuration file is
	// parsed
	utils.InitLogger(utils.LoggingForDaemon, log.ErrorLevel)
	app := utils.InitCLIParser("teleport", "Clustered SSH service. Learn more at https://goteleport.com/teleport")

	// define global flags:
	var clf CommandLineFlags

	// define commands:
	start := app.Command("start", "Starts the Teleport service.")
	ver := app.Command("version", "Print the version.")
	app.HelpFlag.Short('h')

	// define start flags:
	start.Flag("debug", "Enable verbose logging to stderr").
		Short('d').
		BoolVar(&clf.Debug)
	start.Flag("auth-server",
		fmt.Sprintf("Address of the auth server [%s]", defaults.AuthConnectAddr().Addr)).
		StringsVar(&clf.AuthServerAddr)
	start.Flag("token",
		"Invitation token to register with an auth server [none]").
		StringVar(&clf.AuthToken)
	start.Flag("cert", "Cert for Teleport Cert Bot to manage").
		StringsVar(&clf.Certs)

	// parse CLI commands+flags:
	command, err := app.Parse(options.Args)
	if err != nil {
		utils.FatalError(err)
	}

	// execute the selected command unless we're running tests
	switch command {
	case start.FullCommand():
		err = OnStart(ctx, clf)
	case ver.FullCommand():
		utils.PrintVersion()
	}
	if err != nil {
		utils.FatalError(err)
	}
}

// OnStart is the handler for "start" CLI command
func OnStart(ctx context.Context, clf CommandLineFlags) error {
	cfg, err := ParseCLF(ctx, clf)
	if err != nil {
		return trace.Wrap(err)
	}

	certBot, err := NewCertBot(ctx, cfg)
	if err != nil {
		return trace.Wrap(err)
	}

	return certBot.Run(ctx)
}

func ParseCLF(ctx context.Context, clf CommandLineFlags) (Config, error) {
	addrs, err := utils.ParseAddrs(clf.AuthServerAddr)
	if err != nil {
		return Config{}, trace.BadParameter("error parsing auth server address(es): %v", err)
	}

	certs, err := parseCertTuples(ctx, clf.Certs)
	if err != nil {
		return Config{}, trace.BadParameter("error parsing certs: %v", err)
	}

	return Config{
		AuthToken:       clf.AuthToken,
		Addrs:           addrs,
		CertStoreParams: certs,
	}, nil
}

func parseCertTuples(ctx context.Context, tuples []string) ([]CertStoreParams, error) {
	params := make([]CertStoreParams, len(tuples))
	for i, tuple := range tuples {
		var err error
		if params[i], err = parseCertTuple(ctx, tuple); err != nil {
			return nil, trace.Wrap(err)
		}
	}
	return params, nil
}

func parseCertTuple(ctx context.Context, tuple string) (CertStoreParams, error) {
	split := strings.Split(tuple, ",")
	if len(split) < 2 {
		return CertStoreParams{}, trace.BadParameter("format and directory must be provided in cert tuple")
	}

	params := CertStoreParams{
		format: identityfile.Format(split[0]),
		path:   split[1],
	}

	if len(split) >= 3 {
		args := strings.Split(strings.Join(split[3:], ","), " ")
		cmd := exec.Command(args[0], args[1:]...)
		params.reload = cmd.Run
	}

	return params, nil
}
