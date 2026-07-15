// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package egress runs the broker-egress publisher loop (RFC-0027): it consumes
// the per-broker-type statestore Queue mq-egress-<mqType> that the router's
// async dispatcher enqueues broker-destined topic publishes onto, and executes
// each job against the actual broker via the head's publish function — keeping
// broker SDKs and credentials in the mqt head, never in the router.
//
// This is a second consumer of the RFC-0024 queue machinery; the loop is a
// deliberate fresh ~80 lines over the same statestore.Queue rather than an
// extraction from pkg/router/asyncinvoke — the dispatcher's generic part is
// tiny, and its property-tested invariants (A7 lease/timeout coupling, settle
// detachment) encode async-delivery concerns egress does not have. E4 (egress
// honesty) rests on the queue's own checked protocol: a broker outage retries
// per the attempt budget and then dead-letters visibly.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/statestore"
)

const (
	// batchSize bounds one Lease call.
	batchSize = 10
	// leaseFor is the per-job lease; publishTimeout stays strictly below it so
	// a publish attempt cannot outlive its lease (the asyncinvoke A7 shape).
	leaseFor       = 30 * time.Second
	publishTimeout = 20 * time.Second
	// pollInterval paces Lease polls on an idle queue.
	pollInterval = time.Second
	// retryBackoff is the Nack redelivery delay after a failed broker publish —
	// long enough to ride out a broker hiccup without burning the attempt
	// budget in seconds.
	retryBackoff = 10 * time.Second
)

// PublishFunc executes one egress job against the broker. A nil error means
// broker-acked (the job settles); an error means retry per the queue budget.
type PublishFunc func(ctx context.Context, job mqpub.EgressJob) error

// BrokerPublisherProvider is the optional interface a broker MessageQueue
// provider implements to opt its classic head into the egress loop. The bundle
// head type-asserts it after factory.Create and, when the statestore is
// configured, runs a Consumer over mq-egress-<mqType>. The returned closer
// releases the broker producer on head shutdown.
type BrokerPublisherProvider interface {
	NewEgressPublisher() (PublishFunc, io.Closer, error)
}

// Consumer is the egress loop for one broker type's queue.
type Consumer struct {
	logger  logr.Logger
	q       statestore.Queue
	queue   string
	mqType  string
	publish PublishFunc
	// poll and backoff are pollInterval/retryBackoff unless a test tightens
	// them.
	poll    time.Duration
	backoff time.Duration
}

// New builds the egress consumer for mqType over the given queue capability.
func New(logger logr.Logger, q statestore.Queue, mqType string, publish PublishFunc) *Consumer {
	return &Consumer{
		logger:  logger.WithName("egress").WithValues("mqType", mqType),
		q:       q,
		queue:   mqpub.EgressQueueForType(mqType),
		mqType:  mqType,
		publish: publish,
		poll:    pollInterval,
		backoff: retryBackoff,
	}
}

// Run consumes until ctx ends. Queue leases are SKIP LOCKED, so running one
// loop per head replica is safe (competing consumers, like the async
// dispatcher across router replicas).
func (c *Consumer) Run(ctx context.Context) error {
	c.logger.Info("egress consumer started", "queue", c.queue)
	for {
		n := c.pollOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(c.poll):
			}
		}
	}
}

func (c *Consumer) pollOnce(ctx context.Context) int {
	msgs, err := c.q.Lease(ctx, c.queue, batchSize, leaseFor)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Error(err, "leasing egress jobs", "queue", c.queue)
		}
		return 0
	}
	// Process the batch CONCURRENTLY (the asyncinvoke shape): all leased jobs
	// share one leaseFor window, so sequential processing would put every job
	// past the first under an already-expired lease whenever the broker is slow
	// — redelivery and false dead-letters for jobs that actually published.
	// Concurrent processing keeps the per-job publishTimeout < leaseFor
	// coupling real for the whole batch.
	var wg sync.WaitGroup
	for _, msg := range msgs {
		wg.Go(func() { c.process(ctx, msg) })
	}
	wg.Wait()
	return len(msgs)
}

// process publishes one job and settles it. Settles run on a cancel-detached
// context so a terminal outcome lands even during shutdown (the asyncinvoke
// pattern); an unsettled lease simply expires and redelivers — at-least-once.
func (c *Consumer) process(ctx context.Context, msg statestore.LeasedMessage) {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	var job mqpub.EgressJob
	if err := json.Unmarshal(msg.Body, &job); err != nil {
		// Malformed jobs are permanent: no number of retries will fix the bytes.
		// Kill dead-letters immediately, keeping the poison job inspectable.
		c.logger.Error(err, "malformed egress job; dead-lettering", "id", msg.ID)
		recordEgress(ctx, "malformed")
		c.settle(settleCtx, "kill", func() error { return c.q.Kill(settleCtx, msg.Receipt, "malformed egress job") })
		return
	}
	// Re-validate the topic at this sink with the TYPE-AWARE grammar (defense
	// in depth, like the mqpub publishers): a job forged directly onto the
	// queue must not reach the broker with an arbitrary topic string, and a
	// broker-invalid name is permanent — Kill, don't burn the retry budget on
	// a broker that refuses it forever.
	if err := fv1.ValidateTopicNameForMQType("topic", c.mqType, job.Topic); err != nil {
		c.logger.Error(err, "egress job with invalid topic; dead-lettering", "id", msg.ID)
		recordEgress(ctx, "malformed")
		c.settle(settleCtx, "kill", func() error { return c.q.Kill(settleCtx, msg.Receipt, "invalid egress topic") })
		return
	}

	pubCtx, pubCancel := context.WithTimeout(ctx, publishTimeout)
	err := c.publish(pubCtx, job)
	pubCancel()
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down: let the lease expire and redeliver
		}
		// Retry per the queue budget; once attempts are exhausted the Nack
		// dead-letters (queue invariant Q3) — visible via the DLQ admin API.
		c.logger.Error(err, "broker publish failed; will retry",
			"id", msg.ID, "topic", job.Topic, "attempt", msg.Attempts)
		recordEgress(ctx, "retry")
		c.settle(settleCtx, "nack", func() error { return c.q.Nack(settleCtx, msg.Receipt, c.backoff) })
		return
	}
	recordEgress(ctx, "published")
	c.settle(settleCtx, "ack", func() error { return c.q.Ack(settleCtx, msg.Receipt) })
}

// settle runs one settle call, quieting only the stale-receipt race (an
// expired lease already redelivered — the other delivery owns the outcome).
// Every other failure logs and meters: settleCtx is detached from shutdown, so
// the only cancellation is the settle timeout itself — a statestore brownout at
// ack time, i.e. the exact precondition for a duplicate broker publish, which
// must never be the one silent settle failure.
func (c *Consumer) settle(ctx context.Context, op string, fn func() error) {
	if err := fn(); err != nil {
		if errors.Is(err, statestore.ErrInvalidReceipt) {
			c.logger.V(1).Info("egress settle raced lease expiry", "op", op)
			return
		}
		recordEgress(ctx, "settle_failed")
		c.logger.Error(err, "settling egress job (job will redeliver — possible duplicate publish)", "op", op)
	}
}
