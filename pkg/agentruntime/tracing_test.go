// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// tracing_test.go covers the invoke_agent span (dispatcher.go), the
// wake-envelope trace stitching (wake.go), and the agentruntime mux's server
// spans (main.go's otelUtils.GetHandlerWithOTEL wrap, exercised here directly
// against a hand-built mux mirroring that wiring).
//
// Test seams (Global Constraints): the package-wide W3C composite propagator
// is installed EXACTLY ONCE in TestMain below — the process default is a
// no-op TextMapPropagator, so any test relying on it would pass vacuously
// (Extract/Inject would silently no-op). Per-test span assertions thread an
// explicit sdktrace.NewTracerProvider + tracetest.SpanRecorder through
// Dispatcher.SetTracerProvider (mirrors SetWakeEnqueuer's construction-cycle
// seam) rather than touching the global TracerProvider — safe under
// t.Parallel(). The two tests that exercise the SERVER span (otelhttp has no
// GetHandlerWithOTEL provider option) are the deliberate exception: they
// swap otel.SetTracerProvider for their duration (restored via t.Cleanup)
// and are NOT t.Parallel() for that reason — see withGlobalTracerProvider.
package agentruntime

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// TestMain installs the W3C composite propagator (tracecontext + baggage)
// ONCE for the whole package's test binary. Without this the process default
// is otel's no-op propagator: Inject writes nothing and Extract reads
// nothing, so any stitch/inject assertion below would pass vacuously
// regardless of whether dispatcher.go/wake.go's carriage logic is even
// wired correctly. The same propagator value is used by every test — safe
// under t.Parallel() since nothing here mutates it again.
func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	os.Exit(m.Run())
}

// newTestTracerProvider returns an SDK TracerProvider recording every
// started/ended span into rec, with AlwaysSample so a span parented on an
// extracted remote SpanContext is recorded regardless of that context's
// carried sampled-flag.
func newTestTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, rec
}

// withGlobalTracerProvider temporarily swaps the GLOBAL otel TracerProvider
// to tp, restoring the previous one on cleanup. Only the two server-span
// tests need this: otelUtils.GetHandlerWithOTEL (main.go's precedent) has no
// per-call TracerProvider option, so otelhttp's middleware always resolves
// the server span's tracer from otel.GetTracerProvider() at request time
// (see otelhttp's handler.go serveHTTP — h.tracer is nil, and the incoming
// request carries no live span object on its own context, so it falls back
// to the global). Callers of this helper must NOT call t.Parallel().
func withGlobalTracerProvider(t *testing.T, tp trace.TracerProvider) {
	t.Helper()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
}

// findSpan returns the first ended span whose name starts with prefix.
func findSpan(spans []sdktrace.ReadOnlySpan, prefix string) (sdktrace.ReadOnlySpan, bool) {
	for _, s := range spans {
		if strings.HasPrefix(s.Name(), prefix) {
			return s, true
		}
	}
	return nil, false
}

