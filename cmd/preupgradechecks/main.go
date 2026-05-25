// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func main() {
	logger := loggerfactory.GetLogger()

	ctx := signals.SetupSignalHandler()

	preupgradeClient, err := makePreUpgradeTaskClient(crd.NewClientGenerator(), logger)
	if err != nil {
		logger.Error(err, "error creating a crd client, please retry helm upgrade")
		os.Exit(1)
	}

	crd := preupgradeClient.GetFunctionCRD(ctx)
	if crd == nil {
		logger.Info("nothing to do since CRDs are not present on the cluster")
		return
	}
	err = preupgradeClient.LatestSchemaApplied(ctx)
	if err != nil {
		logger.Error(err, "New CRDs are not applied")
		os.Exit(1)
	}
	err = preupgradeClient.VerifyFunctionSpecReferences(ctx)
	if err != nil {
		logger.Error(err, "Function spec references are not valid")
		os.Exit(1)
	}
}
