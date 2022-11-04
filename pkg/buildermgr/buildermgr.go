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

package buildermgr

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	k8sInformers "k8s.io/client-go/informers"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"
)

// Start the buildermgr service.
func Start(ctx context.Context, logger *zap.Logger, storageSvcUrl string, envBuilderNamespace string) error {
	bmLogger := logger.Named("builder_manager")

	fissionClient, kubernetesClient, _, _, err := crd.MakeFissionClient()
	if err != nil {
		return errors.Wrap(err, "failed to get fission or kubernetes client")
	}

	err = crd.WaitForCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/packages")
	if err != nil {
		return errors.Wrap(err, "error making fetcher config")
	}

	var podSpecPatch *apiv1.PodSpec
	namespace, err := utils.GetCurrentNamespace()
	if err != nil {
		logger.Warn("Current namespace not found %v", zap.Error(err))
	} else {
		podSpecPatch, err = util.GetSpecFromConfigMap(ctx, kubernetesClient, fv1.BuilderPodSpecConfigmap, namespace)
		if err != nil {
			logger.Warn("Either configmap is not found or error reading data %v", zap.Error(err))
		}
	}

	envWatcher := makeEnvironmentWatcher(ctx, bmLogger, fissionClient, kubernetesClient, fetcherConfig, envBuilderNamespace, podSpecPatch)
	envWatcher.Run(ctx)

	k8sInformerFactory := k8sInformers.NewSharedInformerFactory(kubernetesClient, time.Minute*30)
	podInformer := k8sInformerFactory.Core().V1().Pods().Informer()
	pkgWatcher := makePackageWatcher(bmLogger, fissionClient,
		kubernetesClient, envBuilderNamespace, storageSvcUrl, podInformer,
		utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.PackagesResource))
	pkgWatcher.Run(ctx)
	return nil
}
