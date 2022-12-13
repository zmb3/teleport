// Copyright 2021 Gravitational, Inc
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

package teleterm

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/zmb3/teleport/lib/teleterm/apiserver"
	"github.com/zmb3/teleport/lib/teleterm/clusters"
	"github.com/zmb3/teleport/lib/teleterm/daemon"
)

// Serve starts daemon service
func Serve(ctx context.Context, cfg Config) error {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}

	grpcCredentials, err := createGRPCCredentials(cfg.Addr, cfg.CertsDir)
	if err != nil {
		return trace.Wrap(err)
	}

	storage, err := clusters.NewStorage(clusters.Config{
		Dir:                cfg.HomeDir,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	daemonService, err := daemon.New(daemon.Config{
		Storage:                         storage,
		CreateTshdEventsClientCredsFunc: grpcCredentials.tshdEvents,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	apiServer, err := apiserver.New(apiserver.Config{
		HostAddr:        cfg.Addr,
		Daemon:          daemonService,
		TshdServerCreds: grpcCredentials.tshd,
		ListeningC:      cfg.ListeningC,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	serverAPIWait := make(chan error)
	go func() {
		err := apiServer.Serve()
		serverAPIWait <- err
	}()

	// Wait for shutdown signals
	go func() {
		c := make(chan os.Signal, len(cfg.ShutdownSignals))
		signal.Notify(c, cfg.ShutdownSignals...)
		select {
		case <-ctx.Done():
			log.Info("Context closed, stopping service.")
		case sig := <-c:
			log.Infof("Captured %s, stopping service.", sig)
		}
		daemonService.Stop()
		apiServer.Stop()
	}()

	errAPI := <-serverAPIWait

	if errAPI != nil {
		return trace.Wrap(errAPI, "shutting down due to API Server error")
	}

	return nil
}

type grpcCredentials struct {
	tshd       grpc.ServerOption
	tshdEvents daemon.CreateTshdEventsClientCredsFunc
}

func createGRPCCredentials(tshdServerAddress string, certsDir string) (*grpcCredentials, error) {
	shouldUseMTLS := strings.HasPrefix(tshdServerAddress, "tcp://")

	if !shouldUseMTLS {
		return &grpcCredentials{
			tshd: grpc.Creds(nil),
			tshdEvents: func() (grpc.DialOption, error) {
				return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
			},
		}, nil
	}

	rendererCertPath := filepath.Join(certsDir, rendererCertFileName)
	tshdCertPath := filepath.Join(certsDir, tshdCertFileName)
	tshdKeyPair, err := generateAndSaveCert(tshdCertPath)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// rendererCertPath will be read on an incoming client connection so we can assume that at this
	// point the renderer process has saved its public key under that path.
	tshdCreds, err := createServerCredentials(tshdKeyPair, rendererCertPath)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// To create client creds, we need to read the server cert. However, at this point we'd need to
	// wait for the Electron app to save the cert under rendererCertPath.
	//
	// Instead of waiting for it, we're going to capture the logic in a function that's going to be
	// called after the Electron app calls UpdateTshdEventsServerAddress of the Terminal service.
	// Since this calls the gRPC server hosted by tsh, we can assume that by this point the Electron
	// app has saved the cert to disk – without the cert, it wouldn't be able to call the tsh server.
	createTshdEventsClientCredsFunc := func() (grpc.DialOption, error) {
		creds, err := createClientCredentials(tshdKeyPair, rendererCertPath)
		return creds, trace.Wrap(err, "could not create tshd events client credentials")
	}

	return &grpcCredentials{
		tshd:       tshdCreds,
		tshdEvents: createTshdEventsClientCredsFunc,
	}, nil
}
