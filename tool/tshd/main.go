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

package main

import (
	"context"
	"flag"
	"os"
	"syscall"

	"github.com/gravitational/teleport/lib/teleterm"

	"github.com/gravitational/trace"

	log "github.com/sirupsen/logrus"
)

var (
	logFormat          = flag.String("log_format", "", "Log format to use (json or text)")
	logLevel           = flag.String("log_level", "", "Log level to use")
	addr               = flag.String("addr", "unix://socket", "Bind address for the Terminal API server")
	homeDir            = flag.String("home_dir", "", "Directory to store Terminal Daemon data")
	insecureSkipVerify = flag.Bool("insecure", false, "Do not verify server's certificate and host name. Use only in test environments")
)

func main() {
	flag.Parse()
	configureLogging()

	if err := run(); err != nil {
		log.Fatal(trace.Wrap(err))
	}
}

func configureLogging() {
	switch *logFormat {
	case "": // OK, use defaults
		log.SetFormatter(&trace.TextFormatter{})
	case "json":
		log.SetFormatter(&trace.JSONFormatter{})
	case "text":
		log.SetFormatter(&trace.TextFormatter{})
	default:
		log.Warnf("Invalid log_format flag: %q", *logFormat)
	}
	if ll := *logLevel; ll != "" {
		switch level, err := log.ParseLevel(ll); {
		case err != nil:
			log.WithError(err).Warn("Invalid -log_level flag")
		default:
			log.SetLevel(level)
		}
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := teleterm.Start(ctx, teleterm.Config{
		HomeDir:            *homeDir,
		Addr:               *addr,
		InsecureSkipVerify: *insecureSkipVerify,
		ShutdownSignals:    []os.Signal{os.Interrupt, syscall.SIGTERM},
	})
	if err != nil {
		return trace.Wrap(err)
	}

	return nil
}
