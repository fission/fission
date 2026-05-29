// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils/leaderelection"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	poster := publisher.MakeWebhookPublisher(logger, routerUrl)
	kubeWatch := MakeKubeWatcher(ctx, logger, fissionClient, kubeClient, poster)
	ws, err := MakeWatchSync(ctx, logger, fissionClient, kubeWatch)
	if err != nil {
		return fmt.Errorf("error making watch sync: %w", err)
	}

	// Active-passive HA: only the elected leader runs the watch sync, so two
	// replicas don't double-register watches / double-fire functions. No-op
	// when LEADER_ELECTION_ENABLED is unset (single-replica default).
	elector, runCtx, err := leaderelection.FromEnv(ctx, kubeClient, "fission-kubewatcher", logger)
	if err != nil {
		return err
	}
	mgr.Add(ctx, func(context.Context) { elector.Run(runCtx) })
	mgr.Add(runCtx, elector.Gated(func(c context.Context) { ws.Run(c, mgr) }))

	return nil
}
