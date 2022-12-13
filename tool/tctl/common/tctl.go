/*
Copyright 2015-2019 Gravitational, Inc.

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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gravitational/kingpin"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/breaker"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/sshutils"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/authclient"
	"github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/config"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/tool/common"
	toolcommon "github.com/zmb3/teleport/tool/common"
)

const (
	searchHelp = `List of comma separated search keywords or phrases enclosed in quotations (e.g. --search=foo,bar,"some phrase")`
	queryHelp  = `Query by predicate language enclosed in single quotes. Supports ==, !=, &&, and || (e.g. --query='labels["key1"] == "value1" && labels["key2"] != "value2"')`
	labelHelp  = "List of comma separated labels to filter by labels (e.g. key1=value1,key2=value2)"
)

const (
	identityFileEnvVar = "TELEPORT_IDENTITY_FILE"
	authAddrEnvVar     = "TELEPORT_AUTH_SERVER"
)

// GlobalCLIFlags keeps the CLI flags that apply to all tctl commands
type GlobalCLIFlags struct {
	// Debug enables verbose logging mode to the console
	Debug bool
	// ConfigFile is the path to the Teleport configuration file
	ConfigFile string
	// ConfigString is the base64-encoded string with Teleport configuration
	ConfigString string
	// AuthServerAddr lists addresses of auth or proxy servers to connect to,
	AuthServerAddr []string
	// IdentityFilePath is the path to the identity file
	IdentityFilePath string
	// Insecure, when set, skips validation of server TLS certificate when
	// connecting through a proxy (specified in AuthServerAddr).
	Insecure bool
}

// CLICommand interface must be implemented by every CLI command
//
// This allows OSS and Enterprise Teleport editions to plug their own
// implementations of different CLI commands into the common execution
// framework
type CLICommand interface {
	// Initialize allows a caller-defined command to plug itself into CLI
	// argument parsing
	Initialize(*kingpin.Application, *service.Config)

	// TryRun is executed after the CLI parsing is done. The command must
	// determine if selectedCommand belongs to it and return match=true
	TryRun(ctx context.Context, selectedCommand string, c auth.ClientI) (match bool, err error)
}

// Run is the same as 'make'. It helps to share the code between different
// "distributions" like OSS or Enterprise
//
// distribution: name of the Teleport distribution
func Run(commands []CLICommand) {
	err := TryRun(commands, os.Args[1:])
	if err != nil {
		var exitError *toolcommon.ExitCodeError
		if errors.As(err, &exitError) {
			os.Exit(exitError.Code)
		}
		utils.FatalError(err)
	}
}

// TryRun is a helper function for Run to call - it runs a tctl command and returns an error.
// This is useful for testing tctl, because we can capture the returned error in tests.
func TryRun(commands []CLICommand, args []string) error {
	utils.InitLogger(utils.LoggingForCLI, log.WarnLevel)

	// app is the command line parser
	app := utils.InitCLIParser("tctl", GlobalHelpString)

	// cfg (teleport auth server configuration) is going to be shared by all
	// commands
	cfg := service.MakeDefaultConfig()
	cfg.CircuitBreakerConfig = breaker.NoopBreakerConfig()

	// each command will add itself to the CLI parser:
	for i := range commands {
		commands[i].Initialize(app, cfg)
	}

	var ccf GlobalCLIFlags

	// If the config file path is being overridden by environment variable, set that.
	// If not, check whether the default config file path exists and set that if so.
	// This preserves tctl's default behavior for backwards compatibility.
	configFileEnvar, isSet := os.LookupEnv(defaults.ConfigFileEnvar)
	if isSet {
		ccf.ConfigFile = configFileEnvar
	} else {
		if utils.FileExists(defaults.ConfigFilePath) {
			ccf.ConfigFile = defaults.ConfigFilePath
		}
	}

	// these global flags apply to all commands
	app.Flag("debug", "Enable verbose logging to stderr").
		Short('d').
		BoolVar(&ccf.Debug)
	app.Flag("config", fmt.Sprintf("Path to a configuration file [%v]. Can also be set via the %v environment variable.", defaults.ConfigFilePath, defaults.ConfigFileEnvar)).
		Short('c').
		ExistingFileVar(&ccf.ConfigFile)
	app.Flag("config-string",
		"Base64 encoded configuration string").Hidden().Envar(defaults.ConfigEnvar).StringVar(&ccf.ConfigString)
	app.Flag("auth-server",
		fmt.Sprintf("Attempts to connect to specific auth/proxy address(es) instead of local auth [%v]", defaults.AuthConnectAddr().Addr)).
		Envar(authAddrEnvVar).
		StringsVar(&ccf.AuthServerAddr)
	app.Flag("identity",
		"Path to an identity file. Must be provided to make remote connections to auth. An identity file can be exported with 'tctl auth sign'").
		Short('i').
		Envar(identityFileEnvVar).
		StringVar(&ccf.IdentityFilePath)
	app.Flag("insecure", "When specifying a proxy address in --auth-server, do not verify its TLS certificate. Danger: any data you send can be intercepted or modified by an attacker.").
		BoolVar(&ccf.Insecure)

	// "version" command is always available:
	ver := app.Command("version", "Print the version of your tctl binary")
	app.HelpFlag.Short('h')

	// parse CLI commands+flags:
	utils.UpdateAppUsageTemplate(app, args)
	selectedCmd, err := app.Parse(args)
	if err != nil {
		app.Usage(args)
		return trace.Wrap(err)
	}

	// "version" command?
	if selectedCmd == ver.FullCommand() {
		utils.PrintVersion()
		return nil
	}

	cfg.TeleportHome = os.Getenv(types.HomeEnvVar)
	if cfg.TeleportHome != "" {
		cfg.TeleportHome = filepath.Clean(cfg.TeleportHome)
	}

	// configure all commands with Teleport configuration (they share 'cfg')
	clientConfig, err := ApplyConfig(&ccf, cfg)
	if err != nil {
		return trace.Wrap(err)
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGTERM, syscall.SIGINT,
	)
	defer cancel()

	client, err := authclient.Connect(ctx, clientConfig)
	if err != nil {
		if utils.IsUntrustedCertErr(err) {
			err = trace.WrapWithMessage(err, utils.SelfSignedCertsMsg)
		}
		utils.Consolef(os.Stderr, log.WithField(trace.Component, teleport.ComponentClient), teleport.ComponentClient,
			"Cannot connect to the auth server: %v.\nIs the auth server running on %q?",
			err, cfg.AuthServerAddresses()[0].Addr)
		return trace.NewAggregate(&toolcommon.ExitCodeError{Code: 1}, err)
	}

	// execute whatever is selected:
	var match bool
	for _, c := range commands {
		match, err = c.TryRun(ctx, selectedCmd, client)
		if err != nil {
			return trace.Wrap(err)
		}
		if match {
			break
		}
	}

	if err := common.ShowClusterAlerts(ctx, client, os.Stderr, nil,
		types.AlertSeverity_HIGH, types.AlertSeverity_HIGH); err != nil {
		log.WithError(err).Warn("Failed to display cluster alerts.")
	}

	return nil
}

// ApplyConfig takes configuration values from the config file and applies them
// to 'service.Config' object.
//
// The returned authclient.Config has the credentials needed to dial the auth
// server.
func ApplyConfig(ccf *GlobalCLIFlags, cfg *service.Config) (*authclient.Config, error) {
	// --debug flag
	if ccf.Debug {
		cfg.Debug = ccf.Debug
		utils.InitLogger(utils.LoggingForCLI, log.DebugLevel)
		log.Debugf("Debug logging has been enabled.")
	}
	cfg.Log = log.StandardLogger()

	if cfg.Version == "" {
		cfg.Version = defaults.TeleportConfigVersionV1
	}

	// If the config file path provided is not a blank string, load the file and apply its values
	var fileConf *config.FileConfig
	var err error
	if ccf.ConfigFile != "" {
		fileConf, err = config.ReadConfigFile(ccf.ConfigFile)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// if configuration is passed as an environment variable,
	// try to decode it and override the config file
	if ccf.ConfigString != "" {
		fileConf, err = config.ReadFromString(ccf.ConfigString)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	if err = config.ApplyFileConfig(fileConf, cfg); err != nil {
		return nil, trace.Wrap(err)
	}

	// --auth-server flag(-s)
	if len(ccf.AuthServerAddr) != 0 {
		authServers, err := utils.ParseAddrs(ccf.AuthServerAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		// Overwrite any existing configuration with flag values.
		if err := cfg.SetAuthServerAddresses(authServers); err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// Config file should take precedence, if available.
	if fileConf == nil && ccf.IdentityFilePath == "" {
		// No config file or identity file.
		// Try the extension loader.
		log.Debug("No config file or identity file, loading auth config via extension.")
		authConfig, err := LoadConfigFromProfile(ccf, cfg)
		if err == nil {
			return authConfig, nil
		}
		if !trace.IsNotFound(err) {
			return nil, trace.Wrap(err)
		}
	}

	// If auth server is not provided on the command line or in file
	// configuration, use the default.
	if len(cfg.AuthServerAddresses()) == 0 {
		authServers, err := utils.ParseAddrs([]string{defaults.AuthConnectAddr().Addr})
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if err := cfg.SetAuthServerAddresses(authServers); err != nil {
			return nil, trace.Wrap(err)
		}
	}

	authConfig := new(authclient.Config)
	// --identity flag
	if ccf.IdentityFilePath != "" {
		key, err := client.KeyFromIdentityFile(ccf.IdentityFilePath)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		clusterName, err := key.RootClusterName()
		if err != nil {
			return nil, trace.Wrap(err)
		}

		authConfig.TLS, err = key.TeleportClientTLSConfig(cfg.CipherSuites, []string{clusterName})
		if err != nil {
			return nil, trace.Wrap(err)
		}

		authConfig.SSH, err = key.ProxyClientSSHConfig(&sshTrustedHostKeyWrapper{key}, clusterName)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		// read the host UUID only in case the identity was not provided,
		// because it will be used for reading local auth server identity
		cfg.HostUUID, err = utils.ReadHostUUID(cfg.DataDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, trace.Wrap(err, "Could not load Teleport host UUID file at %s. "+
					"Please make sure that Teleport is up and running prior to using tctl.",
					filepath.Join(cfg.DataDir, utils.HostUUIDFile))
			} else if errors.Is(err, fs.ErrPermission) {
				return nil, trace.Wrap(err, "Teleport does not have permission to read Teleport host UUID file at %s. "+
					"Ensure that you are running as a user with appropriate permissions.",
					filepath.Join(cfg.DataDir, utils.HostUUIDFile))
			}
			return nil, trace.Wrap(err)
		}
		identity, err := auth.ReadLocalIdentity(filepath.Join(cfg.DataDir, teleport.ComponentProcess), auth.IdentityID{Role: types.RoleAdmin, HostUUID: cfg.HostUUID})
		if err != nil {
			// The "admin" identity is not present? This means the tctl is running
			// NOT on the auth server
			if trace.IsNotFound(err) {
				return nil, trace.AccessDenied("tctl must be either used on the auth server or provided with the identity file via --identity flag")
			}
			return nil, trace.Wrap(err)
		}
		authConfig.TLS, err = identity.TLSConfig(cfg.CipherSuites)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	authConfig.TLS.InsecureSkipVerify = ccf.Insecure
	authConfig.AuthServers = cfg.AuthServerAddresses()
	authConfig.Log = cfg.Log

	return authConfig, nil
}

// sshTrustedHostKeyWrapper wraps a client Key allowing to call GetKnownHostKeys function for particular hostname.
type sshTrustedHostKeyWrapper struct {
	*client.Key
}

// GetKnownHostKeys returns know trusted key for a particular hostname.
func (m *sshTrustedHostKeyWrapper) GetKnownHostKeys(hostname string) ([]ssh.PublicKey, error) {
	ca, err := m.Key.SSHCAsForClusters([]string{hostname})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	trustedKeys, err := sshutils.ParseKnownHosts(ca)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return trustedKeys, nil
}

// LoadConfigFromProfile applies config from ~/.tsh/ profile if it's present
func LoadConfigFromProfile(ccf *GlobalCLIFlags, cfg *service.Config) (*authclient.Config, error) {
	if ccf.IdentityFilePath != "" {
		return nil, trace.NotFound("identity has been supplied, skip loading the config")
	}

	proxyAddr := ""
	if len(ccf.AuthServerAddr) != 0 {
		proxyAddr = ccf.AuthServerAddr[0]
	}
	profile, _, err := client.Status(cfg.TeleportHome, proxyAddr)
	if err != nil {
		if !trace.IsNotFound(err) {
			return nil, trace.Wrap(err)
		}
	}
	// client is already logged in using tsh login and profile is not expired
	if profile == nil {
		return nil, trace.NotFound("profile is not found")
	}
	if profile.IsExpired(clockwork.NewRealClock()) {
		return nil, trace.BadParameter("your credentials have expired, please login using `tsh login`")
	}

	log.WithFields(log.Fields{"proxy": profile.ProxyURL.String(), "user": profile.Username}).Debugf("Found active profile.")

	c := client.MakeDefaultConfig()
	if err := c.LoadProfile(cfg.TeleportHome, proxyAddr); err != nil {
		return nil, trace.Wrap(err)
	}
	keyStore, err := client.NewFSLocalKeyStore(c.KeysDir)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	webProxyHost, _ := c.WebProxyHostPort()
	idx := client.KeyIndex{ProxyHost: webProxyHost, Username: c.Username, ClusterName: profile.Cluster}
	key, err := keyStore.GetKey(idx, client.WithSSHCerts{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Auth config can be created only using a key associated with the root cluster.
	rootCluster, err := key.RootClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if profile.Cluster != rootCluster {
		return nil, trace.BadParameter("your credentials are for cluster %q, please run `tsh login %q` to log in to the root cluster", profile.Cluster, rootCluster)
	}

	authConfig := &authclient.Config{}
	authConfig.TLS, err = key.TeleportClientTLSConfig(cfg.CipherSuites, []string{rootCluster})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	authConfig.TLS.InsecureSkipVerify = ccf.Insecure
	authConfig.SSH, err = key.ProxyClientSSHConfig(keyStore, rootCluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// Do not override auth servers from command line
	if len(ccf.AuthServerAddr) == 0 {
		webProxyAddr, err := utils.ParseAddr(c.WebProxyAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		log.Debugf("Setting auth server to web proxy %v.", webProxyAddr)
		cfg.SetAuthServerAddress(*webProxyAddr)
	}
	authConfig.AuthServers = cfg.AuthServerAddresses()
	authConfig.Log = cfg.Log

	if c.TLSRoutingEnabled {
		cfg.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
	}

	return authConfig, nil
}
