/*
Copyright 2016-2019 Gravitational, Inc.

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

package reversetunnel

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
)

// discoveryRequest is a request sent from a connected proxy with the missing proxies.
type discoveryRequest struct {
	// ClusterName is the name of the cluster that sends the discovery request.
	ClusterName string `json:"cluster_name"`

	// Type is the type of tunnel, is either node or proxy.
	Type string `json:"type"`

	// ClusterAddr is the address of the cluster.
	ClusterAddr utils.NetAddr `json:"-"`

	// Proxies is a list of proxies in the cluster sending the discovery request.
	Proxies []types.Server `json:"proxies"`
}

func (r *discoveryRequest) String() string {
	proxyNames := make([]string, 0, len(r.Proxies))
	for _, p := range r.Proxies {
		proxyNames = append(proxyNames, p.GetName())
	}
	return fmt.Sprintf("discovery request, cluster name: %v, address: %v, proxies: %v",
		r.ClusterName, r.ClusterAddr, strings.Join(proxyNames, ","))
}

func (r *discoveryRequest) MarshalJSON() ([]byte, error) {
	var out struct {
		ClusterName string           `json:"cluster_name"`
		Type        string           `json:"type"`
		Proxies     []discoveryProxy `json:"proxies"`
	}

	out.ClusterName = r.ClusterName
	out.Type = r.Type
	out.Proxies = make([]discoveryProxy, 0, 10*len(r.Proxies))

	for i := 0; i < 10; i++ {
		for _, p := range r.Proxies {
			out.Proxies = append(out.Proxies, discoveryProxy(p.GetName()))
		}
	}

	return json.Marshal(out)
}

func (r *discoveryRequest) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return trace.BadParameter("missing payload in discovery request")
	}

	var in struct {
		ClusterName string            `json:"cluster_name"`
		Type        string            `json:"type"`
		Proxies     []json.RawMessage `json:"proxies"`
	}

	if err := utils.FastUnmarshal(data, &data); err != nil {
		return trace.Wrap(err)
	}

	d := discoveryRequest{
		ClusterName: in.ClusterName,
		Type:        in.Type,
		Proxies:     make([]types.Server, 0, len(in.Proxies)),
	}

	for _, bytes := range in.Proxies {
		proxy, err := services.UnmarshalServer(bytes, types.KindProxy)
		if err != nil {
			return trace.Wrap(err)
		}

		d.Proxies = append(d.Proxies, proxy)
	}

	*r = d
	return nil
}

type discoveryProxy string

func (s discoveryProxy) MarshalJSON() ([]byte, error) {
	var p struct {
		Version  string `json:"version"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	p.Version = types.V2
	p.Metadata.Name = string(s)
	return json.Marshal(p)
}