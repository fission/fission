// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/go-logr/logr"

	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/manager"
)

// ConfigureFeatures gets the feature config and configures the features that are enabled
func ConfigureFeatures(ctx context.Context, restConfig *rest.Config, logger logr.Logger, unitTestMode bool, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, _ manager.Interface) error {
	// set feature enabled to false if unitTestMode
	if unitTestMode {
		return nil
	}

	// get the featureConfig from config map mounted onto the file system
	featureConfig, err := config.GetFeatureConfig(logger)
	if err != nil {
		logger.Error(err, "error getting feature config")
		return err
	}

	// configure respective features
	// in the future when new optional features are added, we need to add corresponding feature handlers and invoke them here
	canaryCfgMgr, err := MakeCanaryConfigMgr(ctx, logger, fissionClient, kubeClient, featureConfig.CanaryConfig.PrometheusSvc)
	if err != nil {
		return fmt.Errorf("failed to start canary config manager: %w", err)
	}

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader progresses canary rollouts, so two replicas don't both
	// shift HTTPTrigger weights. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default).
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-canaryconfig", logger)
	if err != nil {
		return err
	}
	if err := crMgr.Add(crmanager.LeaderRunnable(func(c context.Context) error {
		gm := manager.New()
		canaryCfgMgr.Run(c, gm)
		<-c.Done()
		gm.Wait()
		return nil
	})); err != nil {
		return err
	}
	return crMgr.Start(ctx)
}
