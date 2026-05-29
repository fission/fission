// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/cmd/builder/app"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/profile"
)

// Usage: builder <shared volume path>
func main() {

	mgr := &errgroup.Group{}
	defer func() { _ = mgr.Wait() }()

	logger := loggerfactory.GetLogger()

	ctx := signals.SetupSignalHandler()
	profile.ProfileIfEnabled(ctx, logger, mgr)

	shareVolume := os.Args[1]
	if _, err := os.Stat(shareVolume); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(shareVolume, os.ModeDir|0700)
			if err != nil {
				logger.Error(err, "error creating directory: %s", "directory", shareVolume)

				os.Exit(1)
			}
		}
	}
	app.Run(ctx, logger, mgr, shareVolume)
}
