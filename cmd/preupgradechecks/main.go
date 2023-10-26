/*
Copyright 2016 The Fission Authors.

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
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func main() {
	logger := loggerfactory.GetLogger()
	defer logger.Sync()

	crdBackedClient, err := makePreUpgradeTaskClient(crd.NewClientGenerator(), logger)
	if err != nil {
		logger.Fatal("error creating a crd client, please retry helm upgrade",
			zap.Error(err))
	}

	ctx := signals.SetupSignalHandler()
	crd := crdBackedClient.GetFunctionCRD(ctx)
	if crd == nil {
		logger.Info("nothing to do since CRDs are not present on the cluster")
		return
	}

	err = crdBackedClient.LatestSchemaApplied(ctx)
	if err != nil {
		logger.Fatal("New CRDs are not applied", zap.Error(err))
	}
	crdBackedClient.VerifyFunctionSpecReferences(ctx)
}
