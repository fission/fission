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

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/go-logr/zapr"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(fv1.AddToScheme(scheme))
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, routerUrl string, enableLeaderElection bool, metricsAddr string) error {
	logger := loggerfactory.GetLogger()

	zaprLogger := zapr.NewLogger(logger)
	log.SetLogger(zaprLogger)

	logger.Info("setting up manager")
	config, err := clientGen.GetRestConfig()
	if err != nil {
		logger.Error("failed getting config", zap.Error(err))
	}

	mgrMetrics := metricsserver.Options{
		BindAddress: metricsAddr,
	}

	mgr, err := ctrl.NewManager(config, manager.Options{
		Scheme:           scheme,
		Cache:            getCacheOptions(),
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "timer",
		Metrics:          mgrMetrics,
	})
	if err != nil {
		logger.Error("unable to set up overall controller manager", zap.Error(err))
		return err
	}

	publisher := publisher.MakeWebhookPublisher(logger, routerUrl)

	logger.Info("Setting up controller")
	if err = (&reconcileTimer{
		client:    mgr.GetClient(),
		scheme:    mgr.GetScheme(),
		publisher: publisher,
		triggers:  make(map[types.UID]*timerTriggerWithCron),
	}).SetupWithManager(mgr); err != nil {
		logger.Error("unable to create controller", zap.String("controller", "timetrigger"), zap.Error(err))
		return err
	}

	logger.Info("starting manager")
	if err = mgr.Start(ctx); err != nil {
		logger.Error("problem running manager", zap.Error(err))
		return err
	}
	return nil
}
