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

//nolint:goimports // Ignored because of inline comments.
import (
	"flag"
	"os"
	"time"

	"github.com/gravitational/trace"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/utils/retryutils"
	resourcesv2 "github.com/zmb3/teleport/operator/apis/resources/v2"
	resourcesv3 "github.com/zmb3/teleport/operator/apis/resources/v3"
	resourcesv5 "github.com/zmb3/teleport/operator/apis/resources/v5"
	resourcescontrollers "github.com/zmb3/teleport/operator/controllers/resources"
	"github.com/zmb3/teleport/operator/sidecar"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(resourcesv5.AddToScheme(scheme))
	utilruntime.Must(resourcesv3.AddToScheme(scheme))
	utilruntime.Must(resourcesv2.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme

	utilruntime.Must(apiextv1.AddToScheme(scheme))
}

func main() {
	ctx := ctrl.SetupSignalHandler()

	var err error
	var metricsAddr string
	var probeAddr string
	var leaderElectionID string
	var syncPeriodString string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&leaderElectionID, "leader-election-id", "431e83f4.teleport.dev", "Leader Election Id to use")
	flag.StringVar(&syncPeriodString, "sync-period", "10h", "Operator sync period (format: https://pkg.go.dev/time#ParseDuration)")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	namespace, err := GetKubernetesNamespace()
	if err != nil {
		setupLog.Error(err, "unable to read the namespace, you can force a namespace by setting the POD_NAMESPACE env variable")
		os.Exit(1)
	}

	syncPeriod, err := time.ParseDuration(syncPeriodString)
	if err != nil {
		setupLog.Error(err, "invalid sync-period, please ensure the value is currectly parsed with https://pkg.go.dev/time#ParseDuration")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       leaderElectionID,
		Namespace:              namespace,
		SyncPeriod:             &syncPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var bot *sidecar.Bot
	var features *proto.Features

	retry, err := retryutils.NewLinear(retryutils.LinearConfig{
		Step: 100 * time.Millisecond,
		Max:  time.Second,
	})
	if err != nil {
		setupLog.Error(err, "failed to setup retry")
		os.Exit(1)
	}
	if err := retry.For(ctx, func() error {
		bot, features, err = sidecar.CreateAndBootstrapBot(ctx, sidecar.Options{})
		if err != nil {
			setupLog.Error(err, "failed to connect to teleport cluster, backing off")
		}
		return trace.Wrap(err)
	}); err != nil {
		setupLog.Error(err, "failed to setup teleport client")
		os.Exit(1)
	}
	setupLog.Info("connected to Teleport")

	if err = (&resourcescontrollers.RoleReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		TeleportClientAccessor: bot.GetClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TeleportRole")
		os.Exit(1)
	}

	if err = (&resourcescontrollers.UserReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		TeleportClientAccessor: bot.GetClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TeleportUser")
		os.Exit(1)
	}

	if err = resourcescontrollers.NewGithubConnectorReconciler(mgr.GetClient(), bot.GetClient).
		SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TeleportGithubConnector")
		os.Exit(1)
	}

	if features.OIDC {
		if err = resourcescontrollers.NewOIDCConnectorReconciler(mgr.GetClient(), bot.GetClient).
			SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "TeleportOIDCConnector")
			os.Exit(1)
		}
	} else {
		setupLog.Info("OIDC connectors are only available in Teleport Enterprise edition. TeleportOIDCConnector resources won't be reconciled")
	}

	if features.SAML {
		if err = resourcescontrollers.NewSAMLConnectorReconciler(mgr.GetClient(), bot.GetClient).
			SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "TeleportSAMLConnector")
			os.Exit(1)
		}
	} else {
		setupLog.Info("SAML connectors are only available in Teleport Enterprise edition. TeleportSAMLConnector resources won't be reconciled")
	}

	//+kubebuilder:scaffold:builder
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	if err := mgr.Add(bot); err != nil {
		setupLog.Error(err, "unable to setup bot ")
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