// attrValue returns the value of key among attrs, if present.
func attrValue(attrs []attribute.KeyValue, key attribute.Key) (attribute.Value, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// TestDispatchTurn_ServerSpanAndInvokeAgentChild drives one HTTP turn
// through the REAL mux shape main.go wires (dispatcher.Handler() wrapped in
// otelUtils.GetHandlerWithOTEL, the router-public precedent) and asserts: a
// "fission-agentruntime"-scoped server span exists, an "invoke_agent fn"
// span exists as ITS CHILD (same trace-id, Parent().SpanID() equals the
// server span's SpanID), the invoke_agent span carries the pinned
// gen_ai.*/fission.* attributes plus fission.yield on completion, ends
// with an Ok status, and — critically — is ended EXACTLY ONCE.
func TestDispatchTurn_ServerSpanAndInvokeAgentChild(t *testing.T) {
	// Not t.Parallel(): withGlobalTracerProvider mutates global otel state.
	tp, rec := newTestTracerProvider(t)
	withGlobalTracerProvider(t, tp)

	upstream := okUpstream(t)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	mux := http.NewServeMux()
	mux.Handle("POST /agents/{namespace}/{name}", d.Handler())
	// Filter list kept in sync with main.go's (4 entries); this test only
	// exercises the dispatch route, but drift here masked a real gap once.
	handler := otelUtils.GetHandlerWithOTEL(mux, "fission-agentruntime", otelUtils.UrlsToIgnore("/registry/events", "/ui", "/healthz", "/readyz"))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-span")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	require.Equal(t, http.StatusOK, rec2.Code)

	ended := rec.Ended()
	serverSpan, ok := findSpan(ended, "POST ")
	require.True(t, ok, "expected an otelhttp server span, got spans: %v", spanNames(ended))
	invokeSpan, ok := findSpan(ended, "invoke_agent ")
	require.True(t, ok, "expected an invoke_agent span, got spans: %v", spanNames(ended))

	assert.Equal(t, "invoke_agent fn", invokeSpan.Name())
	assert.Equal(t, serverSpan.SpanContext().TraceID(), invokeSpan.SpanContext().TraceID(), "invoke_agent must share the server span's trace")
	assert.Equal(t, serverSpan.SpanContext().SpanID(), invokeSpan.Parent().SpanID(), "invoke_agent must be a CHILD of the server span")

	attrs := invokeSpan.Attributes()
	if v, ok := attrValue(attrs, "gen_ai.operation.name"); assert.True(t, ok) {
		assert.Equal(t, "invoke_agent", v.AsString())
	}
	if v, ok := attrValue(attrs, "gen_ai.agent.name"); assert.True(t, ok) {
		assert.Equal(t, "fn", v.AsString())
	}
	if v, ok := attrValue(attrs, "fission.agent.namespace"); assert.True(t, ok) {
		assert.Equal(t, "ns", v.AsString())
	}
	if v, ok := attrValue(attrs, "fission.session.id"); assert.True(t, ok) {
		assert.Equal(t, "sess-span", v.AsString())
	}
	if v, ok := attrValue(attrs, "fission.turn.source"); assert.True(t, ok) {
		assert.Equal(t, "http", v.AsString())
	}
	if v, ok := attrValue(attrs, "fission.turn"); assert.True(t, ok) {
		assert.EqualValues(t, 0, v.AsInt64(), "turnsAtStart for a freshly-minted session is 0")
	}
	if v, ok := attrValue(attrs, "fission.yield"); assert.True(t, ok) {
		assert.Equal(t, "", v.AsString(), "okUpstream sets no X-Fission-Agent-Yield header")
	}
	assert.Equal(t, codes.Ok, invokeSpan.Status().Code)

	// Ended exactly once: exactly one span named "invoke_agent fn" in the
	// whole recording, not two (a double-End, or a re-parented duplicate).
	count := 0
	for _, s := range ended {
		if s.Name() == "invoke_agent fn" {
			count++
		}
	}
	assert.Equal(t, 1, count, "invoke_agent span must be ended exactly once")
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

// TestDispatchTurn_SpanEndsExactlyOnceOnEarlyReturn drives the ErrNotAgent
// early-return path (DispatchTurn's very first branch, entry lookup miss) —
// the earliest possible return, before bgCtx is even derived — and asserts
// the invoke_agent span is STILL ended exactly once (the `defer span.End()`
// placed before that branch covers it) with core identity attributes set,
// even though the turn never reached the session-record or upstream-call
// stages. Uses the Dispatcher-local tracer-provider seam only, so this is
// safe under t.Parallel().
func TestDispatchTurn_SpanEndsExactlyOnceOnEarlyReturn(t *testing.T) {
	t.Parallel()
	tp, rec := newTestTracerProvider(t)
	d, _, _ := newTestDispatcher(t, "http://unused.invalid")
	d.SetTracerProvider(tp)
	// Deliberately no view.Upsert: (ns, fn) is not a registered agent.

	tr := TurnRequest{Namespace: "ns", Agent: "fn", SessionID: "sess-notfound"}
	_, err := d.DispatchTurn(t.Context(), tr, &wakeSink{})
	require.ErrorIs(t, err, ErrNotAgent)

	ended := rec.Ended()
	require.Len(t, ended, 1, "exactly one span must be ended on the earliest possible return")
	span := ended[0]
	assert.Equal(t, "invoke_agent fn", span.Name())
	if v, ok := attrValue(span.Attributes(), "fission.session.id"); assert.True(t, ok) {
		assert.Equal(t, "sess-notfound", v.AsString())
	}
	if v, ok := attrValue(span.Attributes(), "fission.turn.source"); assert.True(t, ok) {
		assert.Equal(t, "http", v.AsString())
	}
	// No fission.turn/fission.yield/explicit status: the turn never reached
	// the bookkeeping or upstream-call stages that set those (see
	// DispatchTurn's doc comment on status placement).
	assert.Equal(t, codes.Unset, span.Status().Code)
}

// TestDispatchTurn_TransportFailureSetsErrorStatus pins the status split the
// plan calls out explicitly: Error on a transport failure, Ok otherwise.
func TestDispatchTurn_TransportFailureSetsErrorStatus(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	tp, rec := newTestTracerProvider(t)
	d, view, _ := newTestDispatcher(t, deadURL)
	d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	tr := TurnRequest{Namespace: "ns", Agent: "fn", SessionID: "sess-transport"}
	_, err := d.DispatchTurn(t.Context(), tr, &wakeSink{})
	require.Error(t, err)

	ended := rec.Ended()
	require.Len(t, ended, 1)
	assert.Equal(t, codes.Error, ended[0].Status().Code)
}

// TestWakeStitching_ContinuationSharesEnqueuingTrace drives yield=continue
// END-TO-END: DispatchTurn's own shared-path enqueue (SetWakeEnqueuer wired,
// production shape) produces the wake envelope actually sitting in the
// queue — never a hand-built one — and delivering it must produce a SECOND
// invoke_agent span sharing the FIRST span's trace-id (a different span-id:
// children re-span, they never pass through unchanged).
func TestWakeStitching_ContinuationSharesEnqueuingTrace(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	tp, rec := newTestTracerProvider(t)
	w, view, _, q := newTestWakeServiceFull(t, http.DefaultClient, upstream.URL, time.Now, 0, true)
	w.d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	tr := TurnRequest{Namespace: "ns", Agent: "fn", SessionID: "sess-stitch"}
	res, err := w.d.DispatchTurn(t.Context(), tr, &wakeSink{})
	require.NoError(t, err)
	require.Equal(t, YieldContinue, res.Yield)
	require.True(t, res.ContinuationAllowed)

	enqueuing, ok := findSpan(rec.Ended(), "invoke_agent ")
	require.True(t, ok)
	enqueuingTraceID := enqueuing.SpanContext().TraceID()
	enqueuingSpanID := enqueuing.SpanContext().SpanID()

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "the shared path must have enqueued the continuation")

	var env wakeEnvelope
	require.NoError(t, json.Unmarshal(msgs[0].Body, &env))
	require.NotEmpty(t, env.Trace, "EnqueueWake must have injected the enqueuing turn's trace context")

	w.process(t.Context(), msgs[0])

	ended := rec.Ended()
	require.Len(t, ended, 2, "the delivered continuation must produce a second invoke_agent span")
	var continuation sdktrace.ReadOnlySpan
	for _, s := range ended {
		if s.SpanContext().SpanID() != enqueuingSpanID && strings.HasPrefix(s.Name(), "invoke_agent ") {
			continuation = s
		}
	}
	require.NotNil(t, continuation, "expected a distinct second invoke_agent span")
	assert.Equal(t, enqueuingTraceID, continuation.SpanContext().TraceID(), "the continuation must share the enqueuing turn's trace id")
	assert.NotEqual(t, enqueuingSpanID, continuation.SpanContext().SpanID(), "children re-span — the span-id must differ")
	assert.Equal(t, enqueuingSpanID, continuation.Parent().SpanID(), "the continuation must be a DIRECT CHILD of the enqueuing turn's still-open invoke_agent span (bgCtx carries it live into EnqueueWake), not just trace-id-adjacent")
	if v, ok := attrValue(continuation.Attributes(), "fission.turn.source"); assert.True(t, ok) {
		assert.Equal(t, "wake", v.AsString())
	}
}

// TestWakeStitching_AbsentTraceFieldIsRootSpan pins the backward-compat
// fallback: a wake envelope with no Trace field (the zero value — a
// pre-observability envelope, or an EnqueueWake call whose ctx carried no
// span) must still deliver correctly, producing a fresh ROOT span rather
// than an error or a silently-broken delivery.
func TestWakeStitching_AbsentTraceFieldIsRootSpan(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	tp, rec := newTestTracerProvider(t)
	w, view, _, q := newTestWakeService(t, upstream.URL, time.Now)
	w.d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	// EnqueueWake called with a context carrying no span: the injected
	// carrier is empty, so env.Trace stays nil — exactly the pre-
	// observability shape.
	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-root", 0))
	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	var env wakeEnvelope
	require.NoError(t, json.Unmarshal(msgs[0].Body, &env))
	require.Empty(t, env.Trace, "a context with no active span must inject nothing")

	w.process(t.Context(), msgs[0])

	ended := rec.Ended()
	require.Len(t, ended, 1)
	span := ended[0]
	assert.False(t, span.Parent().IsValid(), "no Trace field on the envelope must produce a ROOT span, not an error")
	if v, ok := attrValue(span.Attributes(), "fission.turn.source"); assert.True(t, ok) {
		assert.Equal(t, "wake", v.AsString())
	}
}

// TestWakeRevival_CarriesDeliveryTrace forces the chain-death race
// reviveChain exists to close (SetWakeEnqueuer deliberately never wired, so
// the shared path CANNOT enqueue — see
// TestWakeService_AckedContinueRevivesChainWithoutSharedPathEnqueuer for the
// non-tracing version of this shape) and asserts the revival's own enqueue
// (wake.go's reviveChain, via withDeliveryTrace) carries the SAME trace-id
// as the delivery's extracted trace — the plan-review pin that a
// poll-loop-derived settle ctx must not silently drop it.
func TestWakeRevival_CarriesDeliveryTrace(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	tp, _ := newTestTracerProvider(t)
	w, view, _, q := newTestWakeService(t, upstream.URL, time.Now) // wireEnqueuer=false
	w.d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	// Seed a trace context to inject into the parent envelope, standing in
	// for whatever turn originally enqueued it.
	seedCtx, seedSpan := tp.Tracer("seed").Start(t.Context(), "seed")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(seedCtx, carrier)
	seedSpan.End()
	wantTraceID := seedSpan.SpanContext().TraceID()
	require.NotEmpty(t, carrier)

	enqueueWakeEnvelope(t, q, wakeEnvelope{
		Version: wakeEnvelopeVersion, Namespace: "ns", Agent: "fn", Session: "sess-revive",
		Turns: 0, EnqueueTime: time.Now(), Trace: map[string]string(carrier),
	})

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	w.process(t.Context(), msgs[0])

	// The shared path could not enqueue (wireEnqueuer=false), so any
	// successor found here came from reviveChain alone.
	successors, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, successors, 1, "reviveChain must have enqueued a successor")

	var succ wakeEnvelope
	require.NoError(t, json.Unmarshal(successors[0].Body, &succ))
	require.NotEmpty(t, succ.Trace, "the revival enqueue must carry the delivery's extracted trace")

	gotCtx := otel.GetTextMapPropagator().Extract(t.Context(), propagation.MapCarrier(succ.Trace))
	sc := trace.SpanContextFromContext(gotCtx)
	require.True(t, sc.IsValid())
	assert.Equal(t, wantTraceID, sc.TraceID(), "the revival enqueue must carry the SAME trace-id as the delivery's extracted trace")
}

