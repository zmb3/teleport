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

package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"golang.org/x/sync/errgroup"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/services"
)

const (
	concurrencyLimit = 5
)

func (s *Server) startKubeWatchers() error {
	if len(s.kubeFetchers) == 0 {
		return nil
	}
	var (
		kubeResources types.ResourcesWithLabels
		mu            sync.Mutex
	)

	watcher, err := services.NewReconciler(
		services.ReconcilerConfig{
			Matcher: func(_ types.ResourceWithLabels) bool { return true },
			GetCurrentResources: func() types.ResourcesWithLabelsMap {
				kcs, err := s.AccessPoint.GetKubernetesClusters(s.ctx)
				if err != nil {
					s.Log.WithError(err).Warn("Unable to get Kubernetes clusters from cache.")
					return nil
				}

				// filter only discover clusters.
				var kubeClusters types.KubeClusters
				for _, kc := range kcs {
					if kc.Origin() != types.OriginCloud {
						continue
					}
					kubeClusters = append(kubeClusters, kc)
				}

				return kubeClusters.AsResources().ToMap()
			},
			GetNewResources: func() types.ResourcesWithLabelsMap {
				mu.Lock()
				defer mu.Unlock()
				return kubeResources.ToMap()
			},
			Log:      s.Log,
			OnCreate: s.onKubeCreate,
			OnUpdate: s.onKubeUpdate,
			OnDelete: s.onKubeDelete,
		},
	)
	if err != nil {
		return trace.Wrap(err)
	}

	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			newResources := s.fetchFetchersResources()
			mu.Lock()
			kubeResources = newResources
			mu.Unlock()

			if err := watcher.Reconcile(s.ctx); err != nil {
				s.Log.WithError(err).Warn("Unable to reconcile resources.")
			}

			select {
			case <-t.C:
			case <-s.ctx.Done():
				return
			}
		}
	}()
	return nil
}

func (s *Server) fetchFetchersResources() types.ResourcesWithLabels {
	var (
		newFetcherResources = make(types.ResourcesWithLabels, 0, 50)
		fetchersLock        sync.Mutex
		group, groupCtx     = errgroup.WithContext(s.ctx)
	)
	group.SetLimit(concurrencyLimit)
	for _, fetcher := range s.kubeFetchers {
		lFetcher := fetcher

		group.Go(func() error {
			resources, err := lFetcher.Get(groupCtx)
			if err != nil {
				s.Log.WithError(err).Warnf("Unable to fetch resources for %s at %s.", lFetcher.ResourceType(), lFetcher.Cloud())
				// never return the error otherwise it will impact other watchers.
				return nil
			}
			fetchersLock.Lock()
			newFetcherResources = append(newFetcherResources, resources...)
			fetchersLock.Unlock()
			return nil
		})
	}
	// error is discarded because we must run all fetchers until the end.
	_ = group.Wait()
	return newFetcherResources
}

func (s *Server) onKubeCreate(ctx context.Context, rwl types.ResourceWithLabels) error {
	kubeCluster, ok := rwl.(types.KubeCluster)
	if !ok {
		return trace.BadParameter("invalid type received; expected types.KubeCluster, received %T", kubeCluster)
	}
	s.Log.Debugf("Creating kube_cluster %s.", kubeCluster.GetName())
	return trace.Wrap(s.AccessPoint.CreateKubernetesCluster(ctx, kubeCluster))
}

func (s *Server) onKubeUpdate(ctx context.Context, rwl types.ResourceWithLabels) error {
	kubeCluster, ok := rwl.(types.KubeCluster)
	if !ok {
		return trace.BadParameter("invalid type received; expected types.KubeCluster, received %T", kubeCluster)
	}
	s.Log.Debugf("Updating kube_cluster %s.", kubeCluster.GetName())
	return trace.Wrap(s.AccessPoint.UpdateKubernetesCluster(ctx, kubeCluster))
}

func (s *Server) onKubeDelete(ctx context.Context, rwl types.ResourceWithLabels) error {
	kubeCluster, ok := rwl.(types.KubeCluster)
	if !ok {
		return trace.BadParameter("invalid type received; expected types.KubeCluster, received %T", kubeCluster)
	}
	s.Log.Debugf("Deleting kube_cluster %s.", kubeCluster.GetName())
	return trace.Wrap(s.AccessPoint.DeleteKubernetesCluster(ctx, kubeCluster.GetName()))
}
