/*
Copyright 2016 The Fission Authors.

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

package cms

import (
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/executortype"
)

type (
	// ConfigSecretController represents a controller for configmaps and secrets
	ConfigSecretController struct {
		logger *zap.Logger

		configmapInformer *k8sCache.SharedIndexInformer
		secretInformer    *k8sCache.SharedIndexInformer

		fissionClient *crd.FissionClient
	}
)

// MakeConfigSecretController makes a controller for configmaps and secrets which changes related functions
func MakeConfigSecretController(logger *zap.Logger, fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset, types map[fv1.ExecutorType]executortype.ExecutorType,
	configmapInformer *k8sCache.SharedIndexInformer,
	secretInformer *k8sCache.SharedIndexInformer) *ConfigSecretController {
	logger.Debug("Creating ConfigMap & Secret Controller")
	cmsController := &ConfigSecretController{
		logger:            logger,
		configmapInformer: configmapInformer,
		secretInformer:    secretInformer,
		fissionClient:     fissionClient,
	}
	(*configmapInformer).AddEventHandler(ConfigMapEventHandlers(logger, fissionClient, kubernetesClient, types))
	(*secretInformer).AddEventHandler(SecretEventHandlers(logger, fissionClient, kubernetesClient, types))

	return cmsController
}

func refreshPods(logger *zap.Logger, funcs []fv1.Function, types map[fv1.ExecutorType]executortype.ExecutorType) {
	for _, f := range funcs {
		var err error

		et, exists := types[f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType]
		if exists {
			err = et.RefreshFuncPods(logger, f)
		} else {
			err = errors.Errorf("Unknown executor type '%v'", f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
		}

		if err != nil {
			logger.Error("Failed to recycle pods for function after configmap/secret changed",
				zap.Error(err),
				zap.Any("function", f))
		}
	}
}
