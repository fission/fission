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
	"context"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	informerv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

type (
	// ConfigSecretController represents a controller for configmaps and secrets
	ConfigSecretController struct {
		logger *zap.Logger

		fissionClient versioned.Interface
	}
)

// MakeConfigSecretController makes a controller for configmaps and secrets which changes related functions
func MakeConfigSecretController(ctx context.Context, logger *zap.Logger, fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface, types map[fv1.ExecutorType]executortype.ExecutorType,
	configmapInformer map[string]informerv1.ConfigMapInformer,
	secretInformer map[string]informerv1.SecretInformer) *ConfigSecretController {
	logger.Debug("Creating ConfigMap & Secret Controller")
	cmsController := &ConfigSecretController{
		logger:        logger,
		fissionClient: fissionClient,
	}
	for _, informer := range configmapInformer {
		informer.Informer().AddEventHandler(ConfigMapEventHandlers(ctx, logger, fissionClient, kubernetesClient, types))
	}
	for _, informer := range secretInformer {
		informer.Informer().AddEventHandler(SecretEventHandlers(ctx, logger, fissionClient, kubernetesClient, types))
	}

	return cmsController
}

func refreshPods(ctx context.Context, logger *zap.Logger, funcs []fv1.Function, types map[fv1.ExecutorType]executortype.ExecutorType) {
	for _, f := range funcs {
		var err error

		et, exists := types[f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType]
		if exists {
			err = et.RefreshFuncPods(ctx, logger, f)
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
