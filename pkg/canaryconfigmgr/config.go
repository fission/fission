// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils/manager"
)

// ConfigureFeatures gets the feature config and configures the features that are enabled
func ConfigureFeatures(ctx context.Context, logger logr.Logger, unitTestMode bool, fissionClient versioned.Interface,
	kubeClient kubernetes.Interface, mgr manager.Interface) error {
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
	canaryCfgMgr.Run(ctx, mgr)

	return err
}
