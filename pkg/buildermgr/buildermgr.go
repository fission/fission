// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

// Start the buildermgr service.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, storageSvcUrl string) error {
	bmLogger := logger.WithName("builder_manager")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	fetcherConfig, err := fetcherConfig.MakeFetcherConfig("/packages")
	if err != nil {
		return fmt.Errorf("error making fetcher config: %w", err)
	}

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.BuilderPodSpecPath)
	if err != nil && !os.IsNotExist(err) {
		logger.Error(err, "error reading data for pod spec patch", "path", fv1.BuilderPodSpecPath)
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
