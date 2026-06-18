// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils/crmanager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, routerUrl string) error {
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}

	poster := publisher.MakeWebhookPublisher(logger, routerUrl)
	kubeWatch := MakeKubeWatcher(ctx, logger, kubeClient, poster)

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader registers watches, so two replicas don't double-register /
	// double-fire functions. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default). The reconciler watches through the Manager's cache
	// and runs only on the elected leader.
	crMgr, err := crmanager.NewTriggerManager(ctx, clientGen, "fission-kubewatcher", logger)
	if err != nil {
		return err
	}
	r := &KubernetesWatchTriggerReconciler{
		logger:      logger.WithName("kuberneteswatchtrigger_reconciler"),
		client:      crMgr.GetClient(),
		kubeWatcher: kubeWatch,
	}
	if err := controller.RegisterTenantScoped(crMgr, &fv1.KubernetesWatchTrigger{}, r, "kuberneteswatchtrigger"); err != nil {
		return fmt.Errorf("error registering kuberneteswatchtrigger reconciler: %w", err)
	}
	return crMgr.Start(ctx)
}
