/*
Copyright 2017 The Fission Authors.

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

package timer

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/publisher"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger *zap.Logger, mgr manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	poster := publisher.MakeWebhookPublisher(logger, routerUrl)
	timerSync, err := MakeTimerSync(ctx, logger, fissionClient, MakeTimer(logger, poster))
	if err != nil {
		return fmt.Errorf("error making timer sync: %w", err)
	}
	timerSync.Run(ctx, mgr)
	return nil
}
