/*
Copyright 2020 Gravitational, Inc.

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
	"github.com/zmb3/teleport/lib/utils"
)

// ListenerType identifies different registered listeners in
// process.registeredListeners.
type ListenerType string

var (
	ListenerAuth       = ListenerType(teleport.ComponentAuth)
	ListenerNodeSSH    = ListenerType(teleport.ComponentNode)
	ListenerProxySSH   = ListenerType(teleport.Component(teleport.ComponentProxy, "ssh"))
	ListenerDiagnostic = ListenerType(teleport.ComponentDiagnostic)
	ListenerProxyKube  = ListenerType(teleport.Component(teleport.ComponentProxy, "kube"))
	ListenerKube       = ListenerType(teleport.ComponentKube)
	// Proxy can use the same listener for tunnels and web interface
	// (multiplexing the requests).
	ListenerProxyTunnelAndWeb = ListenerType(teleport.Component(teleport.ComponentProxy, "tunnel", "web"))
	ListenerProxyWeb          = ListenerType(teleport.Component(teleport.ComponentProxy, "web"))
	ListenerProxyTunnel       = ListenerType(teleport.Component(teleport.ComponentProxy, "tunnel"))
	ListenerProxyMySQL        = ListenerType(teleport.Component(teleport.ComponentProxy, "mysql"))
	ListenerProxyPostgres     = ListenerType(teleport.Component(teleport.ComponentProxy, "postgres"))
	ListenerProxyMongo        = ListenerType(teleport.Component(teleport.ComponentProxy, "mongo"))
	ListenerProxyPeer         = ListenerType(teleport.Component(teleport.ComponentProxy, "peer"))
	ListenerMetrics           = ListenerType(teleport.ComponentMetrics)
	ListenerWindowsDesktop    = ListenerType(teleport.ComponentWindowsDesktop)
)

// AuthAddr returns auth server endpoint, if configured and started.
func (process *TeleportProcess) AuthAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerAuth)
}

// NodeSSHAddr returns the node SSH endpoint, if configured and started.
func (process *TeleportProcess) NodeSSHAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerNodeSSH)
}

// ProxySSHAddr returns the proxy SSH endpoint, if configured and started.
func (process *TeleportProcess) ProxySSHAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerProxySSH)
}

// DiagnosticAddr returns the diagnostic endpoint, if configured and started.
func (process *TeleportProcess) DiagnosticAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerDiagnostic)
}

// ProxyKubeAddr returns the proxy kubernetes endpoint, if configured and
// started.
func (process *TeleportProcess) ProxyKubeAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerProxyKube)
}

// ProxyWebAddr returns the proxy web interface endpoint, if configured and
// started.
func (process *TeleportProcess) ProxyWebAddr() (*utils.NetAddr, error) {
	addr, err := process.registeredListenerAddr(ListenerProxyTunnelAndWeb)
	if err == nil {
		return addr, nil
	}
	return process.registeredListenerAddr(ListenerProxyWeb)
}

// ProxyTunnelAddr returns the proxy reverse tunnel endpoint, if configured and
// started.
func (process *TeleportProcess) ProxyTunnelAddr() (*utils.NetAddr, error) {
	addr, err := process.registeredListenerAddr(ListenerProxyTunnelAndWeb)
	if err == nil {
		return addr, nil
	}
	return process.registeredListenerAddr(ListenerProxyTunnel)
}

// ProxyTunnelAddr returns the proxy peer address, if configured and started.
func (process *TeleportProcess) ProxyPeerAddr() (*utils.NetAddr, error) {
	return process.registeredListenerAddr(ListenerProxyPeer)
}

func (process *TeleportProcess) registeredListenerAddr(typ ListenerType) (*utils.NetAddr, error) {
	process.Lock()
	defer process.Unlock()

	var matched []registeredListener
	for _, l := range process.registeredListeners {
		if l.typ == typ {
			matched = append(matched, l)
		}
	}
	switch len(matched) {
	case 0:
		return nil, trace.NotFound("no registered address for type %q", typ)
	case 1:
		return utils.ParseAddr(matched[0].listener.Addr().String())
	default:
		return nil, trace.NotFound("multiple registered listeners found for type %q", typ)
	}
}
