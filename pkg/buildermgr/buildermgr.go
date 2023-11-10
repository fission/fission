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
	"os"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

// Start the buildermgr service.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, storageSvcUrl string) error {
	bmLogger := logger.Named("builder_manager")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return errors.Wrap(err, "failed to get fission client")
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return errors.Wrap(err, "failed to get kubernetes client")
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/packages")
	if err != nil {
		return errors.Wrap(err, "error making fetcher config")
	}

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.BuilderPodSpecPath)
	if err != nil && !os.IsNotExist(err) {
		logger.Warn("error reading data for pod spec patch", zap.String("path", fv1.BuilderPodSpecPath), zap.Error(err))
	}

	envWatcher, err := makeEnvironmentWatcher(ctx, bmLogger, fissionClient, kubernetesClient, fetcherConfig, podSpecPatch)
	if err != nil {
		return err
	}
	envWatcher.Run(ctx, mgr)

	pkgWatcher := makePackageWatcher(bmLogger, fissionClient,
		kubernetesClient, storageSvcUrl,
		utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.Pods),
		utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.PackagesResource))
	err = pkgWatcher.Run(ctx, mgr)
	if err != nil {
		return err
	}
	return nil
}
