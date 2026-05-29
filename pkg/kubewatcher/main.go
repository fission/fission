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
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
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

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader runs the watch sync, so two replicas don't double-register
	// watches / double-fire functions. No-op when LEADER_ELECTION_ENABLED is
	// unset (single-replica default).
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-kubewatcher", logger)
	if err != nil {
		return err
	}
	if err := crMgr.Add(crmanager.LeaderRunnable(func(c context.Context) error {
		gm := manager.New()
		ws.Run(c, gm)
		<-c.Done()
		gm.Wait()
		return nil
	})); err != nil {
		return err
	}
	return crMgr.Start(ctx)
}
