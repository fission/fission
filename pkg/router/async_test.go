// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

func routerMemQueue(t *testing.T) statestore.Queue {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	q, err := caps.Queue()
	require.NoError(t, err)
	return q
}

func TestAsyncInvokerHandleAccepted(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}, Spec: fv1.FunctionSpec{FunctionTimeout: 30}}

	r := httptest.NewRequest("POST", "/x?a=1", strings.NewReader("payload"))
	r.Header.Set(asyncinvoke.HeaderDedupKey, "dk")
	w := httptest.NewRecorder()
	inv.handle(w, r, fn)

	require.Equal(t, 202, w.Code)
	id := w.Header().Get(asyncinvoke.HeaderInvocationID)
	require.NotEmpty(t, id)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, id, body["invocationId"])

	// The message is durably enqueued with the request faithfully captured.
	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	env, err := asyncinvoke.Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, "ns", env.Namespace)
	assert.Equal(t, "fn", env.Function)
	assert.Equal(t, 30, env.FunctionTimeout)
	assert.Equal(t, []byte("payload"), env.Body)
}

// TestAsyncInvokerHandleStampsFunctionVersion pins the RFC-0025 Task 5
// enqueue-side stamp: when the resolved backend fn carries
// fv1.FUNCTION_VERSION (versioning.VersionedFunction's label, set for any
// alias- or version-pin resolve), handle() stamps it into the durable
// envelope's FunctionVersion. An unversioned (bare-name) fn — no label —
// stamps nothing, byte-identical to pre-Task-5 envelopes.
func TestAsyncInvokerHandleStampsFunctionVersion(t *testing.T) {
	t.Parallel()

	t.Run("versioned resolve stamps FunctionVersion", func(t *testing.T) {
		t.Parallel()
		q := routerMemQueue(t)
		inv := &asyncInvoker{queue: q, logger: logr.Discard()}
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: "hello", Namespace: "ns",
				Labels: map[string]string{fv1.FUNCTION_VERSION: "hello-v1"},
			},
		}
		r := httptest.NewRequest("POST", "/x", strings.NewReader("p"))
		w := httptest.NewRecorder()
		inv.handle(w, r, fn)
		require.Equal(t, 202, w.Code)

		l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		env, err := asyncinvoke.Decode(l[0].Body)
		require.NoError(t, err)
		assert.Equal(t, "hello-v1", env.FunctionVersion)
	})

	t.Run("unversioned resolve stamps nothing", func(t *testing.T) {
		t.Parallel()
		q := routerMemQueue(t)
		inv := &asyncInvoker{queue: q, logger: logr.Discard()}
		fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "ns"}}
		r := httptest.NewRequest("POST", "/x", strings.NewReader("p"))
		w := httptest.NewRecorder()
		inv.handle(w, r, fn)
		require.Equal(t, 202, w.Code)

		l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		env, err := asyncinvoke.Decode(l[0].Body)
		require.NoError(t, err)
		assert.Empty(t, env.FunctionVersion)
	})
}

func TestAsyncInvokerHandleDisabled501(t *testing.T) {
	t.Parallel()
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}

	// nil queue → disabled.
	invNilQueue := &asyncInvoker{logger: logr.Discard()}
	w := httptest.NewRecorder()
	invNilQueue.handle(w, httptest.NewRequest("POST", "/x", nil), fn)
	assert.Equal(t, 501, w.Code)

	// nil invoker (feature entirely off) → 501 via nil-safe receiver.
	var invNil *asyncInvoker
	w2 := httptest.NewRecorder()
	invNil.handle(w2, httptest.NewRequest("POST", "/x", nil), fn)
	assert.Equal(t, 501, w2.Code)
}

func TestAsyncInvokerHandleBodyTooLarge413(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}

	big := strings.Repeat("a", asyncinvoke.DefaultMaxBodyBytes+1)
	w := httptest.NewRecorder()
	inv.handle(w, httptest.NewRequest("POST", "/x", strings.NewReader(big)), fn)
	assert.Equal(t, 413, w.Code)

	// Nothing enqueued.
	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, l)
}

// TestHandlerAsyncBranchPublicOnly asserts the function handler enqueues on the
// public listener (httpTrigger != nil) when the async header is set.
func TestHandlerAsyncBranchPublicOnly(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}

	fh := functionHandler{
		logger:       logr.Discard(),
		function:     fn,
		httpTrigger:  &fv1.HTTPTrigger{}, // public handler
		asyncInvoker: inv,
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader("body"))
	r.Header.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	w := httptest.NewRecorder()
	fh.handler(w, r)

	assert.Equal(t, 202, w.Code, "public handler enqueues async")
	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
}

