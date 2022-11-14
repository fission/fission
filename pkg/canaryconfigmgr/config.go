/*
Copyright 2018 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package canaryconfigmgr

import (
	"context"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// ConfigureFeatures gets the feature config and configures the features that are enabled
func ConfigureFeatures(ctx context.Context, logger *zap.Logger, unitTestMode bool, fissionClient versioned.Interface, kubeClient kubernetes.Interface) error {
	// set feature enabled to false if unitTestMode
	if unitTestMode {
		return nil
	}

	// get the featureConfig from config map mounted onto the file system
	featureConfig, err := config.GetFeatureConfig()
	if err != nil {
		logger.Error("error getting feature config", zap.Error(err))
		return err
	}

	// configure respective features
	// in the future when new optional features are added, we need to add corresponding feature handlers and invoke them here
	canaryCfgMgr, err := MakeCanaryConfigMgr(ctx, logger, fissionClient, kubeClient, featureConfig.CanaryConfig.PrometheusSvc)
	if err != nil {
		return errors.Wrap(err, "failed to start canary config manager")
	}
	canaryCfgMgr.Run(ctx)

	return err
}
