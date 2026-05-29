// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/manager"
)

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ manager.Interface, routerUrl string) error {
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
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

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader consumes the message queue and manages triggers, so two
	// replicas don't double-consume. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default).
	crMgr, err := crmanager.NewLeaderElected(restConfig, "fission-mqtrigger", logger)
	if err != nil {
		return err
	}
	if err := crMgr.Add(crmanager.LeaderRunnable(func(c context.Context) error {
		gm := manager.New()
		for _, factory := range finformerFactory {
			factory.Start(c.Done())
		}
		mqtMgr.Run(c, c.Done(), gm)
		<-c.Done()
		gm.Wait()
		return nil
	})); err != nil {
		return err
	}
	return crMgr.Start(ctx)
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
