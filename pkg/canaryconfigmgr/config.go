// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/utils/crmanager"
)

// ConfigureFeatures gets the feature config and configures the features that are enabled
func ConfigureFeatures(ctx context.Context, restConfig *rest.Config, logger logr.Logger, unitTestMode bool) error {
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

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader progresses canary rollouts, so two replicas don't both
	// shift HTTPTrigger weights. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default). The reconciler watches through the Manager's
	// namespace-scoped cache and runs only on the elected leader.
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-canaryconfig", logger)
	if err != nil {
		return err
	}

	canaryCfgMgr, err := MakeCanaryConfigMgr(logger, crMgr.GetClient(), crMgr.GetAPIReader(), featureConfig.CanaryConfig.PrometheusSvc)
	if err != nil {
		return fmt.Errorf("failed to start canary config manager: %w", err)
	}

	r := &CanaryConfigReconciler{
		logger: logger.WithName("canaryconfig_reconciler"),
		client: crMgr.GetClient(),
		mgr:    canaryCfgMgr,
	}
	if err := controller.RegisterTenantScoped(crMgr, &fv1.CanaryConfig{}, r, "canaryconfig"); err != nil {
		return fmt.Errorf("error registering canaryconfig reconciler: %w", err)
	}
	return crMgr.Start(ctx)
}