// TestWakeDedup_TraceFieldExcludedFromKey pins wakeDedupKey's exclusion of
// Trace: two enqueues of the identical logical continuation, injected from
// DIFFERENT active spans, must still collapse to one unsettled message (the
// dedup key is ns/agent/session/turns only) — and the FIRST enqueue's trace
// must be the one that survives, since a deduped EnqueueWake call never
// rewrites an existing message body.
func TestWakeDedup_TraceFieldExcludedFromKey(t *testing.T) {
	t.Parallel()
	tp, _ := newTestTracerProvider(t)
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)

	ctx1, span1 := tp.Tracer("t1").Start(t.Context(), "first")
	require.NoError(t, w.EnqueueWake(ctx1, "ns", "fn", "sess", 3))
	span1.End()

	ctx2, span2 := tp.Tracer("t2").Start(t.Context(), "second")
	require.NoError(t, w.EnqueueWake(ctx2, "ns", "fn", "sess", 3))
	span2.End()

	st, err := q.Stats(t.Context(), wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Visible, "the trace field must not affect the dedup key")

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	var env wakeEnvelope
	require.NoError(t, json.Unmarshal(msgs[0].Body, &env))
	gotCtx := otel.GetTextMapPropagator().Extract(t.Context(), propagation.MapCarrier(env.Trace))
	sc := trace.SpanContextFromContext(gotCtx)
	require.True(t, sc.IsValid())
	assert.Equal(t, span1.SpanContext().TraceID(), sc.TraceID(), "the FIRST enqueue's trace must win the dedup collapse")
}

