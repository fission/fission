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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"
)

// Start the buildermgr service.
func Start(ctx context.Context, logger *zap.Logger, storageSvcUrl string) error {
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

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.BuilderPodSpecPath)
	if err != nil {
		logger.Warn("error reading data for pod spec patch", zap.String("path", fv1.BuilderPodSpecPath), zap.Error(err))
	}

	envWatcher := makeEnvironmentWatcher(ctx, bmLogger, fissionClient, kubernetesClient, fetcherConfig, podSpecPatch)
	envWatcher.Run(ctx)

	pkgWatcher := makePackageWatcher(bmLogger, fissionClient,
		kubernetesClient, storageSvcUrl,
		utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.Pods),
		utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.PackagesResource))
	pkgWatcher.Run(ctx)
	return nil
}
