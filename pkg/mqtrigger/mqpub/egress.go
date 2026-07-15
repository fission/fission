// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqpub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// EgressJob is the unit of broker egress (RFC-0027): a topic publish destined
// for an external broker, enqueued durably on the statestore and executed by
// the publisher loop in that broker type's classic mqt head — where the broker
// SDK, connectivity, and credentials already live, and deliberately NOT in the
// router. Namespace is carried for provenance (broker topic namespaces are the
// broker's own concern — kafka topics are flat, so Topic is used as-is).
type EgressJob struct {
	Namespace   string `json:"namespace"`
	Topic       string `json:"topic"`
	ContentType string `json:"contentType,omitempty"`
	Payload     []byte `json:"payload"`
}

// EgressQueueForType returns the statestore Queue a broker type's egress jobs
// travel on. Per-type queues, not one shared queue: Lease has no type filter,
// so heterogeneous broker heads competing on one queue would lease each
// other's jobs and burn their attempt budgets.
func EgressQueueForType(mqType string) string { return "mq-egress-" + mqType }

// egressPublisher enqueues EgressJobs for broker-type topics. Enqueue is a
// durable write (E1: "durably enqueued for egress" is the broker arm of the
// publish contract); the broker ack happens later in the consuming head,
// retried per the queue budget and dead-lettered visibly (E4).
type egressPublisher struct {
	q     statestore.Queue
	types map[string]struct{}
}

// NewEgressPublisher builds the broker arm of topic publishing over a
// statestore Queue. brokerTypes is the set of MQ types with a classic head
// that runs an egress consumer (kafka today); any other type is rejected with
// ErrUnsupportedMQType rather than enqueued onto a queue nothing consumes.
func NewEgressPublisher(q statestore.Queue, brokerTypes ...string) TopicPublisher {
	types := make(map[string]struct{}, len(brokerTypes))
	for _, t := range brokerTypes {
		types[t] = struct{}{}
	}
	return &egressPublisher{q: q, types: types}
}

// Publish implements TopicPublisher by enqueueing an EgressJob on the
// per-type egress queue.
func (p *egressPublisher) Publish(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error {
	if _, ok := p.types[mqType]; !ok {
		// Fixed label value — mqType is caller-supplied (see statestorePublisher).
		recordPublish(ctx, "unknown", "unsupported")
		return fmt.Errorf("%w: %q has no egress consumer", ErrUnsupportedMQType, mqType)
	}
	if namespace == "" || strings.Contains(namespace, "/") {
		recordPublish(ctx, mqType, "invalid")
		return fmt.Errorf("mqpub: invalid namespace %q", namespace)
	}
	// Broker topic grammars are the broker's own; the shared CRD-side rule is
	// re-applied here for the same defense-in-depth as the statestore sink (it
	// is also what admission enforced on the TopicRef).
	if err := fv1.ValidateTopicName("topic", topic); err != nil {
		recordPublish(ctx, mqType, "invalid")
		return fmt.Errorf("mqpub: invalid topic name: %w", err)
	}
	body, err := json.Marshal(EgressJob{Namespace: namespace, Topic: topic, ContentType: contentType, Payload: payload})
	if err != nil {
		recordPublish(ctx, mqType, "error")
		return fmt.Errorf("mqpub: encoding egress job: %w", err)
	}
	if _, err := p.q.Enqueue(ctx, EgressQueueForType(mqType), statestore.Message{Body: body}, statestore.EnqueueOptions{}); err != nil {
		recordPublish(ctx, mqType, "error")
		return fmt.Errorf("mqpub: enqueueing egress job for %s topic %q: %w", mqType, topic, err)
	}
	recordPublish(ctx, mqType, "enqueued")
	return nil
}

// multiPublisher dispatches Publish by MQ type: the statestore type publishes
// directly (one local EventLog append), every other type goes through the
// egress queue. This is the publisher the router's async dispatcher is wired
// with once broker topic destinations are admitted.
type multiPublisher struct {
	direct TopicPublisher
	egress TopicPublisher
}

// NewMultiPublisher composes the built-in statestore publisher with the broker
// egress publisher behind one TopicPublisher.
func NewMultiPublisher(direct, egress TopicPublisher) TopicPublisher {
	return &multiPublisher{direct: direct, egress: egress}
}

func (p *multiPublisher) Publish(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error {
	if mqType == fv1.MessageQueueTypeStatestore {
		return p.direct.Publish(ctx, namespace, mqType, topic, contentType, payload)
	}
	return p.egress.Publish(ctx, namespace, mqType, topic, contentType, payload)
}
