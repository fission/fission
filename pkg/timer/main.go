// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/crmanager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, routerUrl string) error {
	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader schedules cron triggers, so two replicas don't double-fire
	// timers. No-op when LEADER_ELECTION_ENABLED is unset (single-replica
	// default). The TimeTrigger reconciler watches through the Manager's cache and
	// runs only on the elected leader.
	crMgr, err := crmanager.NewTriggerManager(ctx, clientGen, "fission-timer", logger)
	if err != nil {
		return err
	}
	r := &TimeTriggerReconciler{
		logger: logger.WithName("timetrigger_reconciler"),
		client: crMgr.GetClient(),
		timer:  MakeTimer(logger, routerUrl),
	}
	if err := controller.RegisterTenantScoped(crMgr, &fv1.TimeTrigger{}, r, "timetrigger"); err != nil {
		return fmt.Errorf("error registering timetrigger reconciler: %w", err)
	}
	return crMgr.Start(ctx)
}
