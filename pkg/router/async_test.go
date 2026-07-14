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
