// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/crd"
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

	timerSync, err := MakeTimerSync(ctx, logger, fissionClient, MakeTimer(logger, routerUrl))
	if err != nil {
		return fmt.Errorf("error making timer sync: %w", err)
	}

	// Active-passive HA: only the elected leader schedules cron triggers, so
	// two replicas don't double-fire timers. No-op when LEADER_ELECTION_ENABLED
	// is unset (single-replica default).
	elector, runCtx, err := leaderelection.FromEnv(ctx, kubeClient, "fission-timer", logger)
	if err != nil {
		return err
	}
	mgr.Add(ctx, func(context.Context) { elector.Run(runCtx) })
	mgr.Add(runCtx, elector.Gated(func(c context.Context) { timerSync.Run(c, mgr) }))
	return nil
}
