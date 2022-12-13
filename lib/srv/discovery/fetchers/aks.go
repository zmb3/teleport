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

package fetchers

import (
	"context"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils"
	"github.com/zmb3/teleport/lib/cloud/azure"
	"github.com/zmb3/teleport/lib/services"
)

type aksFetcher struct {
	AKSFetcherConfig
}

// AKSFetcherConfig configures the AKS fetcher.
type AKSFetcherConfig struct {
	// Client is the Azure AKS client.
	Client azure.AKSClient
	// Regions are the regions where the clusters should be located.
	Regions []string
	// ResourceGroups are the Azure resource groups the clusters must belong to.
	ResourceGroups []string
	// FilterLabels are the filter criteria.
	FilterLabels types.Labels
	// Log is the logger.
	Log logrus.FieldLogger
}

// CheckAndSetDefaults validates and sets the defaults values.
func (c *AKSFetcherConfig) CheckAndSetDefaults() error {
	if c.Client == nil {
		return trace.BadParameter("missing Client field")
	}
	if len(c.Regions) == 0 {
		return trace.BadParameter("missing Regions field")
	}

	if len(c.FilterLabels) == 0 {
		return trace.BadParameter("missing FilterLabels field")
	}

	if c.Log == nil {
		c.Log = logrus.WithField(trace.Component, "fetcher:aks")
	}
	return nil
}

// NewAKSFetcher creates a new AKS fetcher configuration.
func NewAKSFetcher(cfg AKSFetcherConfig) (Fetcher, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &aksFetcher{cfg}, nil
}

func (a *aksFetcher) Get(ctx context.Context) (types.ResourcesWithLabels, error) {
	clusters, err := a.getAKSClusters(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var kubeClusters types.KubeClusters
	for _, cluster := range clusters {
		if !a.isRegionSupported(cluster.Location) {
			a.Log.Debugf("Cluster region %q does not match with allowed values.", cluster.Location)
			continue
		}
		if match, reason, err := services.MatchLabels(a.FilterLabels, cluster.Tags); err != nil {
			a.Log.WithError(err).Warn("Unable to match AKS cluster labels against match labels.")
			continue
		} else if !match {
			a.Log.Debugf("AKS cluster labels does not match the selector: %s.", reason)
			continue
		}

		kubeCluster, err := services.NewKubeClusterFromAzureAKS(cluster)
		if err != nil {
			a.Log.WithError(err).Warn("Unable to create Kubernetes cluster from azure.AKSCluster.")
			continue
		}
		kubeClusters = append(kubeClusters, kubeCluster)
	}
	return kubeClusters.AsResources(), nil
}

func (a *aksFetcher) getAKSClusters(ctx context.Context) ([]*azure.AKSCluster, error) {
	var (
		clusters []*azure.AKSCluster
		err      error
	)
	if len(a.ResourceGroups) == 1 && a.ResourceGroups[0] == types.Wildcard {
		clusters, err = a.Client.ListAll(ctx)
	} else {
		var errs []error
		for _, resourceGroup := range a.ResourceGroups {
			lClusters, lerr := a.Client.ListWithinGroup(ctx, resourceGroup)
			if lerr != nil {
				errs = append(errs, trace.Wrap(lerr))
				continue
			}
			clusters = append(clusters, lClusters...)
		}
		err = trace.NewAggregate(errs...)
	}
	return clusters, trace.Wrap(err)
}

func (a *aksFetcher) isRegionSupported(region string) bool {
	return utils.SliceContainsStr(a.Regions, types.Wildcard) || utils.SliceContainsStr(a.Regions, region)
}

func (a *aksFetcher) ResourceType() string {
	return types.KindKubernetesCluster
}

func (a *aksFetcher) Cloud() string {
	return types.CloudAzure
}
