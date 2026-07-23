// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestProcessFiresFunctionDestination_VersionPinned pins the RFC-0025
// Task 5 destination-invoke behavior for a Version pin: UNLIKE Alias, the
// version is NOT baked into the fired envelope's Function as a `:<version>`
// suffix -- it rides FunctionVersion instead (bare Function name), so the
// eventual delivery gets the deliverer's 404-fallback machinery rather than
// dead-lettering the first time ordinary retain-N GC removes that specific
// version's route (see Destination.Version's doc comment for why baking the
// suffix in directly would be wrong here).
func TestProcessFiresFunctionDestination_VersionPinned(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	now := time.Unix(1_000_000, 0)
	d := destDispatcher(q, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now,
		resolverFor(FunctionConfig{}))

	_, msg := leaseOne(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: []byte("orig"),
		OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next", Version: "next-v2"},
	})
	d.process(context.Background(), msg)

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	destEnv, err := Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, "next", destEnv.Function, "bare name -- the version is NOT suffixed onto Function")
	assert.Equal(t, "next-v2", destEnv.FunctionVersion, "the version pin rides FunctionVersion instead")
}

// TestProcessVersionPinnedDestinationDelivery_FallsBackNotDeadLettered is
// the end-to-end proof for the fix above: a destination-fired envelope whose
// pinned version's route has been GC'd (404 on the versioned URL) falls back
// to the bare-name route and SUCCEEDS -- acked, not killed/dead-lettered --
// exactly like a primary invocation's version-pinned 404 fallback
// (TestHTTPDelivererVersionPinned_FallsBackOnNotFound in deliverer_test.go).
// Before this fix, a Version-pinned destination's `:<version>` suffix was
// baked directly into Function with no fallback: any 404 on that route
// (routine, since retain-N GC does not track destination references) would
// dead-letter every single fire permanently.
func TestProcessVersionPinnedDestinationDelivery_FallsBackNotDeadLettered(t *testing.T) {
	t.Parallel()
	var attempts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts = append(attempts, r.URL.Path)
		if strings.Contains(r.URL.Path, ":") {
			w.WriteHeader(http.StatusNotFound) // the pinned version's route was GC'd
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	deliverer := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	d := newTestDispatcher(rq, deliverer, now)

	// The envelope fireDestination would enqueue for a Version-pinned
	// destination: bare Function, the pin on FunctionVersion.
	env := Envelope{EnqueueTime: now, Namespace: "ns", Function: "next", FunctionVersion: "next-vGONE"}
	d.process(context.Background(), leasedMsg(t, env, 1))

	require.Len(t, attempts, 2, "versioned attempt then bare fallback, within one process() call")
	assert.Contains(t, attempts[0], ":next-vGONE")
	assert.Equal(t, "/fission-function/ns/next", attempts[1])
	assert.Equal(t, []string{"receipt-x"}, rq.acks, "the fallback succeeded: acked")
	assert.Empty(t, rq.kills, "must NOT dead-letter a version pinned to a routinely GC'd route")
	assert.Empty(t, rq.nacks)
}

// TestProcessFiresFunctionDestination_AliasPinned mirrors the version case
// for an Alias-pinned destination.
func TestProcessFiresFunctionDestination_AliasPinned(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	now := time.Unix(1_000_000, 0)
	d := destDispatcher(q, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now,
		resolverFor(FunctionConfig{}))

	_, msg := leaseOne(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: []byte("orig"),
		OnSuccess: &Destination{FunctionNamespace: "ns", FunctionName: "next", Alias: "prod"},
	})
	d.process(context.Background(), msg)

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	destEnv, err := Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, "next:prod", destEnv.Function, "the fired envelope's Function carries the :<alias> suffix")
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
	env := Envelope{Version: EnvelopeVersion, Namespace: "ns", Function: "src", EnqueueTime: now, Body: big, Depth: 2}
	_, msg := leaseOne(t, q, env)
	re := d.buildResult(env, msg, ConditionSuccess, DeliveryResult{StatusCode: 200, Body: []byte("partial"), BodyTruncated: true})

	assert.Equal(t, 2, re.RequestContext.Depth, "the chain depth is stamped for future async consumers (no wire migration)")
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

// TestFireDestinationTopicUnsupported: the dispatcher forwards EVERY topic type
// to the publisher (which types have a publish path is the publisher's
// knowledge — egress phase); an ErrTopicUnsupported-wrapped failure and a nil
// publisher both drop the destination without enqueuing.
func TestFireDestinationTopicUnsupported(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	rec := &publishRecorder{err: fmt.Errorf("%w: no head for it", ErrTopicUnsupported)}
	d := destDispatcher(q, scriptedDeliverer{}, time.Unix(1, 0), resolverFor(FunctionConfig{}))
	d.publishFn = rec.publish

	// A type without a publish path reaches the publisher and is dropped on its
	// sentinel (classification is error-driven, not a dispatcher-local list).
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "t", MQType: "nats-jetstream"}, 0, ResultEnvelope{})
	require.Len(t, rec.recorded(), 1, "the publisher decides supportedness — the dispatcher must forward")

	// No publisher wired (feature off) → topics drop without a call.
	d.publishFn = nil
	d.fireDestination(context.Background(), &Destination{FunctionNamespace: "ns", Topic: "t", MQType: MQTypeStatestore}, 0, ResultEnvelope{})

	l, err := q.Lease(t.Context(), DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, l, "topic destinations never enqueue on the async queue")
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
