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

package service

import (
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/cloud"
	"github.com/zmb3/teleport/lib/srv/discovery"
)

func (process *TeleportProcess) shouldInitDiscovery() bool {
	return process.Config.Discovery.Enabled && !process.Config.Discovery.IsEmpty()
}

func (process *TeleportProcess) initDiscovery() {
	process.registerWithAuthServer(types.RoleDiscovery, DiscoveryIdentityEvent)
	process.RegisterCriticalFunc("discovery.init", process.initDiscoveryService)
}

func (process *TeleportProcess) initDiscoveryService() error {
	log := process.log.WithField(trace.Component, teleport.Component(
		teleport.ComponentDiscovery, process.id))

	conn, err := process.waitForConnector(DiscoveryIdentityEvent, log)
	if conn == nil {
		return trace.Wrap(err)
	}

	accessPoint, err := process.newLocalCacheForDiscovery(conn.Client,
		[]string{teleport.ComponentDiscovery})
	if err != nil {
		return trace.Wrap(err)
	}

	// asyncEmitter makes sure that sessions do not block
	// in case if connections are slow
	asyncEmitter, err := process.newAsyncEmitter(conn.Client)
	if err != nil {
		return trace.Wrap(err)
	}

	discoveryService, err := discovery.New(process.ExitContext(), &discovery.Config{
		Clients:       cloud.NewClients(),
		AWSMatchers:   process.Config.Discovery.AWSMatchers,
		AzureMatchers: process.Config.Discovery.AzureMatchers,
		GCPMatchers:   process.Config.Discovery.GCPMatchers,
		Emitter:       asyncEmitter,
		AccessPoint:   accessPoint,
		Log:           process.log,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	process.OnExit("discovery.stop", func(payload interface{}) {
		log.Info("Shutting down.")
		if discoveryService != nil {
			discoveryService.Stop()
		}
		if asyncEmitter != nil {
			warnOnErr(asyncEmitter.Close(), process.log)
		}
		warnOnErr(conn.Close(), log)
		log.Info("Exited.")
	})

	process.BroadcastEvent(Event{Name: DiscoveryReady, Payload: nil})

	if err := discoveryService.Start(); err != nil {
		return trace.Wrap(err)
	}
	log.Infof("Discovery service has successfully started")

	if err := discoveryService.Wait(); err != nil {
		return trace.Wrap(err)
	}

	return nil
}
