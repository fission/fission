// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// resolverFor returns a FunctionConfigResolver that reports every function found
// with the given config (and no onward destinations unless set).
func resolverFor(cfg FunctionConfig) FunctionConfigResolver {
	return func(context.Context, string, string) (FunctionConfig, bool) { return cfg, true }
}

func destDispatcher(q statestore.Queue, d Deliverer, now time.Time, resolve FunctionConfigResolver) *Dispatcher {
	return New(Options{
		Queue: q, Deliverer: d, Logger: logr.Discard(),
		Now: func() time.Time { return now }, Rand: func() float64 { return 0.5 },
		ResolveFunctionConfig: resolve,
	})
}

// leaseOne enqueues env and returns the single leased message.
func leaseOne(t *testing.T, q statestore.Queue, env Envelope) (string, statestore.LeasedMessage) {
	t.Helper()
	body, err := env.Encode()
	require.NoError(t, err)
	id, err := q.Enqueue(t.Context(), DefaultQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	return id, l[0]
}

func TestProcessFiresOnSuccessFunctionDestination(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	now := time.Unix(1_000_000, 0)
	d := destDispatcher(q, scriptedDeliverer{DeliveryResult{StatusCode: 200, Body: []byte("resp")}}, now,
		resolverFor(FunctionConfig{FunctionTimeout: 30}))

	id, msg := leaseOne(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: []byte("orig"),
		OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next"},
	})
	d.process(context.Background(), msg)

	// The primary acked; the destination invocation is now enqueued.
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	destEnv, err := Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, "next", destEnv.Function)
	assert.Equal(t, "ns", destEnv.Namespace)
	assert.Equal(t, 1, destEnv.Depth, "destination enqueued at depth+1")
	assert.Equal(t, 30, destEnv.FunctionTimeout)

	var re ResultEnvelope
	require.NoError(t, json.Unmarshal(destEnv.Body, &re))
	assert.Equal(t, ConditionSuccess, re.RequestContext.Condition)
	assert.Equal(t, "ns/src", re.RequestContext.FunctionRef)
	assert.Equal(t, id, re.RequestContext.InvocationID)
	assert.Equal(t, []byte("orig"), re.RequestPayload)
	assert.Equal(t, []byte("resp"), re.ResponsePayload)
	assert.Equal(t, 200, re.ResponseContext.StatusCode)
}

func TestProcessFiresOnFailureOn4xx(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	now := time.Unix(1_000_000, 0)
	d := destDispatcher(q, scriptedDeliverer{DeliveryResult{StatusCode: 403}}, now, resolverFor(FunctionConfig{}))

	_, msg := leaseOne(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now,
		OnFailure: &Destination{FunctionNamespace: "ns", FunctionName: "handler"},
	})
	d.process(context.Background(), msg)

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1, "onFailure destination enqueued on permanent 4xx")
	destEnv, _ := Decode(l[0].Body)
	assert.Equal(t, "handler", destEnv.Function)
	var re ResultEnvelope
	require.NoError(t, json.Unmarshal(destEnv.Body, &re))
	assert.Equal(t, ConditionHTTP4xx, re.RequestContext.Condition)
}

func TestFireDestinationDepthCap(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	dest := &Destination{FunctionNamespace: "ns", FunctionName: "loop"}

	// At the cap (next = MaxChainDepth+1) → dropped.
	d.fireDestination(context.Background(), dest, MaxChainDepth, ResultEnvelope{})
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l, "destination that would exceed MaxChainDepth is dropped")

	// Just below (next = MaxChainDepth) → enqueued.
	d.fireDestination(context.Background(), dest, MaxChainDepth-1, ResultEnvelope{})
	l, err = q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	destEnv, _ := Decode(l[0].Body)
	assert.Equal(t, MaxChainDepth, destEnv.Depth)

	// A forged envelope with a corrupt depth (negative, or one that overflows on
	// +1) must still be capped — the guard rejects any next <= 0 (SEC hardening).
	for _, badDepth := range []int{-1, math.MaxInt} {
		d.fireDestination(context.Background(), dest, badDepth, ResultEnvelope{})
		l, err = q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
		require.NoError(t, err)
		assert.Emptyf(t, l, "corrupt depth %d must be dropped, not enqueued", badDepth)
	}
}