// TestAsyncRequested pins the async-vs-proxy decision across the public trigger
// path, the internal direct-function path, and the dispatcher-delivery exclusion.
func TestAsyncRequested(t *testing.T) {
	t.Parallel()
	publicTrigger := &fv1.HTTPTrigger{}
	triggerModeAsync := &fv1.HTTPTrigger{Spec: fv1.HTTPTriggerSpec{InvocationMode: asyncinvoke.InvokeModeAsync}}

	cases := []struct {
		name    string
		trigger *fv1.HTTPTrigger // nil = internal direct-function handler
		mode    string           // X-Fission-Invoke-Mode header
		invID   string           // X-Fission-Invocation-Id (set ⇒ dispatcher delivery)
		want    bool
	}{
		{"direct async header enqueues", nil, "async", "", true},
		{"public async header enqueues", publicTrigger, "async", "", true},
		{"trigger invocationMode enqueues", triggerModeAsync, "", "", true},
		{"no async signal proxies", nil, "", "", false},
		// Internal path: the dispatcher's own delivery (invocation-id set) proxies sync.
		{"internal dispatcher delivery never enqueues", nil, "async", "inv-1", false},
		// Public path: a user-spoofed invocation-id must NOT bypass async — the guard
		// is internal-only (the dispatcher never delivers on the public path).
		{"public spoofed invocation-id can't bypass trigger mode", triggerModeAsync, "", "inv-1", true},
		{"public spoofed invocation-id can't bypass async header", publicTrigger, "async", "inv-1", true},
		{"case-insensitive header", nil, "ASYNC", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fh := functionHandler{httpTrigger: tc.trigger}
			r := httptest.NewRequest("POST", "/x", nil)
			if tc.mode != "" {
				r.Header.Set(asyncinvoke.HeaderInvokeMode, tc.mode)
			}
			if tc.invID != "" {
				r.Header.Set(asyncinvoke.HeaderInvocationID, tc.invID)
			}
			assert.Equal(t, tc.want, fh.asyncRequested(r))
		})
	}
}

// TestHandlerAsyncBranchDirectPath asserts the internal direct-function handler
// (no httpTrigger) enqueues an async-header request — the `fission function test
// --async` path — while a dispatcher delivery (X-Fission-Invocation-Id set) is NOT
// enqueued.
func TestHandlerAsyncBranchDirectPath(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}

	// Direct path, async header, no invocation id → enqueues.
	fh := functionHandler{logger: logr.Discard(), function: fn, asyncInvoker: inv} // httpTrigger nil
	r := httptest.NewRequest("POST", "/fission-function/ns/fn", strings.NewReader("body"))
	r.Header.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	w := httptest.NewRecorder()
	fh.handler(w, r)
	assert.Equal(t, 202, w.Code, "direct caller enqueues async")

	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1, "exactly one message enqueued")
}

// TestHandlerAsyncBranchTriggerMode asserts a trigger with
// spec.invocationMode=async enqueues even without the X-Fission-Invoke-Mode header
// (for callers that cannot set headers).
func TestHandlerAsyncBranchTriggerMode(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}

	fh := functionHandler{
		logger:       logr.Discard(),
		function:     fn,
		httpTrigger:  &fv1.HTTPTrigger{Spec: fv1.HTTPTriggerSpec{InvocationMode: "async"}},
		asyncInvoker: inv,
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader("body")) // no async header
	w := httptest.NewRecorder()
	fh.handler(w, r)

	assert.Equal(t, 202, w.Code, "trigger invocationMode=async enqueues without the header")
	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
}

func TestDestinationsFromSpec(t *testing.T) {
	t.Parallel()
	onS, onF := destinationsFromSpec(nil, "ns")
	assert.Nil(t, onS)
	assert.Nil(t, onF)

	ic := &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "next"}},
		OnFailure: &fv1.DestinationRef{Topic: &fv1.TopicRef{MessageQueueType: fv1.MessageQueueTypeStatestore, Topic: "errs"}},
	}
	onS, onF = destinationsFromSpec(ic, "ns")
	require.NotNil(t, onS)
	assert.Equal(t, "ns", onS.FunctionNamespace, "function destination inherits the source namespace")
	assert.Equal(t, "next", onS.FunctionName)
	assert.True(t, onS.IsFunction())
	require.NotNil(t, onF)
	assert.Equal(t, "ns", onF.FunctionNamespace, "topic destination inherits the source namespace too (RFC-0027)")
	assert.Equal(t, "errs", onF.Topic)
	assert.Equal(t, fv1.MessageQueueTypeStatestore, onF.MQType)
	assert.True(t, onF.IsTopic())
}

