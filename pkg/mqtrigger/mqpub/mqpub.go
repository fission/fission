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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// ErrUnsupportedMQType is returned by a publisher handed a message-queue type it
// does not implement. Until the RFC-0027 egress phase lands, only the statestore
// provider exists, so broker types map to this error.
var ErrUnsupportedMQType = errors.New("mqpub: unsupported message-queue type")

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
	el statestore.EventLog
}

// NewStatestorePublisher builds the built-in, broker-free TopicPublisher over an
// EventLog capability.
func NewStatestorePublisher(el statestore.EventLog) TopicPublisher {
	return &statestorePublisher{el: el}
}

// Publish implements TopicPublisher: one AppendAny of one event. AppendAny is a
// single atomic round-trip (no client CAS loop), and Append returns only after
// the write is durable, so a nil error IS the durability guarantee (E1).
func (p *statestorePublisher) Publish(ctx context.Context, namespace, mqType, topic, contentType string, payload []byte) error {
	if mqType != fv1.MessageQueueTypeStatestore {
		recordPublish(ctx, mqType, "unsupported")
		return fmt.Errorf("%w: %q (statestore only until the egress phase)", ErrUnsupportedMQType, mqType)
	}
	_, err := p.el.Append(ctx, StreamForTopic(namespace, topic), statestore.AppendAny,
		[]statestore.Event{{Type: contentType, Payload: payload}})
	if err != nil {
		recordPublish(ctx, mqType, "error")
		return fmt.Errorf("mqpub: publishing to topic %s/%s: %w", namespace, topic, err)
	}
	recordPublish(ctx, mqType, "published")
	return nil
}