// TestBuildResultFlagsTruncationAndOmission proves the result envelope flags a
// truncated response and an omitted (over-cap) request body, so a destination can
// tell partial/elided from empty.
func TestBuildResultFlagsTruncationAndOmission(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	now := time.Unix(1_000_000, 0)

	big := bytes.Repeat([]byte("a"), MaxPayloadBytes+1)
	env := Envelope{Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: big}
	_, msg := leaseOne(t, q, env)
	re := d.buildResult(env, msg, ConditionSuccess, DeliveryResult{StatusCode: 200, Body: []byte("partial"), BodyTruncated: true})

	assert.True(t, re.RequestPayloadOmitted, "over-cap request body is omitted and flagged")
	assert.Nil(t, re.RequestPayload, "omitted request payload is not embedded")
	assert.True(t, re.ResponseContext.Truncated, "truncated response is flagged")

	// A within-cap body is embedded whole and unflagged.
	small := Envelope{Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: []byte("ok")}
	_, msg2 := leaseOne(t, q, small)
	re2 := d.buildResult(small, msg2, ConditionSuccess, DeliveryResult{StatusCode: 200, Body: []byte("resp")})
	assert.False(t, re2.RequestPayloadOmitted)
	assert.Equal(t, []byte("ok"), re2.RequestPayload)
	assert.False(t, re2.ResponseContext.Truncated)
}

// ackErrorQueue errors every Ack and counts Enqueue calls, to prove a stale ack
// does not fire the OnSuccess destination (invariant A3).
type ackErrorQueue struct {
	statestore.Queue
	enqueues atomic.Int64
}

func (q *ackErrorQueue) Ack(context.Context, string) error { return statestore.ErrInvalidReceipt }
func (q *ackErrorQueue) Enqueue(context.Context, string, statestore.Message, statestore.EnqueueOptions) (string, error) {
	q.enqueues.Add(1)
	return "x", nil
}

func TestProcessStaleAckDoesNotFireDestination(t *testing.T) {
	t.Parallel()
	q := &ackErrorQueue{}
	now := time.Unix(1_000_000, 0)
	d := destDispatcher(q, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now, resolverFor(FunctionConfig{}))
	d.process(context.Background(), leasedMsg(t, Envelope{
		EnqueueTime: now, OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next"},
	}, 1))
	assert.Zero(t, q.enqueues.Load(), "a stale/failed ack must not fire the destination (A3)")
}

func TestFireDestinationResolverNotFound(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	notFound := func(context.Context, string, string) (FunctionConfig, bool) { return FunctionConfig{}, false }
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), notFound)
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", FunctionName: "gone"}, 0, ResultEnvelope{})
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l, "a destination to a missing function is dropped")
}

// publishRecorder is a scripted TopicPublishFunc capturing every publish.
type publishRecorder struct {
	mu    sync.Mutex
	calls []publishCall
	err   error
}

type publishCall struct {
	namespace, mqType, topic, contentType string
	payload                               []byte
}

func (p *publishRecorder) publish(_ context.Context, namespace, mqType, topic, contentType string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, publishCall{namespace, mqType, topic, contentType, payload})
	return p.err
}

func (p *publishRecorder) recorded() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishCall(nil), p.calls...)
}

// TestFireDestinationTopicPublishesStatestore: a statestore topic destination
// publishes the result envelope to the namespaced topic — a LEAF (nothing is
// enqueued, no chain continues).
func TestFireDestinationTopicPublishesStatestore(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	rec := &publishRecorder{}
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	d.publishFn = rec.publish

	result := ResultEnvelope{
		Version:         EnvelopeVersion,
		RequestContext:  RequestContext{InvocationID: "id-1", FunctionRef: "ns/src", Condition: ConditionSuccess, Attempts: 1},
		ResponseContext: ResponseContext{StatusCode: 200},
	}
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "orders", MQType: MQTypeStatestore}, 0, result)

	calls := rec.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "ns", calls[0].namespace)
	assert.Equal(t, MQTypeStatestore, calls[0].mqType)
	assert.Equal(t, "orders", calls[0].topic)
	assert.Equal(t, "application/json", calls[0].contentType)
	var decoded ResultEnvelope
	require.NoError(t, json.Unmarshal(calls[0].payload, &decoded))
	assert.Equal(t, "id-1", decoded.RequestContext.InvocationID, "the payload is the result envelope")

	// A topic is a leaf: nothing was enqueued.
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l)
}

// TestFireDestinationTopicUnsupported: broker types (admission-rejected, so only
// a forged/legacy envelope carries one) and a nil publisher both drop the
// destination without publishing or enqueuing.
func TestFireDestinationTopicUnsupported(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	rec := &publishRecorder{}
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	d.publishFn = rec.publish

	// Broker type → dropped, not published.
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "t", MQType: "kafka"}, 0, ResultEnvelope{})
	assert.Empty(t, rec.recorded(), "broker topic types are unsupported until the egress phase")

	// No publisher wired (feature off) → statestore topics drop too.
	d.publishFn = nil
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "t", MQType: MQTypeStatestore}, 0, ResultEnvelope{})

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l, "topic destinations never enqueue")
}

// TestFireDestinationTopicPublishError: a failing publish is dropped (best-effort
// destination contract) without panicking or enqueuing.
func TestFireDestinationTopicPublishError(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	rec := &publishRecorder{err: errors.New("store down")}
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	d.publishFn = rec.publish

	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "t", MQType: MQTypeStatestore}, 0, ResultEnvelope{})
	require.Len(t, rec.recorded(), 1, "the publish was attempted")
	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l)
}
