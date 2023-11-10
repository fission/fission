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

package kubewatcher

import (
	"context"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return errors.Wrap(err, "failed to get fission client")
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return errors.Wrap(err, "failed to get kubernetes client")
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	poster := publisher.MakeWebhookPublisher(logger, routerUrl)
	kubeWatch := MakeKubeWatcher(ctx, logger, kubeClient, poster)
	ws, err := MakeWatchSync(ctx, logger, fissionClient, kubeWatch)
	if err != nil {
		return errors.Wrap(err, "error making watch sync")
	}
	ws.Run(ctx, mgr)

	return nil
}
