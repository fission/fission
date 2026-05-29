// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	timerSync, err := MakeTimerSync(ctx, logger, fissionClient, MakeTimer(logger, routerUrl))
	if err != nil {
		return fmt.Errorf("error making timer sync: %w", err)
	}

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader schedules cron triggers, so two replicas don't double-fire
	// timers. No-op when LEADER_ELECTION_ENABLED is unset (single-replica
	// default).
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-timer", logger)
	if err != nil {
		return err
	}
	if err := crMgr.Add(crmanager.LeaderRunnable(func(c context.Context) error {
		gm := manager.New()
		timerSync.Run(c, gm)
		<-c.Done()
		gm.Wait()
		return nil
	})); err != nil {
		return err
	}
	return crMgr.Start(ctx)
}
