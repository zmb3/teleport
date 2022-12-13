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

package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/go-redis/redis/v9"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv/db/common"
	"github.com/zmb3/teleport/lib/srv/db/common/role"
	"github.com/zmb3/teleport/lib/srv/db/redis/connection"
	"github.com/zmb3/teleport/lib/srv/db/redis/protocol"
)

// Commands with additional processing in Teleport when using cluster mode.
const (
	dbsizeCmd   = "dbsize"
	keysCmd     = "keys"
	mgetCmd     = "mget"
	flushallCmd = "flushall"
	flushdbCmd  = "flushdb"
	scriptCmd   = "script"
)

// Overridden subcommands for Redis SCRIPT command.
const (
	scriptLoadSubcmd   = "load"
	scriptExistsSubcmd = "exists"
	scriptFLushSubcmd  = "flush"
)

// List of unsupported commands in cluster mode.
const (
	aclCmd        = "acl"
	askingCmd     = "asking"
	clientCmd     = "client"
	clusterCmd    = "cluster"
	configCmd     = "config"
	debugCmd      = "debug"
	execCmd       = "exec"
	infoCmd       = "info"
	latencyCmd    = "latency"
	memoryCmd     = "memory"
	migrateCmd    = "migrate"
	moduleCmd     = "module"
	monitorCmd    = "monitor"
	multiCmd      = "multi"
	pfdebugCmd    = "pfdebug"
	pfselftestCmd = "pfselftest"
	psyncCmd      = "psync"
	readonlyCmd   = "readonly"
	readwriteCmd  = "readwrite"
	replconfCmd   = "replconf"
	replicaofCmd  = "replicaof"
	roleCmd       = "role"
	scanCmd       = "scan"
	shutdownCmd   = "shutdown"
	slaveofCmd    = "slaveof"
	slowlogCmd    = "slowlog"
	syncCmd       = "sync"
	timeCmd       = "time"
	waitCmd       = "wait"
	watchCmd      = "watch"
)

const (
	// aclWhoami is a subcommand of "acl" that requires special handling.
	aclWhoami = "whoami"
)

// clusterClient is a wrapper around redis.ClusterClient
type clusterClient struct {
	redis.ClusterClient
}

// newClient creates a new Redis client based on given ConnectionMode. If connection mode is not supported
// an error is returned.
func newClient(ctx context.Context, connectionOptions *connection.Options, tlsConfig *tls.Config, onConnect onClientConnectFunc) (redis.UniversalClient, error) {
	connectionAddr := net.JoinHostPort(connectionOptions.Address, connectionOptions.Port)
	// TODO(jakub): Investigate Redis Sentinel.
	switch connectionOptions.Mode {
	case connection.Standalone:
		return redis.NewClient(&redis.Options{
			Addr:      connectionAddr,
			TLSConfig: tlsConfig,
			OnConnect: onConnect,

			// Auth should be done by the `OnConnect` callback here. So disable
			// "automatic" auth by the client.
			DisableAuthOnConnect: true,
		}), nil
	case connection.Cluster:
		client := &clusterClient{
			ClusterClient: *redis.NewClusterClient(&redis.ClusterOptions{
				Addrs:     []string{connectionAddr},
				TLSConfig: tlsConfig,
				OnConnect: onConnect,
				NewClient: func(opt *redis.Options) *redis.Client {
					opt.DisableAuthOnConnect = true
					return redis.NewClient(opt)
				},
			}),
		}
		// Load cluster information.
		client.ReloadState(ctx)

		return client, nil
	default:
		// We've checked that while validating the config, but checking again can help with regression.
		return nil, trace.BadParameter("incorrect connection mode %s", connectionOptions.Mode)
	}
}

// onClientConnectFunc is a callback function that performs setups after Redis
// client makes a new connection.
type onClientConnectFunc func(context.Context, *redis.Conn) error

// authWithPasswordOnConnect returns an onClientConnectFunc that sends "auth"
// with provided username and password.
func authWithPasswordOnConnect(username, password string) onClientConnectFunc {
	return func(ctx context.Context, conn *redis.Conn) error {
		return authConnection(ctx, conn, username, password)
	}
}

