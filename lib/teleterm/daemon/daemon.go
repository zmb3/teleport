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

package daemon

import (
	"context"
	"net"

	"github.com/gravitational/teleport/api/profile"
	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/teleterm/api/uri"

	"github.com/gravitational/trace"

	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
)

// Config is the cluster service config
type Config struct {
	// Dir is the directory to store cluster profiles
	Dir string
	// Clock is a clock for time-related operations
	Clock clockwork.Clock
	// InsecureSkipVerify is an option to skip HTTPS cert check
	InsecureSkipVerify bool
	// Log is a component logger
	Log *logrus.Entry
}

// CheckAndSetDefaults checks the configuration for its validity and sets default values if needed
func (c *Config) CheckAndSetDefaults() error {
	if c.Dir == "" {
		return trace.BadParameter("missing working directory")
	}

	if c.Clock == nil {
		c.Clock = clockwork.NewRealClock()
	}

	if c.Log == nil {
		c.Log = logrus.NewEntry(logrus.StandardLogger()).WithField(trace.Component, "daemon")
	}

	return nil
}

// Service is the cluster service
type Service struct {
	Config
	clusters []*Cluster
}

// Start creates and starts a Teleport Terminal service.
func New(cfg Config) (*Service, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Service{Config: cfg}, nil
}

// GetClusters returns a list of existing clusters
func (s *Service) GetClusters() []*Cluster {
	return s.clusters
}

// CreateCluster creates a new cluster
func (s *Service) CreateCluster(ctx context.Context, webProxyAddress string) (*Cluster, error) {
	profiles, err := profile.ListProfileNames(s.Dir)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clusterName := parseClusterName(webProxyAddress)
	for _, pname := range profiles {
		if pname == clusterName {
			return nil, trace.BadParameter("cluster %v already exists", clusterName)
		}
	}

	cluster, err := s.newCluster(s.Dir, webProxyAddress)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	s.clusters = append(s.clusters, cluster)
	return cluster, nil
}

// GetCluster returns a cluster by its name
func (s *Service) GetCluster(clusterURI string) (*Cluster, error) {
	for _, cluster := range s.clusters {
		if cluster.URI == clusterURI {
			return cluster, nil
		}
	}

	return nil, trace.NotFound("cluster is not found: %v", clusterURI)
}

// Init loads clusters from saved profiles
func (s *Service) Init() error {
	pfNames, err := profile.ListProfileNames(s.Dir)
	if err != nil {
		return trace.Wrap(err)
	}

	for _, name := range pfNames {
		cluster, err := s.newClusterFromProfile(name)
		if err != nil {
			return trace.Wrap(err)
		}

		s.clusters = append(s.clusters, cluster)
	}

	return nil
}

// CreateGateway creates a gateway
func (s *Service) CreateGateway(ctx context.Context, targetURI string, port string) (*Gateway, error) {
	clusterUri := uri.Cluster(uri.Parse(targetURI).Cluster())
	cluster, err := s.GetCluster(clusterUri.String())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	gateway, err := cluster.CreateGateway(ctx, targetURI, port, "")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return gateway, nil
}

// RemoveGateway removes cluster gateway
func (s *Service) RemoveGateway(ctx context.Context, gatewayURI string) error {
	clusterID := uri.Parse(gatewayURI).Cluster()
	clusters := s.GetClusters()
	for _, cluster := range clusters {
		if cluster.status.Name == clusterID {
			if err := cluster.RemoveGateway(ctx, gatewayURI); err != nil {
				return trace.Wrap(err)
			}

			return nil
		}
	}

	return trace.NotFound("cluster is not found: %v", clusterID)
}

// CloseConnections terminates all cluster open connections
func (s *Service) CloseConnections() {
	for _, cluster := range s.clusters {
		cluster.CloseConnections()
	}
}

// newClusterFromProfile creates new cluster from its profile
func (s *Service) newClusterFromProfile(name string) (*Cluster, error) {
	if name == "" {
		return nil, trace.BadParameter("name is missing")
	}

	cfg := client.MakeDefaultConfig()
	if err := cfg.LoadProfile(s.Dir, name); err != nil {
		return nil, trace.Wrap(err)
	}

	cfg.KeysDir = s.Dir
	cfg.HomePath = s.Dir
	cfg.InsecureSkipVerify = s.InsecureSkipVerify

	clt, err := client.NewClient(cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	status := &client.ProfileStatus{}

	// load profile status if key exists
	_, err = clt.LocalAgent().GetKey(name)
	if err != nil {
		s.Log.WithError(err).Infof("Unable to load the keys for cluster %v.", name)
	}

	if err == nil && cfg.Username != "" {
		status, err = client.StatusFromFile(s.Dir, name)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		if err := clt.LoadKeyForCluster(status.Cluster); err != nil {
			return nil, trace.Wrap(err)
		}
	}
	if err != nil && !trace.IsNotFound(err) {
		return nil, trace.Wrap(err)
	}

	return &Cluster{
		URI:           uri.Cluster(name).String(),
		Name:          name,
		clusterClient: clt,
		dir:           s.Dir,
		clock:         s.Clock,
		status:        *status,
	}, nil
}

// newCluster creates new cluster
func (s *Service) newCluster(dir, webProxyAddress string) (*Cluster, error) {
	if webProxyAddress == "" {
		return nil, trace.BadParameter("cluster address is missing")
	}

	if dir == "" {
		return nil, trace.BadParameter("cluster directory is missing")
	}

	cfg := client.MakeDefaultConfig()
	cfg.WebProxyAddr = webProxyAddress
	cfg.HomePath = s.Dir
	cfg.KeysDir = s.Dir
	cfg.InsecureSkipVerify = s.InsecureSkipVerify

	if err := cfg.SaveProfile(s.Dir, false); err != nil {
		return nil, trace.Wrap(err)
	}

	clusterClient, err := client.NewClient(cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clusterName := parseClusterName(webProxyAddress)

	return &Cluster{
		URI:           uri.Cluster(clusterName).String(),
		Name:          clusterName,
		dir:           s.Dir,
		clusterClient: clusterClient,
		clock:         s.Clock,
	}, nil
}

// parseClusterName gets cluster name from cluster web proxy address
// TODO(alex-kovoy): revisit storage layer to allow storing multiple profiles per hostname
func parseClusterName(webProxyAddress string) string {
	clusterName, _, err := net.SplitHostPort(webProxyAddress)
	if err != nil {
		clusterName = webProxyAddress
	}

	return clusterName
}
