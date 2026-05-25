// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/cmd/fetcher/app"
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/profile"
)

const fetcherPort = "8000"

// Usage: fetcher <shared volume path>
func main() {

	mgr := manager.New()
	defer mgr.Wait()

	logger := loggerfactory.GetLogger()

	ctx := signals.SetupSignalHandler()
	profile.ProfileIfEnabled(ctx, logger, mgr)

	err := app.Run(ctx, crd.NewClientGenerator(), logger, mgr, fetcherPort, fv1.PodInfoMount)
	if err != nil {
		logger.Error(err, "fetcher failed")
		os.Exit(1)
	}
}
