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

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib"
	"github.com/zmb3/teleport/lib/asciitable"
	kubeserver "github.com/zmb3/teleport/lib/kube/proxy/testing/kube_server"
	"github.com/zmb3/teleport/lib/service"
	"github.com/zmb3/teleport/lib/utils"
)

func TestListKube(t *testing.T) {
	lib.SetInsecureDevMode(true)
	t.Cleanup(func() { lib.SetInsecureDevMode(false) })
	ctx := context.Background()
	rootClusterName := "root-cluster"
	firstClusterName := "first-cluster"
	leaftClusterName := "leaf-cluster"
	serviceLabels := map[string]string{
		"label1": "val1",
		"ultra_long_label_for_teleport_kubernetes_service_list_kube_clusters_method": "ultra_long_label_value_for_teleport_kubernetes_service_list_kube_clusters_method",
	}
	formatedLabels := formatServiceLabels(serviceLabels)

	s := newTestSuite(t,
		withRootConfigFunc(func(cfg *service.Config) {
			cfg.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
			cfg.Kube.Enabled = true
			cfg.Kube.ListenAddr = utils.MustParseAddr(localListenerAddr())
			cfg.Kube.KubeconfigPath = newKubeConfigFile(t, rootClusterName, firstClusterName)
			cfg.Kube.StaticLabels = serviceLabels
		}),
		withLeafCluster(),
		withLeafConfigFunc(
			func(cfg *service.Config) {
				cfg.Auth.NetworkingConfig.SetProxyListenerMode(types.ProxyListenerMode_Multiplex)
				cfg.Kube.Enabled = true
				cfg.Kube.ListenAddr = utils.MustParseAddr(localListenerAddr())
				cfg.Kube.KubeconfigPath = newKubeConfigFile(t, leaftClusterName)
			},
		),
		withValidationFunc(func(s *suite) bool {
			rootClusters, err := s.root.GetAuthServer().GetKubernetesServers(ctx)
			require.NoError(t, err)
			return len(rootClusters) >= 2
		}),
		withNewTeleportOption(
			// Disables cloud auto-imported labels when running tests in cloud envs such as
			// Github Actions.
			// This is required otherwise Teleport will import cloud instance labels and use them
			// as labels in Kubernetes Service and this test would fail because the output
			// includes unexpected labels.
			service.WithIMDSClient(&fakeCloudMetadata{}),
		),
	)

	mustLoginSetEnv(t, s)

	tests := []struct {
		name      string
		args      []string
		wantTable func() string
	}{
		{
			name: "default mode with truncated table",
			args: nil,
			wantTable: func() string {
				table := asciitable.MakeTableWithTruncatedColumn(
					[]string{"Kube Cluster Name", "Labels", "Selected"},
					[][]string{{firstClusterName, formatedLabels, ""}, {rootClusterName, formatedLabels, ""}},
					"Labels")
				return table.AsBuffer().String()
			},
		},
		{
			name: "show complete list of labels",
			args: []string{"--verbose"},
			wantTable: func() string {
				table := asciitable.MakeTable(
					[]string{"Kube Cluster Name", "Labels", "Selected"},
					[]string{firstClusterName, formatedLabels, ""},
					[]string{rootClusterName, formatedLabels, ""})
				return table.AsBuffer().String()
			},
		},
		{
			name: "show headless table",
			args: []string{"--quiet"},
			wantTable: func() string {
				table := asciitable.MakeHeadlessTable(2)
				table.AddRow([]string{firstClusterName, formatedLabels, ""})
				table.AddRow([]string{rootClusterName, formatedLabels, ""})

				return table.AsBuffer().String()
			},
		},
		{
			name: "list all clusters including leaf clusters",
			args: []string{"--all"},
			wantTable: func() string {
				table := asciitable.MakeTable(
					[]string{"Proxy", "Cluster", "Kube Cluster Name", "Labels"},

					[]string{s.root.Config.Proxy.WebAddr.String(), "leaf1", leaftClusterName, ""},
					[]string{s.root.Config.Proxy.WebAddr.String(), "localhost", firstClusterName, formatedLabels},
					[]string{s.root.Config.Proxy.WebAddr.String(), "localhost", rootClusterName, formatedLabels},
				)
				return table.AsBuffer().String()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			captureStdout := new(bytes.Buffer)
			err := Run(
				context.Background(),
				append([]string{
					"--insecure",
					"kube",
					"ls",
				},
					tc.args...,
				),
				func(cf *CLIConf) error {
					cf.overrideStdout = io.MultiWriter(os.Stdout, captureStdout)
					return nil
				},
			)
			require.NoError(t, err)
			require.Contains(t, captureStdout.String(), tc.wantTable())
		})
	}
}

func newKubeConfigFile(t *testing.T, clusterNames ...string) string {
	tmpDir := t.TempDir()

	kubeConf := clientcmdapi.NewConfig()
	for _, name := range clusterNames {
		kubeConf.Clusters[name] = &clientcmdapi.Cluster{
			Server:                newKubeSelfSubjectServer(t),
			InsecureSkipTLSVerify: true,
		}
		kubeConf.AuthInfos[name] = &clientcmdapi.AuthInfo{}

		kubeConf.Contexts[name] = &clientcmdapi.Context{
			Cluster:  name,
			AuthInfo: name,
		}
	}
	kubeConfigLocation := filepath.Join(tmpDir, "kubeconfig")
	err := clientcmd.WriteToFile(*kubeConf, kubeConfigLocation)
	require.NoError(t, err)
	return kubeConfigLocation
}

func formatServiceLabels(labels map[string]string) string {
	labelSlice := make([]string, 0, len(labels))
	for key, value := range labels {
		labelSlice = append(labelSlice, fmt.Sprintf("%s=%s", key, value))
	}

	sort.Strings(labelSlice)
	return strings.Join(labelSlice, " ")
}

type fakeCloudMetadata struct{}

func (f *fakeCloudMetadata) IsAvailable(ctx context.Context) bool {
	return true
}

// GetTags gets all of the instance's tags.
func (f *fakeCloudMetadata) GetTags(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// GetHostname gets the hostname set by the cloud instance that Teleport
// should use, if any.
func (f *fakeCloudMetadata) GetHostname(ctx context.Context) (string, error) {
	return "hostname", nil
}

// GetType gets the cloud instance type.
func (f *fakeCloudMetadata) GetType() types.InstanceMetadataType {
	return types.InstanceMetadataTypeDisabled
}

// GetID gets the cloud instance ID.
func (f *fakeCloudMetadata) GetID(ctx context.Context) (string, error) {
	return "id", nil
}

func newKubeSelfSubjectServer(t *testing.T) string {
	srv, err := kubeserver.NewKubeAPIMock()
	require.NoError(t, err)
	t.Cleanup(func() { srv.Close() })

	return srv.URL
}
