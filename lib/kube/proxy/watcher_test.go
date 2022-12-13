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

package proxy

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/zmb3/teleport/api/types"
	testingkubemock "github.com/zmb3/teleport/lib/kube/proxy/testing/kube_server"
	"github.com/zmb3/teleport/lib/services"
)

// TestWatcher verifies that kubernetes agent properly detects and applies
// changes to kube_cluster resources.
func TestWatcher(t *testing.T) {
	kubeMock, err := testingkubemock.NewKubeAPIMock()
	require.NoError(t, err)
	t.Cleanup(func() { kubeMock.Close() })

	ctx := context.Background()

	reconcileCh := make(chan types.KubeClusters)
	// Setup kubernetes server that proxies one static kube cluster and
	// watches for kube_clusters with label group=a.
	testCtx := setupTestContext(ctx, t, testConfig{
		clusters: []kubeClusterConfig{{"kube0", kubeMock.URL}},
		resourceMatchers: []services.ResourceMatcher{
			{Labels: types.Labels{
				"group": []string{"a"},
			}},
		},
		onReconcile: func(kcs types.KubeClusters) {
			reconcileCh <- kcs
		},
	})

	require.Len(t, testCtx.kubeServer.fwd.kubeClusters(), 1)
	kube0 := testCtx.kubeServer.fwd.kubeClusters()[0]

	// Only kube0 should be registered initially.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Create kube_cluster with label group=a.
	kube1, err := makeDynamicKubeCluster(t, "kube1", kubeMock.URL, map[string]string{"group": "a"})
	require.NoError(t, err)
	err = testCtx.authServer.CreateKubernetesCluster(ctx, kube1)
	require.NoError(t, err)

	// It should be registered.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube1}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Try to update kube0 which is registered statically.
	kube0Updated, err := makeDynamicKubeCluster(t, "kube0", kubeMock.URL, map[string]string{"group": "a", types.OriginLabel: types.OriginDynamic})
	require.NoError(t, err)
	err = testCtx.authServer.CreateKubernetesCluster(ctx, kube0Updated)
	require.NoError(t, err)

	// It should not be registered, old kube0 should remain.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube1}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Create kube_cluster with label group=b.
	kube2, err := makeDynamicKubeCluster(t, "kube2", kubeMock.URL, map[string]string{"group": "b"})
	require.NoError(t, err)
	err = testCtx.authServer.CreateKubernetesCluster(ctx, kube2)
	require.NoError(t, err)

	// It shouldn't be registered.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube1}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Update kube2 labels so it matches.
	kube2.SetStaticLabels(map[string]string{"group": "a", types.OriginLabel: types.OriginDynamic})
	err = testCtx.authServer.UpdateKubernetesCluster(ctx, kube2)
	require.NoError(t, err)

	// Both should be registered now.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube1, kube2}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Update kube2 expiry so it gets re-registered.
	kube2.SetExpiry(time.Now().Add(1 * time.Hour))
	kube2.SetKubeconfig(newKubeConfig(t, "random", "https://api.cluster.com"))
	err = testCtx.authServer.UpdateKubernetesCluster(ctx, kube2)
	require.NoError(t, err)

	// kube2 should get updated.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube1, kube2}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
		// make sure credentials were updated as well.
		require.Equal(t, "api.cluster.com:443", testCtx.kubeServer.fwd.clusterDetails["kube2"].kubeCreds.getTargetAddr())
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Update kube1 labels so it doesn't match.
	kube1.SetStaticLabels(map[string]string{"group": "c", types.OriginLabel: types.OriginDynamic})
	err = testCtx.authServer.UpdateKubernetesCluster(ctx, kube1)
	require.NoError(t, err)

	// Only kube0 and kube2 should remain registered.
	select {
	case a := <-reconcileCh:
		sort.Sort(a)
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0, kube2}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}

	// Remove kube2.
	err = testCtx.authServer.DeleteKubernetesCluster(ctx, kube2.GetName())
	require.NoError(t, err)

	// Only static kube_cluster should remain.
	select {
	case a := <-reconcileCh:
		require.Empty(t, cmp.Diff(types.KubeClusters{kube0}, a,
			cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		))
	case <-time.After(time.Second):
		t.Fatal("Didn't receive reconcile event after 1s.")
	}
}

func makeDynamicKubeCluster(t *testing.T, name, url string, labels map[string]string) (*types.KubernetesClusterV3, error) {
	return makeKubeCluster(t, name, url, labels, map[string]string{
		types.OriginLabel: types.OriginDynamic,
	})
}

func makeKubeCluster(t *testing.T, name string, url string, labels map[string]string, additionalLabels map[string]string) (*types.KubernetesClusterV3, error) {
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range additionalLabels {
		labels[k] = v
	}
	return types.NewKubernetesClusterV3(types.Metadata{
		Name:   name,
		Labels: labels,
	}, types.KubernetesClusterSpecV3{
		Kubeconfig: newKubeConfig(t, name, url),
	})
}

func newKubeConfig(t *testing.T, name, url string) []byte {
	kubeConf := clientcmdapi.NewConfig()

	kubeConf.Clusters[name] = &clientcmdapi.Cluster{
		Server:                url,
		InsecureSkipTLSVerify: true,
	}
	kubeConf.AuthInfos[name] = &clientcmdapi.AuthInfo{}

	kubeConf.Contexts[name] = &clientcmdapi.Context{
		Cluster:  name,
		AuthInfo: name,
	}

	buf, err := clientcmd.Write(*kubeConf)
	require.NoError(t, err)
	return buf
}
