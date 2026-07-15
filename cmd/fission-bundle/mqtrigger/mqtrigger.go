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

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/mqtrigger/egress"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	_ "github.com/fission/fission/pkg/mqtrigger/messageQueue/kafka"
	_ "github.com/fission/fission/pkg/mqtrigger/messageQueue/statestore"
	"github.com/fission/fission/pkg/statestore"

	// Statestore drivers the statestore MQ provider opens via STATESTORE_DRIVER:
	// the HTTP client (embedded mode → svc/statestore) and Postgres (external
	// mode → the DB directly). Registered here, not in the provider package, so
	// importing the provider for its validator (fission CLI) links no drivers.
	_ "github.com/fission/fission/pkg/statestore/client"
	_ "github.com/fission/fission/pkg/statestore/postgres"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/metrics"
)

// mqtReconcileConcurrency lets independent MessageQueueTriggers subscribe in
// parallel, matching the throughput of the previous 4 create/update workers.
const mqtReconcileConcurrency = 4

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, routerUrl string) error {
	// fissionClient is needed below for the subscription manager; NewTriggerManager
	// resolves its own to wait for the Function CRDs (a cheap cached call).
	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
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
		return fmt.Errorf("failed to connect to remote message queue server: %w", err)
	}

	// Active-passive HA via native controller-runtime leader election: only the
	// elected leader consumes the message queue and manages triggers, so two
	// replicas don't double-consume. No-op when LEADER_ELECTION_ENABLED is unset
	// (single-replica default). The reconciler watches MessageQueueTriggers
	// through the Manager's cache and runs only on the leader.
	crMgr, err := crmanager.NewTriggerManager(ctx, clientGen, "fission-mqtrigger", logger)
	if err != nil {
		return err
	}

	// Serve the subsystem's custom metrics on every replica (the crmanager
	// Manager's own metrics server is disabled), so a scrape hits whichever pod.
	if err := crMgr.Add(crmanager.NonLeaderRunnable(func(c context.Context) error {
		gm := &errgroup.Group{}
		metrics.ServeMetrics(c, "mqtrigger", logger, gm)
		<-c.Done()
		return gm.Wait()
	})); err != nil {
		return err
	}

	// RFC-0027 broker egress: a broker provider (kafka) also runs the publisher
	// loop over its per-type statestore queue mq-egress-<mqType> — the jobs the
	// router's async dispatcher enqueues for broker-destined topic publishes.
	// Requires the statestore wiring (chart sets STATESTORE_DRIVER/DSN on broker
	// heads when statestore.enabled); without it the loop is skipped with a log,
	// matching the admission story (broker topic destinations are usable only on
	// statestore-enabled installs). Queue leases are SKIP LOCKED, so the loop
	// runs on every replica (NonLeader), like the async dispatcher itself.
	if provider, ok := mq.(egress.BrokerPublisherProvider); ok {
		if driver := os.Getenv("STATESTORE_DRIVER"); driver != "" {
			caps, err := statestore.Open(ctx, statestore.Config{Driver: driver, DSN: os.Getenv("STATESTORE_DSN")})
			if err != nil {
				return fmt.Errorf("opening statestore for broker egress: %w", err)
			}
			queue, err := statestore.NewScoped(caps, nil).Queue()
			if err != nil {
				_ = caps.Close()
				return fmt.Errorf("statestore queue capability for broker egress: %w", err)
			}
			publish, producerCloser, err := provider.NewEgressPublisher()
			if err != nil {
				_ = caps.Close()
				return fmt.Errorf("creating broker egress publisher: %w", err)
			}
			consumer := egress.New(logger, queue, string(mqType), publish)
			if err := crMgr.Add(crmanager.NonLeaderRunnable(func(c context.Context) error {
				// Producer and store handles are released on shutdown; opened
				// here rather than process-lifetime so a flush happens.
				defer func() { _ = caps.Close() }()
				defer func() { _ = producerCloser.Close() }()
				return consumer.Run(c)
			})); err != nil {
				_ = producerCloser.Close()
				_ = caps.Close()
				return err
			}
		} else {
			logger.Info("statestore not configured; broker egress consumer disabled",
				"mqType", mqType)
		}
	}

	// The subscription manager (service() actor) is leader-only: only the leader
	// holds live queue subscriptions, so a standby doesn't double-consume.
	mqtMgr := mqtrigger.MakeMessageQueueTriggerManager(logger, fissionClient, mqType, mq)
	if err := crMgr.Add(crmanager.LeaderRunnable(mqtMgr.Start)); err != nil {
		return err
	}

	r := mqtrigger.NewMessageQueueTriggerReconciler(logger, crMgr.GetClient(), mqtMgr)
	if err := controller.RegisterTenantScopedWithConcurrency(crMgr, &fv1.MessageQueueTrigger{}, r, "messagequeuetrigger", mqtReconcileConcurrency); err != nil {
		return fmt.Errorf("error registering messagequeuetrigger reconciler: %w", err)
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
