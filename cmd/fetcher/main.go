// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/cmd/fetcher/app"
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/profile"

	"github.com/fission/fission/pkg/svcinfo"

	"strconv"
)

var fetcherPort = strconv.Itoa(svcinfo.PortFetcher)

// Usage: fetcher <shared volume path>
func main() {

	mgr := &errgroup.Group{}
	defer func() { _ = mgr.Wait() }()

	logger := loggerfactory.GetLogger()

	ctx := signals.SetupSignalHandler()
	profile.ProfileIfEnabled(ctx, logger, mgr)

	err := app.Run(ctx, crd.NewClientGenerator(), logger, mgr, fetcherPort, fv1.PodInfoMount)
	if err != nil {
		logger.Error(err, "fetcher failed")
		os.Exit(1)
	}
}
