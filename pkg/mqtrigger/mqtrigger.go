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
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
)

func Start(logger *zap.Logger, routerUrl string) error {
	fissionClient, _, _, err := crd.MakeFissionClient()

	if err != nil {
		return errors.Wrap(err, "failed to get fission or kubernetes client")
	}

	err = fissionClient.WaitForCRDs()
	if err != nil {
		return errors.Wrap(err, "error waiting for CRDs")
	}

	// Message queue type: nats is the only supported one for now
	mqType := os.Getenv("MESSAGE_QUEUE_TYPE")
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

	mqCfg := messageQueue.MessageQueueConfig{
		MQType:  mqType,
		Url:     mqUrl,
		Secrets: secrets,
	}
	messageQueue.MakeMessageQueueTriggerManager(logger, fissionClient, routerUrl, mqCfg)
	return nil
}

func readSecrets(logger *zap.Logger, secretsPath string) (map[string][]byte, error) {

	// return if no secrets exist
	if _, err := os.Stat(secretsPath); os.IsNotExist(err) {
		return nil, err
	}

	secretFiles, err := ioutil.ReadDir(secretsPath)
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
			secret, fileReadErr := ioutil.ReadFile(filePath)
			if fileReadErr != nil {
				return nil, fileReadErr
			}

			secrets[fileName] = secret
		}
	}

	return secrets, nil
}
