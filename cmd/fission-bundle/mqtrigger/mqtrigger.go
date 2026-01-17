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

package mqtrigger

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	_ "github.com/fission/fission/pkg/mqtrigger/messageQueue/kafka"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, mgr manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}

	err = crd.WaitForFunctionCRDs(ctx, logger, fissionClient)
	if err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	mqType := (fv1.MessageQueueType)(os.Getenv("MESSAGE_QUEUE_TYPE"))
	mqUrl := os.Getenv("MESSAGE_QUEUE_URL")

	secretsPath := strings.TrimSpace(os.Getenv("MESSAGE_QUEUE_SECRETS"))

	var secrets map[string][]byte
	if len(secretsPath) > 0 {
		// For authentication with message queue
		secrets, err = readSecrets(logger, secretsPath)
		if err != nil {
			return err
		}
	}

	mq, err := factory.Create(
		logger,
		mqType,
		messageQueue.Config{
			MQType:  (string)(mqType),
			Url:     mqUrl,
			Secrets: secrets,
		},
		routerUrl,
	)
	if err != nil {
		logger.Error(err, "failed to connect to remote message queue server")

		os.Exit(1)
	}

	finformerFactory := make(map[string]genInformer.SharedInformerFactory, 0)
	for _, ns := range utils.DefaultNSResolver().FissionResourceNS {
		finformerFactory[ns] = genInformer.NewSharedInformerFactoryWithOptions(fissionClient, time.Minute*30, genInformer.WithNamespace(ns))
	}

	mqtMgr, err := mqtrigger.MakeMessageQueueTriggerManager(logger, fissionClient, mqType, finformerFactory, mq)
	if err != nil {
		return err
	}

	// Start informer factory
	for _, factory := range finformerFactory {
		factory.Start(ctx.Done())
	}

	mqtMgr.Run(ctx, ctx.Done(), mgr)

	return nil
}

func readSecrets(logger logr.Logger, secretsPath string) (map[string][]byte, error) {
	// return if no secrets exist
	if _, err := os.Stat(secretsPath); os.IsNotExist(err) {
		return nil, err
	}

	secretFiles, err := os.ReadDir(secretsPath)
	if err != nil {
		return nil, err
	}

	secrets := make(map[string][]byte)
	for _, secretFile := range secretFiles {

		fileName := secretFile.Name()
		// /etc/secrets contain some hidden directories (like .data)
		// ignore them
		if !secretFile.IsDir() && !strings.HasPrefix(fileName, ".") {
			logger.Info(fmt.Sprintf("Reading secret from %s", fileName))

			filePath := path.Join(secretsPath, fileName)
			secret, fileReadErr := os.ReadFile(filePath)
			if fileReadErr != nil {
				return nil, fileReadErr
			}

			secrets[fileName] = secret
		}
	}

	return secrets, nil
}
