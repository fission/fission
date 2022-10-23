/*
Copyright 2022.

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

package webhook

import (
	"context"
	"log"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"go.uber.org/zap"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	//+kubebuilder:scaffold:imports
)

// var (
// 	scheme   = runtime.NewScheme()
// 	setupLog = ctrl.Log.WithName("setup")
// )

// func init() {
// 	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

// 	//+kubebuilder:scaffold:scheme
// }

func Start(ctx context.Context, logger *zap.Logger, port int) (err error) {

	wLogger := logger.Named("webhook")
	// var params HookParamters
	// var metricsAddr string
	// var enableLeaderElection bool
	// var probeAddr string
	// flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	// flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	// flag.BoolVar(&enableLeaderElection, "leader-elect", false,
	// 	"Enable leader election for controller manager. "+
	// 		"Enabling this will ensure there is only one active controller manager.")
	// opts := optzap.Options{
	// 	Development: true,
	// }
	// opts.BindFlags(flag.CommandLine)
	// flag.Parse()

	// Setup a Manager
	log.Println("setting up manager")
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Scheme: scheme.Scheme,
		// MetricsBindAddress: metricsAddr,
		Port: port, // TODO: implement this
		// HealthProbeBindAddress: probeAddr,
		// LeaderElection:         enableLeaderElection,
		// LeaderElectionID:       "1e55c7b8.fission.io",
	})
	if err != nil {
		wLogger.Error("unable to set up overall controller manager", zap.Error(err))
		return err
	}
	log.Println("setting up manager done")
	something := &v1.CanaryConfig{}

	if err = something.SetupWebhookWithManager(mgr); err != nil {
		log.Println("Error found: ", err)
		wLogger.Error("unable to create webhook CanaryConfig", zap.Error(err))
		return err
	}

	if err = (&v1.Environment{}).SetupWebhookWithManager(mgr); err != nil {
		wLogger.Error("unable to create webhook Environment", zap.Error(err))
		return err
	}

	wLogger.Info("starting manager")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		wLogger.Error("unable to run manager", zap.Error(err))
		return err
	}
	return nil
}
