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

package mongodb

import (
	"context"
	"crypto/tls"
	"net/url"
	"time"

	"github.com/gravitational/trace"
	"go.mongodb.org/mongo-driver/mongo/address"
	"go.mongodb.org/mongo-driver/mongo/description"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/x/mongo/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/auth"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"go.mongodb.org/mongo-driver/x/mongo/driver/ocsp"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"

	"github.com/zmb3/teleport/lib/srv/db/common"
)

// connect returns connection to a MongoDB server.
//
// When connecting to a replica set, returns connection to the server selected
// based on the read preference connection string option. This allows users to
// configure database access to always connect to a secondary for example.
func (e *Engine) connect(ctx context.Context, sessionCtx *common.Session) (driver.Connection, func(), error) {
	options, selector, err := e.getTopologyOptions(ctx, sessionCtx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	// Using driver's "topology" package allows to retain low-level control
	// over server connections (reading/writing wire messages) but at the
	// same time get access to logic such as picking a server to connect to
	// in a replica set.
	top, err := topology.New(options...)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	err = top.Connect()
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	server, err := top.SelectServer(ctx, selector)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	e.Log.Debugf("Cluster topology: %v, selected server %v.", top, server)
	conn, err := server.Connection(ctx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	closeFn := func() {
		if err := top.Disconnect(ctx); err != nil {
			e.Log.WithError(err).Warn("Failed to close topology")
		}
		if err := conn.Close(); err != nil {
			e.Log.WithError(err).Error("Failed to close server connection.")
		}
	}
	return conn, closeFn, nil
}

// getTopologyOptions constructs topology options for connecting to a MongoDB server.
func (e *Engine) getTopologyOptions(ctx context.Context, sessionCtx *common.Session) ([]topology.Option, description.ServerSelector, error) {
	connString, err := getConnectionString(sessionCtx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	selector, err := getServerSelector(connString)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	serverOptions, err := e.getServerOptions(ctx, sessionCtx)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	return []topology.Option{
		topology.WithConnString(func(cs connstring.ConnString) connstring.ConnString {
			return connString
		}),
		topology.WithServerSelectionTimeout(func(time.Duration) time.Duration {
			return common.DefaultMongoDBServerSelectionTimeout
		}),
		topology.WithServerOptions(func(so ...topology.ServerOption) []topology.ServerOption {
			return serverOptions
		}),
	}, selector, nil
}

// getServerOptions constructs server options for connecting to a MongoDB server.
func (e *Engine) getServerOptions(ctx context.Context, sessionCtx *common.Session) ([]topology.ServerOption, error) {
	connectionOptions, err := e.getConnectionOptions(ctx, sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return []topology.ServerOption{
		topology.WithConnectionOptions(func(opts ...topology.ConnectionOption) []topology.ConnectionOption {
			return connectionOptions
		}),
	}, nil
}

// getConnectionOptions constructs connection options for connecting to a MongoDB server.
func (e *Engine) getConnectionOptions(ctx context.Context, sessionCtx *common.Session) ([]topology.ConnectionOption, error) {
	tlsConfig, err := e.Auth.GetTLSConfig(ctx, sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	authenticator, err := auth.CreateAuthenticator(auth.MongoDBX509, &auth.Cred{
		// MongoDB uses full certificate Subject field as a username.
		Username: "CN=" + sessionCtx.DatabaseUser,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return []topology.ConnectionOption{
		topology.WithTLSConfig(func(*tls.Config) *tls.Config {
			return tlsConfig
		}),
		topology.WithOCSPCache(func(ocsp.Cache) ocsp.Cache {
			return ocsp.NewCache()
		}),
		topology.WithHandshaker(func(topology.Handshaker) topology.Handshaker {
			// Auth handshaker will authenticate the client connection using
			// x509 mechanism as the database user specified above.
			return auth.Handshaker(
				// Wrap the driver's auth handshaker with our custom no-op
				// handshaker to prevent the driver from sending client metadata
				// to the server as a first message. Otherwise, the actual
				// client connecting to Teleport will get an error when they try
				// to send its own metadata since client metadata is immutable.
				&handshaker{},
				&auth.HandshakeOptions{Authenticator: authenticator})
		}),
	}, nil
}

// getConnectionString returns connection string for the server.
func getConnectionString(sessionCtx *common.Session) (connstring.ConnString, error) {
	uri, err := url.Parse(sessionCtx.Database.GetURI())
	if err != nil {
		return connstring.ConnString{}, trace.Wrap(err)
	}
	switch uri.Scheme {
	case connstring.SchemeMongoDB, connstring.SchemeMongoDBSRV:
		return connstring.ParseAndValidate(sessionCtx.Database.GetURI())
	}
	return connstring.ConnString{Hosts: []string{sessionCtx.Database.GetURI()}}, nil
}

// getServerSelector returns selector for picking the server to connect to,
// which is mostly useful when connecting to a MongoDB replica set.
//
// It uses readPreference connection flag. Defaults to "primary".
func getServerSelector(connString connstring.ConnString) (description.ServerSelector, error) {
	if connString.ReadPreference == "" {
		return description.ReadPrefSelector(readpref.Primary()), nil
	}
	readPrefMode, err := readpref.ModeFromString(connString.ReadPreference)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	readPref, err := readpref.New(readPrefMode)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return description.ReadPrefSelector(readPref), nil
}

// handshaker is Mongo driver no-op handshaker that doesn't send client
// metadata when connecting to server.
type handshaker struct{}

// GetHandshakeInformation overrides default auth handshaker's logic which
// would otherwise have sent client metadata request to the server which
// would break the actual client connecting to Teleport.
func (h *handshaker) GetHandshakeInformation(context.Context, address.Address, driver.Connection) (driver.HandshakeInformation, error) {
	return driver.HandshakeInformation{}, nil
}

// Finish handshake is no-op as all auth logic will be done by the driver's
// default auth handshaker.
func (h *handshaker) FinishHandshake(context.Context, driver.Connection) error {
	return nil
}
