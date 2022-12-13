/*
Copyright 2022 Gravitational, Inc.

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

package sidecar

import (
	"context"
	"fmt"
	"time"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/auth"
	"github.com/zmb3/teleport/lib/auth/authclient"
	"github.com/zmb3/teleport/lib/tbot"
	"github.com/zmb3/teleport/lib/tbot/config"
	"github.com/zmb3/teleport/lib/tbot/identity"
	"github.com/zmb3/teleport/lib/tlsca"
	"github.com/zmb3/teleport/lib/utils"
)

const (
	DefaultCertificateTTL  = 2 * time.Hour
	DefaultRenewalInterval = 30 * time.Minute
)

// ClientAccessor returns a working teleport auth client when invoked.
// Client users should always call this function on a regular basis to ensure certs are always valid.
type ClientAccessor func(ctx context.Context) (auth.ClientI, error)

// Bot is a wrapper around an embedded tbot.
// It implements sigs.k8s.io/controller-runtime/manager.Runnable and
// sigs.k8s.io/controller-runtime/manager.LeaderElectionRunnable so it can be added to a controllerruntime.Manager.
type Bot struct {
	cfg        *config.BotConfig
	running    bool
	rootClient auth.ClientI
	opts       Options
}

func (b *Bot) initializeConfig() {
	// Initialize the memory stores. They contain identities renewed by the bot
	// We're reading certs directly from them
	rootMemoryStore := &config.DestinationMemory{}
	destMemoryStore := &config.DestinationMemory{}

	// Initialize tbot config
	b.cfg = &config.BotConfig{
		Onboarding: &config.OnboardingConfig{
			TokenValue: "",         // Field should be populated later, before running
			CAPins:     []string{}, // Field should be populated later, before running
			JoinMethod: types.JoinMethodToken,
		},
		Storage: &config.StorageConfig{
			DestinationMixin: config.DestinationMixin{
				Memory: rootMemoryStore,
			},
		},
		Destinations: []*config.DestinationConfig{
			{
				DestinationMixin: config.DestinationMixin{
					Memory: destMemoryStore,
				},
			},
		},
		Debug:           false,
		AuthServer:      b.opts.Addr,
		CertificateTTL:  DefaultCertificateTTL,
		RenewalInterval: DefaultRenewalInterval,
		Oneshot:         false,
	}

	// We do our own init because config's "CheckAndSetDefaults" is too linked with tbot logic and invokes
	// `addRequiredConfigs` on each Storage Destination
	rootMemoryStore.CheckAndSetDefaults()
	destMemoryStore.CheckAndSetDefaults()

	for _, artifact := range identity.GetArtifacts() {
		_ = destMemoryStore.Write(artifact.Key, []byte{})
		_ = rootMemoryStore.Write(artifact.Key, []byte{})
	}

}

func (b *Bot) GetClient(ctx context.Context) (auth.ClientI, error) {
	if !b.running {
		return nil, trace.Errorf("bot not started yet")
	}
	// If the bot has not joined the cluster yet or not generated client certs we bail out
	// This is either temporary or the bot is dead and the manager will shut down everything.
	if botCert, err := b.cfg.Storage.Memory.Read(identity.TLSCertKey); err != nil || len(botCert) == 0 {
		return nil, trace.Retry(err, "bot cert not yet present")
	}
	if cert, err := b.cfg.Destinations[0].Memory.Read(identity.TLSCertKey); err != nil || len(cert) == 0 {
		return nil, trace.Retry(err, "cert not yet present")
	}

	// Hack to be able to reuse LoadIdentity functions from tbot
	// LoadIdentity expects to have all the artifacts required for a bot
	// We loop over missing artifacts and are loading them from the bot storage to the destination
	for _, artifact := range identity.GetArtifacts() {
		if artifact.Kind == identity.KindBotInternal {
			value, err := b.cfg.Storage.Memory.Read(artifact.Key)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			if err := b.cfg.Destinations[0].Memory.Write(artifact.Key, value); err != nil {
				return nil, trace.Wrap(err)
			}

		}
	}

	id, err := identity.LoadIdentity(b.cfg.Destinations[0].Memory, identity.BotKinds()...)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	tlsConfig, err := id.TLSConfig(utils.DefaultCipherSuites())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	sshConfig, err := id.SSHClientConfig()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	authAddr, err := utils.ParseAddr(b.cfg.AuthServer)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	authClientConfig := &authclient.Config{
		TLS:         tlsConfig,
		SSH:         sshConfig,
		AuthServers: []utils.NetAddr{*authAddr},
		Log:         log.StandardLogger(),
	}

	c, err := authclient.Connect(ctx, authClientConfig)
	return c, trace.Wrap(err)
}

func (b *Bot) NeedLeaderElection() bool {
	return true
}

func (b *Bot) Start(ctx context.Context) error {
	token, err := createOrReplaceBot(ctx, b.opts, b.rootClient)
	if err != nil {
		return trace.Wrap(err)
	}
	log.Infof("Token generated %s", token)

	b.cfg.Onboarding.TokenValue = token

	// Getting the cluster CA Pins to be able to join regardless of the cert SANs.
	localCAResponse, err := b.rootClient.GetClusterCACert(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	caPins, err := tlsca.CalculatePins(localCAResponse.TLSCA)
	if err != nil {
		return trace.Wrap(err)
	}
	log.Infof("CA Pins recovered: %s", caPins)

	b.cfg.Onboarding.CAPins = caPins

	reloadChan := make(chan struct{})
	realBot := tbot.New(b.cfg, log.StandardLogger(), reloadChan)

	b.running = true
	log.Info("Running tbot")
	return trace.Wrap(realBot.Run(ctx))
}

// CreateAndBootstrapBot connects to teleport using a local auth connection, creates operator's role in teleport
// and creates tbot's configuration.
func CreateAndBootstrapBot(ctx context.Context, opts Options) (*Bot, *proto.Features, error) {
	if err := opts.CheckAndSetDefaults(); err != nil {
		return nil, nil, trace.Wrap(err)
	}

	// First we are creating a local auth client, like local tctl
	authClientConfig, err := createAuthClientConfig(opts)
	if err != nil {
		return nil, nil, trace.WrapWithMessage(err, "failed to create auth client config")
	}

	authClient, err := authclient.Connect(ctx, authClientConfig)
	if err != nil {
		return nil, nil, trace.WrapWithMessage(err, "failed to create auth client")
	}

	ping, err := authClient.Ping(ctx)
	if err != nil {
		return nil, nil, trace.WrapWithMessage(err, "failed to ping teleport")
	}

	// Then we create a role for the operator
	role, err := sidecarRole(opts.Role)
	if err != nil {
		return nil, nil, trace.WrapWithMessage(err, "failed to create role")
	}

	if err := authClient.UpsertRole(ctx, role); err != nil {
		return nil, nil, trace.WrapWithMessage(err, "failed to create operator's role")
	}
	log.Debug("Operator role created")

	bot := &Bot{
		running:    false,
		rootClient: authClient,
		opts:       opts,
	}

	bot.initializeConfig()
	return bot, ping.ServerFeatures, nil
}

// It is not currently possible to join back the cluster as an existing bot.
// See https://github.com/gravitational/teleport/issues/13091
func createOrReplaceBot(ctx context.Context, opts Options, authClient auth.ClientI) (string, error) {
	var token string
	botPresent, err := botExists(ctx, opts, authClient)
	if err != nil {
		return "", trace.Wrap(err)
	}
	if botPresent {
		if err := authClient.DeleteBot(ctx, opts.Name); err != nil {
			return "", trace.Wrap(err)
		}
		if err := authClient.DeleteRole(ctx, fmt.Sprintf("bot-%s", opts.Name)); err != nil {
			return "", trace.Wrap(err)
		}

	}
	response, err := authClient.CreateBot(ctx, &proto.CreateBotRequest{
		Name:  opts.Name,
		Roles: []string{opts.Role},
	})
	if err != nil {
		return "", trace.Wrap(err)
	}
	token = response.TokenID

	return token, nil
}

func botExists(ctx context.Context, opts Options, authClient auth.ClientI) (bool, error) {
	botUsers, err := authClient.GetBotUsers(ctx)
	if err != nil {
		return false, trace.Wrap(err)
	}
	for _, botUser := range botUsers {

		if botUser.GetName() == fmt.Sprintf("bot-%s", opts.Name) {
			return true, nil
		}
	}
	return false, nil
}