// fetchUserPasswordOnConnect returns an onClientConnectFunc that fetches user
// password on the fly then uses it for "auth".
func fetchUserPasswordOnConnect(sessionCtx *common.Session, users common.Users, audit common.Audit) onClientConnectFunc {
	var auditOnce sync.Once
	return func(ctx context.Context, conn *redis.Conn) error {
		err := sessionCtx.Checker.CheckAccess(sessionCtx.Database,
			services.AccessMFAParams{Verified: true},
			role.DatabaseRoleMatchers(
				defaults.ProtocolRedis,
				sessionCtx.DatabaseUser,
				sessionCtx.DatabaseName,
			)...)
		if err != nil {
			return trace.Wrap(err)
		}

		username := sessionCtx.DatabaseUser
		password, err := users.GetPassword(ctx, sessionCtx.Database, username)
		if err != nil {
			return trace.AccessDenied("failed to get password for %v: %v.", username, err)
		}

		auditOnce.Do(func() {
			audit.OnQuery(ctx, sessionCtx, common.Query{Query: fmt.Sprintf("AUTH %s ******", username)})
		})
		return authConnection(ctx, conn, username, password)
	}
}

// authConnection is a helper function that sends "auth" command to provided
// Redis connection with provided username and password.
func authConnection(ctx context.Context, conn *redis.Conn, username, password string) error {
	// Copied from redis.baseClient.initConn.
	_, err := conn.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		if password != "" {
			if username != "" {
				pipe.AuthACL(ctx, username, password)
			} else {
				pipe.Auth(ctx, password)
			}
		}
		return nil
	})
	return trace.Wrap(err)
}

