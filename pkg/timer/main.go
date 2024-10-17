/*
Copyright 2017 The Fission Authors.

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

package timer

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(fv1.AddToScheme(scheme))
	log.SetLogger(zap.New())
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, routerUrl string) error {
	logger := log.Log.WithName("timer")
	logger.Info("setting up manager")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	config, err := clientGen.GetRestConfig()
	if err != nil {
		logger.Error(err, "failed getting config")
	}

	mgr, err := ctrl.NewManager(config, manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		logger.Error(err, "unable to set up overall controller manager")
		cancel()
	}

	publisher := publisher.MakeWebhookPublisher(zap.NewRaw(), routerUrl)

	logger.Info("Setting up controller")
	if err = (&reconcileTimer{
		client:    mgr.GetClient(),
		scheme:    mgr.GetScheme(),
		publisher: publisher,
		triggers:  make(map[types.UID]*timerTriggerWithCron),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "timer")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		cancel()
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		cancel()
	}

	logger.Info("starting manager")
	err = mgr.Start(ctx)
	if err != nil {
		logger.Error(err, "problem running manager")
		cancel()
	}

	return nil
}
