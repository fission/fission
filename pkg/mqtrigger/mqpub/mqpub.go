// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package mqpub provides TopicPublisher — the outbound mirror of the mqtrigger
// MessageQueue (Subscribe/Unsubscribe) interface (RFC-0027). It is named to stay
// distinct from pkg/publisher, the router-POST webhook publisher.
//
// The statestore implementation makes topics work with zero external brokers:
// a topic is the EventLog stream "topic/<namespace>/<topic>", published with a
// single AppendAny (no CAS retry loop — topic events are independent). Broker
// implementations (kafka et al.) arrive with the RFC-0027 egress phase behind
// the same interface.
package mqpub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// ErrUnsupportedMQType is returned by a publisher handed a message-queue type it
// does not implement. Until the RFC-0027 egress phase lands, only the statestore
// provider exists, so broker types map to this error.
var ErrUnsupportedMQType = errors.New("mqpub: unsupported message-queue type")

// ErrTopicBacklogCap is returned when a topic stream has reached the phase-1
// backlog cap (see DefaultMaxTopicBacklog).
var ErrTopicBacklogCap = errors.New("mqpub: topic backlog cap reached")

// DefaultMaxTopicBacklog bounds a topic stream's head in phase 1. Nothing
// consumes or trims topics until the P2 subscriber + retention reaper land, so
// every append is permanent — an unbounded stream on the shared store is a
// cluster-wide resource hazard (a full embedded PVC fails every tenant's
// enqueue). A publish to a topic at the cap is rejected loudly and counted
// ("capped"), never silently dropped. The check reads Head first, so the cap is
// soft under concurrent publishers (bounded overshoot by in-flight appends) —
// a resource guard, not an exact quota. P2's retention machinery supersedes it
// with min-cursor trim + age/size backstops and a tunable knob.
const DefaultMaxTopicBacklog = 10000

// TopicPublisher durably publishes a payload to a namespaced topic on a
// message-queue provider.
//
// The signature is all-strings (no fv1 types) so consumers that must stay free
// of the CRD package — notably the router's asyncinvoke dispatcher, which takes
// Publish as an injected function — can depend on it without coupling. The
// namespace is an argument rather than constructor state (a deliberate
// flattening of the RFC-0027 sketch) because the async dispatcher publishing
// destinations is cluster-scoped and serves every namespace from one loop.
type TopicPublisher interface {
	// Publish durably hands payload to topic in namespace on the provider
	// selected by mqType. For the statestore provider it returns only after the
	// event is appended (RFC-0027 invariant E1 — never a fake accept);
	// contentType travels as the event's Type so consumers can replay it as the
	// delivery Content-Type.
	Publish(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error
}

// StreamForTopic returns the EventLog stream name for a namespaced topic.
// Topics are namespace-scoped (RFC-0027, mirroring RFC-0024's same-namespace
// destination rule R6); topic names are admission-validated to exclude "/" so
// the mapping cannot alias across namespaces.
func StreamForTopic(namespace, topic string) string {
	return "topic/" + namespace + "/" + topic
}

// statestorePublisher publishes topics onto the RFC-0021 EventLog.
type statestorePublisher struct {
	el         statestore.EventLog
	maxBacklog int64
}

// NewStatestorePublisher builds the built-in, broker-free TopicPublisher over an
// EventLog capability, with the phase-1 DefaultMaxTopicBacklog cap.
func NewStatestorePublisher(el statestore.EventLog) TopicPublisher {
	return &statestorePublisher{el: el, maxBacklog: DefaultMaxTopicBacklog}
}

// Publish implements TopicPublisher: one AppendAny of one event. AppendAny is a
// single atomic round-trip (no client CAS loop), and Append returns only after
// the write is durable, so a nil error IS the durability guarantee (E1).
//
// Inputs are re-validated at this sink (defense in depth — admission is the
// authoritative gate, but the stream-name injectivity that namespace isolation
// rests on must not depend on a single distant layer).
func (p *statestorePublisher) Publish(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error {
	if mqType != fv1.MessageQueueTypeStatestore {
		// Fixed label value: mqType is caller-supplied, and raw strings in a
		// metric label would mint unbounded time series (the error carries the
		// exact type).
		recordPublish(ctx, "unknown", "unsupported")
		return fmt.Errorf("%w: %q (statestore only until the egress phase)", ErrUnsupportedMQType, mqType)
	}
	if namespace == "" || strings.Contains(namespace, "/") {
		recordPublish(ctx, mqType, "invalid")
		return fmt.Errorf("mqpub: invalid namespace %q", namespace)
	}
	if err := fv1.ValidateTopicName("topic", topic); err != nil {
		recordPublish(ctx, mqType, "invalid")
		return fmt.Errorf("mqpub: invalid topic name: %w", err)
	}
	stream := StreamForTopic(namespace, topic)
	// Phase-1 backlog cap (soft — see DefaultMaxTopicBacklog): reject loudly
	// rather than grow the shared store unboundedly with no consumer to trim it.
	head, err := p.el.Head(ctx, stream)
	if err != nil {
		recordPublish(ctx, mqType, "error")
		return fmt.Errorf("mqpub: reading topic head %s/%s: %w", namespace, topic, err)
	}
	if head >= p.maxBacklog {
		recordPublish(ctx, mqType, "capped")
		return fmt.Errorf("%w: topic %s/%s at %d events", ErrTopicBacklogCap, namespace, topic, head)
	}
	if _, err := p.el.Append(ctx, stream, statestore.AppendAny,
		[]statestore.Event{{Type: contentType, Payload: payload}}); err != nil {
		recordPublish(ctx, mqType, "error")
		return fmt.Errorf("mqpub: publishing to topic %s/%s: %w", namespace, topic, err)
	}
	recordPublish(ctx, mqType, "published")
	return nil
}