// Process add supports for additional cluster commands. Our Redis implementation passes most commands to
// go-redis `Process()` function which doesn't handel all Cluster commands like for ex. DBSIZE, FLUSHDB, etc.
// This function provides additional processing for those commands enabling more Redis commands in Cluster mode.
// Commands are override by a simple rule:
//   - If command work only on a single slot (one shard) without any modifications and returns a CROSSSLOT error if executed on
//     multiple keys it's send the Redis client as it is, and it's the client responsibility to make sure keys are in a single slot.
//   - If a command returns incorrect result in the Cluster mode (for ex. DBSIZE as it would return only size of one shard not whole cluster)
//     then it's handled by Teleport or blocked if reasonable processing is not possible.
//   - Otherwise a commands is sent to Redis without any modifications.
func (c *clusterClient) Process(ctx context.Context, inCmd redis.Cmder) error {
	cmd, ok := inCmd.(*redis.Cmd)
	if !ok {
		return trace.BadParameter("failed to cast Redis command type: %T", cmd)
	}

	switch cmdName := strings.ToLower(cmd.Name()); cmdName {
	case multiCmd, execCmd, watchCmd, scanCmd, askingCmd, clientCmd, clusterCmd, configCmd, debugCmd,
		infoCmd, latencyCmd, memoryCmd, migrateCmd, moduleCmd, monitorCmd, pfdebugCmd, pfselftestCmd,
		psyncCmd, readonlyCmd, readwriteCmd, replconfCmd, replicaofCmd, roleCmd, shutdownCmd, slaveofCmd,
		slowlogCmd, syncCmd, timeCmd, waitCmd:
		// block commands that return incorrect results in Cluster mode
		return protocol.ErrCmdNotSupported
	case aclCmd:
		// allows "acl whoami" which is a very useful command that works fine
		// in Cluster mode.
		if len(cmd.Args()) == 2 {
			if subcommand, ok := cmd.Args()[1].(string); ok && strings.ToLower(subcommand) == aclWhoami {
				return c.ClusterClient.Process(ctx, cmd)
			}
		}
		// block other "acl" commands.
		return protocol.ErrCmdNotSupported
	case dbsizeCmd:
		// use go-redis dbsize implementation. It returns size of all keys in the whole cluster instead of
		// just currently connected instance.
		res := c.DBSize(ctx)
		cmd.SetVal(res.Val())
		cmd.SetErr(res.Err())

		return nil
	case keysCmd:
		var resultsKeys []string
		var mtx sync.Mutex

		if err := c.ForEachMaster(ctx, func(ctx context.Context, client *redis.Client) error {
			scanCmd := redis.NewStringSliceCmd(ctx, cmd.Args()...)
			err := client.Process(ctx, scanCmd)
			if err != nil {
				return trace.Wrap(err)
			}

			keys, err := scanCmd.Result()
			if err != nil {
				return trace.Wrap(err)
			}

			mtx.Lock()
			resultsKeys = append(resultsKeys, keys...)
			mtx.Unlock()

			return nil
		}); err != nil {
			return trace.Wrap(err)
		}

		cmd.SetVal(resultsKeys)

		return nil
	case mgetCmd:
		if len(cmd.Args()) == 1 {
			return trace.BadParameter("wrong number of arguments for 'mget' command")
		}

		var resultsKeys []interface{}

		keys := cmd.Args()[1:]
		for _, key := range keys {
			k, ok := key.(string)
			if !ok {
				return trace.BadParameter("wrong key type, expected string, got %T", key)
			}

			result := c.Get(ctx, k)
			if result.Err() == redis.Nil {
				resultsKeys = append(resultsKeys, redis.Nil)
				continue
			}

			if result.Err() != nil {
				cmd.SetErr(result.Err())
				return trace.Wrap(result.Err())
			}

			resultsKeys = append(resultsKeys, result.Val())
		}

		cmd.SetVal(resultsKeys)

		return nil
	case flushallCmd, flushdbCmd:
		var mtx sync.Mutex

		if err := c.ForEachMaster(ctx, func(ctx context.Context, client *redis.Client) error {
			singleCmd := redis.NewCmd(ctx, cmd.Args()...)
			err := client.Process(ctx, singleCmd)
			if err != nil {
				return trace.Wrap(err)
			}

			mtx.Lock()
			defer mtx.Unlock()

			if cmd.Err() != nil {
				// If other call have already set error do not erase it.
				// It should be returned to the caller.
				return nil
			}

			cmd.SetVal(singleCmd.Val())
			cmd.SetErr(singleCmd.Err())

			return nil
		}); err != nil {
			return trace.Wrap(err)
		}

		return nil
	case scriptCmd:
		return c.handleScriptCmd(ctx, cmd)
	default:
		return c.ClusterClient.Process(ctx, cmd)
	}
}

// handleScriptCmd processes Redis SCRIPT command in Cluster mode.
func (c *clusterClient) handleScriptCmd(ctx context.Context, cmd *redis.Cmd) error {
	cmdArgs := cmd.Args()

	if len(cmdArgs) < 2 {
		return trace.BadParameter("wrong number of arguments for 'script' command")
	}

	args := make([]string, len(cmdArgs))

	for i := range cmdArgs {
		var ok bool
		args[i], ok = cmdArgs[i].(string)
		if !ok {
			return trace.BadParameter("wrong script subcommand type, expected string, got %T", cmdArgs[i])
		}
	}

	switch cmdSubName := strings.ToLower(args[1]); cmdSubName {
	case scriptExistsSubcmd:
		res := c.ScriptExists(ctx, args[2:]...)

		cmd.SetVal(res.Val())
		cmd.SetErr(res.Err())

		return nil
	case scriptLoadSubcmd:
		res := c.ScriptLoad(ctx, args[2])

		cmd.SetVal(res.Val())
		cmd.SetErr(res.Err())

		return nil

	case scriptFLushSubcmd:
		// TODO(jakule): ASYNC is ignored.
		res := c.ScriptFlush(ctx)

		cmd.SetVal(res.Val())
		cmd.SetErr(res.Err())

		return nil
	default:
		// SCRIPT KILL and SCRIPT DEBUG
		return protocol.ErrCmdNotSupported
	}
}