// TestDispatchTurn_OutboundRequestCarriesTraceparent proves the whole point
// of starting the span before the outbound call: the router-internal
// request's traceparent header must carry the SAME trace-id as the inbound
// turn, via the otelhttp transport that main.go wires onto the dispatcher's
// client (main.go:460).
func TestDispatchTurn_OutboundRequestCarriesTraceparent(t *testing.T) {
	t.Parallel()
	var gotTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	tp, rec := newTestTracerProvider(t)
	client := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(tp))}
	view := NewAgentView()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	d := NewDispatcher(logr.Discard(), view, store, client, upstream.URL, time.Hour, time.Now, 0, defaultMaxSpawnDepth)
	d.SetTracerProvider(tp)
	view.Upsert(headerEntry("ns", "fn", 0))

	tr := TurnRequest{Namespace: "ns", Agent: "fn", SessionID: "sess-outbound"}
	_, err := d.DispatchTurn(t.Context(), tr, &wakeSink{})
	require.NoError(t, err)

	require.NotEmpty(t, gotTraceparent, "the outbound request must carry a traceparent header")
	parts := strings.Split(gotTraceparent, "-")
	require.Len(t, parts, 4, "traceparent must be 00-<trace-id>-<span-id>-<flags>")

	invokeSpan, ok := findSpan(rec.Ended(), "invoke_agent ")
	require.True(t, ok)
	assert.Equal(t, invokeSpan.SpanContext().TraceID().String(), parts[1], "outbound traceparent trace-id must match the invoke_agent span's")
	assert.NotEqual(t, invokeSpan.SpanContext().SpanID().String(), parts[2], "the outbound request re-spans under otelhttp's own client span, not the server span's id")
}