// TestDestinationsFromSpec_AliasVersion pins the RFC-0025 Task 5 mapping: a
// function destination's Alias/Version pin (fv1.FunctionReference) carries
// through to the envelope-side Destination's own Alias/Version fields.
func TestDestinationsFromSpec_AliasVersion(t *testing.T) {
	t.Parallel()
	ic := &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{
			Type: fv1.FunctionReferenceTypeFunctionName, Name: "next", Version: "next-v3",
		}},
		OnFailure: &fv1.DestinationRef{Function: &fv1.FunctionReference{
			Type: fv1.FunctionReferenceTypeFunctionName, Name: "handler", Alias: "prod",
		}},
	}
	onS, onF := destinationsFromSpec(ic, "ns")
	require.NotNil(t, onS)
	assert.Equal(t, "next-v3", onS.Version)
	assert.Empty(t, onS.Alias)
	require.NotNil(t, onF)
	assert.Equal(t, "prod", onF.Alias)
	assert.Empty(t, onF.Version)
}

func TestPolicyFromSpec(t *testing.T) {
	t.Parallel()
	assert.Equal(t, asyncinvoke.Policy{}, policyFromSpec(nil), "nil config → zero policy")

	jitterOff := false
	ic := &fv1.InvocationConfig{
		Retry: fv1.RetryPolicy{
			MaxAttempts: new(7),
			BackoffBase: &metav1.Duration{Duration: 2 * time.Second},
			BackoffCap:  &metav1.Duration{Duration: time.Minute},
			Jitter:      &jitterOff,
		},
		MaxAge: &metav1.Duration{Duration: 3 * time.Hour},
	}
	got := policyFromSpec(ic)
	assert.Equal(t, 7, got.MaxAttempts)
	assert.Equal(t, 2*time.Second, got.BackoffBase)
	assert.Equal(t, time.Minute, got.BackoffCap)
	assert.Equal(t, 3*time.Hour, got.MaxAge)
	assert.True(t, got.NoJitter, "Jitter:false → NoJitter:true")
}

// TestAsyncInvokerHandleDedup asserts the handler wires X-Fission-Dedup-Key
// through to Enqueue: two async requests with the same key collapse to one
// durable invocation (same id, one queued message).
func TestAsyncInvokerHandleDedup(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}
	post := func() string {
		r := httptest.NewRequest("POST", "/x", strings.NewReader("p"))
		r.Header.Set(asyncinvoke.HeaderDedupKey, "same")
		w := httptest.NewRecorder()
		inv.handle(w, r, fn)
		require.Equal(t, 202, w.Code)
		return w.Header().Get(asyncinvoke.HeaderInvocationID)
	}
	id1, id2 := post(), post()
	assert.Equal(t, id1, id2, "same dedup key → same invocation id")

	st, err := q.Stats(t.Context(), asyncinvoke.DefaultQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Visible, "dedup collapses to a single queued message")
}

// TestAsyncInvokerHandleIgnoresCallerDepth asserts a public caller cannot seed the
// destination-chain depth: an X-Fission-Invocation-Depth header on the incoming
// request is not reflected into the enqueued envelope (it stays 0).
func TestAsyncInvokerHandleIgnoresCallerDepth(t *testing.T) {
	t.Parallel()
	q := routerMemQueue(t)
	inv := &asyncInvoker{queue: q, logger: logr.Discard()}
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "ns"}}
	r := httptest.NewRequest("POST", "/x", strings.NewReader("p"))
	r.Header.Set(asyncinvoke.HeaderInvocationDepth, "5")
	w := httptest.NewRecorder()
	inv.handle(w, r, fn)
	require.Equal(t, 202, w.Code)

	l, err := q.Lease(t.Context(), asyncinvoke.DefaultQueue, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	env, err := asyncinvoke.Decode(l[0].Body)
	require.NoError(t, err)
	assert.Equal(t, 0, env.Depth, "caller-supplied depth must not be trusted")
}
