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
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"github.com/go-logr/logr"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	//+kubebuilder:scaffold:imports
)

type WebhookInjector interface {
	SetupWebhookWithManager(mgr manager.Manager) error
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, options webhook.Options) (err error) {
	wLogger := logger.WithName("webhook")
	log.SetLogger(wLogger)

	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":8080"
	}
	if metricsAddr[0] != ':' {
		metricsAddr = fmt.Sprintf(":%s", metricsAddr)
	}
	mgrOpt := manager.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: webhook.NewServer(options),
		Logger:        wLogger,
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		wLogger.Error(err, "unable to get rest config")
		return err
	}
	// Setup a Manager
	mgr, err := manager.New(restConfig, mgrOpt)
	if err != nil {
		wLogger.Error(err, "unable to set up overall controller manager")
		return err
	}

	// Setup webhooks

	webhookInjectors := []WebhookInjector{
		&CanaryConfig{},
		&Environment{},
		&Package{},
		&Function{},
		&HTTPTrigger{},
		&MessageQueueTrigger{},
		&TimeTrigger{},
		&KubernetesWatchTrigger{},
	}

	for _, injector := range webhookInjectors {
		if err := injector.SetupWebhookWithManager(mgr); err != nil {
			wLogger.Error(err, "unable to create webhook")
			return err
		}
	}

	wLogger.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		wLogger.Error(err, "unable to run manager")
		return err
	}
	return nil
}