// TestAgentruntimeMux_FiltersSSEAndUIFromServerSpans pins the mux-wrap
// filter (main.go): /registry/events, /ui (both the bare route and
// everything under /ui/, since otelUtils.UrlsToIgnore is HasPrefix), and the
// kubelet probe routes /healthz + /readyz must all produce NO server span,
// while an ordinary route still does — proving the filter engaged rather
// than the whole pipeline being silently dead.
func TestAgentruntimeMux_FiltersSSEAndUIFromServerSpans(t *testing.T) {
	// Not t.Parallel(): withGlobalTracerProvider mutates global otel state.
	tp, rec := newTestTracerProvider(t)
	withGlobalTracerProvider(t, tp)

	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /registry/events", ok)
	mux.HandleFunc("GET /ui", ok)
	mux.HandleFunc("GET /ui/", ok)
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	// A control route NOT in the filter list, mirroring another route
	// main.go actually registers unfiltered — proves the filter mechanism
	// itself is what suppressed the others, not a dead otel pipeline.
	mux.HandleFunc("GET /registry/agents", ok)
	handler := otelUtils.GetHandlerWithOTEL(mux, "fission-agentruntime", otelUtils.UrlsToIgnore("/registry/events", "/ui", "/healthz", "/readyz"))

	for _, path := range []string{"/registry/events", "/ui", "/ui/console", "/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, path)
	}
	assert.Empty(t, rec.Ended(), "filtered routes (including the kubelet probes) must produce NO server span")

	req := httptest.NewRequest(http.MethodGet, "/registry/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, rec.Ended(), 1, "an un-filtered route must still produce a server span — proves the filter, not the pipeline, is what suppressed the others")
}
