// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqpub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

func memQueue(t *testing.T) statestore.Queue {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)
	return q
}

func TestEgressQueueForType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "mq-egress-kafka", EgressQueueForType("kafka"))
}

func TestEgressPublisherEnqueuesJob(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	p := NewEgressPublisher(q, fv1.MessageQueueTypeKafka)

	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeKafka, "orders", "application/json", []byte(`{"n":1}`)))

	msgs, err := q.Lease(t.Context(), EgressQueueForType(fv1.MessageQueueTypeKafka), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "one durable egress job per publish (E1: enqueued-for-egress)")
	var job EgressJob
	require.NoError(t, json.Unmarshal(msgs[0].Body, &job))
	assert.Equal(t, EgressJob{Namespace: "ns", Topic: "orders", ContentType: "application/json", Payload: []byte(`{"n":1}`)}, job)
}

func TestEgressPublisherRejects(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	p := NewEgressPublisher(q, fv1.MessageQueueTypeKafka)

	// A type with no egress consumer must not be enqueued onto a queue nothing
	// drains — rejected with the sentinel.
	err := p.Publish(t.Context(), "ns", "nats-jetstream", "t", "", nil)
	require.ErrorIs(t, err, ErrUnsupportedMQType)

	require.Error(t, p.Publish(t.Context(), "", fv1.MessageQueueTypeKafka, "t", "", nil), "empty namespace")
	require.Error(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeKafka, "a/b", "", nil), "slash topic")

	// Nothing leaked onto any egress queue.
	msgs, err := q.Lease(t.Context(), EgressQueueForType(fv1.MessageQueueTypeKafka), 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestMultiPublisherDispatch(t *testing.T) {
	t.Parallel()
	el := memEventLog(t)
	q := memQueue(t)
	p := NewMultiPublisher(NewStatestorePublisher(el), NewEgressPublisher(q, fv1.MessageQueueTypeKafka))

	// statestore → direct EventLog append.
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeStatestore, "orders", "text/plain", []byte("a")))
	evs, err := el.Read(t.Context(), StreamForTopic("ns", "orders"), 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1)

	// kafka → egress queue, not the EventLog.
	require.NoError(t, p.Publish(t.Context(), "ns", fv1.MessageQueueTypeKafka, "orders", "text/plain", []byte("b")))
	msgs, err := q.Lease(t.Context(), EgressQueueForType(fv1.MessageQueueTypeKafka), 10, time.Minute)
	require.NoError(t, err)
	assert.Len(t, msgs, 1)
	evs, err = el.Read(t.Context(), StreamForTopic("ns", "orders"), 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1, "broker publishes never touch the topic stream")

	// Unknown type → the sentinel, from the egress arm.
	require.ErrorIs(t, p.Publish(t.Context(), "ns", "gcp-pubsub", "t", "", nil), ErrUnsupportedMQType)
}
