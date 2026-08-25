// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// tracing_test.go covers _meta trace-context extraction (tracing.go) and the
// execute_tool span (server.go's callTool), plus the /mcp mount's server span
// (main.go's otelUtils.GetHandlerWithOTEL wrap, exercised here directly
// against a hand-built handler mirroring that wiring).
//
// Test seams (Global Constraints, mirroring pkg/agentruntime/tracing_test.go):
// the package-wide W3C composite propagator is installed EXACTLY ONCE in
// TestMain below -- the process default is a no-op TextMapPropagator, so any
// test relying on it would pass vacuously (Extract/Inject would silently
// no-op). Per-test span assertions thread an explicit
// sdktrace.NewTracerProvider + tracetest.SpanRecorder through
// Server.SetTracerProvider rather than touching the global TracerProvider --
// safe under t.Parallel(). The tests that exercise the SERVER span or the
// outbound proxy's otelhttp auto-inject are the deliberate exception: they
// swap otel.SetTracerProvider for their duration (restored via t.Cleanup)
// and are NOT t.Parallel() for that reason -- see withGlobalTracerProvider.
package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
// ONCE for the whole package's test binary -- without this the process
// default is otel's no-op propagator: Inject writes nothing and Extract
// reads nothing, so every extraction/inject assertion below would pass
// vacuously regardless of whether tracing.go's carriage logic is wired
// correctly at all. The same propagator value is used by every test -- safe
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
// to tp, restoring the previous one on cleanup. Needed by the tests that
// exercise otelhttp's own span creation (the /mcp server span via
// otelUtils.GetHandlerWithOTEL, and the outbound proxy client span via
// otelhttp.NewTransport as wired in proxy.go's NewProxy) -- neither call site
// takes a per-call TracerProvider option, so both always resolve their
// tracer from otel.GetTracerProvider() at request time. Callers of this
// helper must NOT call t.Parallel().
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

func attrValue(attrs []attribute.KeyValue, key attribute.Key) (attribute.Value, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// okToolServer returns a Server wired to upstream, with tp threaded via
// SetTracerProvider, one tool ("tool-a" in "ns-a") registered, and a
// pass-through authorizer (authz is not what tracing tests exercise).
func okToolServer(t *testing.T, tp trace.TracerProvider, upstreamURL string) *Server {
	t.Helper()
	reg := NewRegistry()
	e := entry("ns-a", "fn1", "tool-a")
	reg.Upsert(e)
	s := NewServer(reg, NewProxy(upstreamURL, nil, logr.Discard(), 0), NewAuthorizer(nil), logr.Discard())
	s.SetTracerProvider(tp)
	s.ApplyToolDelta([]ToolEntry{e}, nil) // what the reconciler does on upsert; needed so tools/call resolves over the wire
	return s
}

func okUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// callToolReq builds a *mcp.CallToolRequest with the given raw _meta map
// (nil for "no _meta at all"), bypassing the scopeMiddleware/HTTP transport
// so callTool's extraction+span logic is exercised directly (mirrors
// pkg/agentruntime/tracing_test.go's direct DispatchTurn calls).
func callToolReq(tool string, meta mcp.Meta) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: tool, Meta: meta},
	}
}

// TestCallTool_MetaTraceparent_ChildOfExtractedTrace pins the spec'd
// extraction: a valid traceparent under _meta makes execute_tool a CHILD of
// the extracted (caller's own) trace, not of whatever ctx already carried --
// proving the "parents on the extracted context" half of callTool's design.
func TestCallTool_MetaTraceparent_ChildOfExtractedTrace(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	tp, rec := newTestTracerProvider(t)
	s := okToolServer(t, tp, upstream.URL)

	seedCtx, seedSpan := tp.Tracer("seed").Start(t.Context(), "seed")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(seedCtx, carrier)
	seedSpan.End()
	require.NotEmpty(t, carrier["traceparent"])

	meta := mcp.Meta{"traceparent": carrier["traceparent"]}
	res, err := s.callTool(context.Background(), callToolReq("tool-a", meta))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	toolSpan, ok := findSpan(rec.Ended(), "execute_tool ")
	require.True(t, ok, "expected an execute_tool span")
	assert.Equal(t, "execute_tool tool-a", toolSpan.Name())
	assert.Equal(t, seedSpan.SpanContext().TraceID(), toolSpan.SpanContext().TraceID(), "execute_tool must share the extracted _meta trace")
	assert.Equal(t, seedSpan.SpanContext().SpanID(), toolSpan.Parent().SpanID(), "execute_tool must be a CHILD of the extracted span, not a root")
	assert.Equal(t, codes.Ok, toolSpan.Status().Code)

	attrs := toolSpan.Attributes()
	if v, ok := attrValue(attrs, "gen_ai.operation.name"); assert.True(t, ok) {
		assert.Equal(t, "execute_tool", v.AsString())
	}
	if v, ok := attrValue(attrs, "gen_ai.tool.name"); assert.True(t, ok) {
		assert.Equal(t, "tool-a", v.AsString())
	}
	if v, ok := attrValue(attrs, "fission.agent.namespace"); assert.True(t, ok) {
		assert.Equal(t, "ns-a", v.AsString())
	}
}

// TestCallTool_MetaMalformed_NewRootSpanCallSucceeds pins the additive-only,
// fail-open contract: a syntactically invalid traceparent must not error the
// call, must not panic, and must not log above V(1) (log level is not
// observable from this test, but the call succeeding with a fresh ROOT span
// is the behavioral proof that extraction failed closed rather than open).
func TestCallTool_MetaMalformed_NewRootSpanCallSucceeds(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	tp, rec := newTestTracerProvider(t)
	s := okToolServer(t, tp, upstream.URL)

	meta := mcp.Meta{"traceparent": "not-a-valid-traceparent-at-all"}
	res, err := s.callTool(context.Background(), callToolReq("tool-a", meta))
	require.NoError(t, err, "a malformed traceparent must never error the call")
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	toolSpan, ok := findSpan(rec.Ended(), "execute_tool ")
	require.True(t, ok)
	assert.False(t, toolSpan.Parent().IsValid(), "a malformed traceparent must produce a ROOT span, not a spoofed/broken parent")
}

// TestCallTool_MetaAbsent_ServerSpanChild drives the byte-identical-behavior
// pin from the other direction: with NO _meta at all, an HTTP tools/call must
// still produce an execute_tool span, parented on the "fission-mcp" server
// span from main.go's otelUtils.GetHandlerWithOTEL wrap -- proving Step 1b's
// finding empirically (the go-sdk's ctx detach preserves context VALUES, so
// the server span survives into callTool) rather than merely asserting it
// from source reading.
func TestCallTool_MetaAbsent_ServerSpanChild(t *testing.T) {
	// Not t.Parallel(): withGlobalTracerProvider mutates global otel state.
	tp, rec := newTestTracerProvider(t)
	withGlobalTracerProvider(t, tp)

	upstream := okUpstream(t)
	s := okToolServer(t, tp, upstream.URL)
	handler := otelUtils.GetHandlerWithOTEL(s.HTTPHandler(), "fission-mcp")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "tool-a"})
	require.NoError(t, err)
	require.False(t, res.IsError)

	// client.Connect() issues its own "initialize" POST first, producing an
	// UNRELATED server span in a different trace before the tools/call one --
	// so the server span under test must be found by matching
	// toolSpan.Parent(), not by taking the first "POST "-prefixed span.
	ended := rec.Ended()
	toolSpan, ok := findSpan(ended, "execute_tool ")
	require.True(t, ok, "expected an execute_tool span, got spans: %v", spanNames(ended))

	var serverSpan sdktrace.ReadOnlySpan
	for _, sp := range ended {
		if sp.SpanContext().SpanID() == toolSpan.Parent().SpanID() {
			serverSpan = sp
		}
	}
	require.NotNil(t, serverSpan, "expected to find execute_tool's parent span among the ended spans: %v", spanNames(ended))
	assert.True(t, strings.HasPrefix(serverSpan.Name(), "POST"), "execute_tool's parent must be the otelhttp server span, got %q", serverSpan.Name())
	assert.Equal(t, serverSpan.SpanContext().TraceID(), toolSpan.SpanContext().TraceID(), "execute_tool must share the server span's trace when _meta carries no trace context")
	assert.Equal(t, serverSpan.SpanContext().SpanID(), toolSpan.Parent().SpanID(), "execute_tool must be a CHILD of the server span -- proving context VALUES survive the SDK's ctx detach")
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

// TestCallTool_MetaTraceparentOverWire_ChildOfExtractedTrace is the real-SDK-
// client twin of TestCallTool_MetaTraceparent_ChildOfExtractedTrace: the
// direct-callTool test above proves the SERVER's extraction contract, but
// says nothing about whether a real go-sdk client's params.SetMeta actually
// puts traceparent on the wire under the bare _meta key the server expects,
// rather than the client silently reshaping or namespacing it. If the go-sdk
// client ever stopped round-tripping caller-set _meta verbatim, every other
// test in this file would keep passing while the feature went dead for every
// real go-sdk caller -- this is the test that would catch that.
func TestCallTool_MetaTraceparentOverWire_ChildOfExtractedTrace(t *testing.T) {
	// Not t.Parallel(): withGlobalTracerProvider mutates global otel state.
	tp, rec := newTestTracerProvider(t)
	withGlobalTracerProvider(t, tp)

	upstream := okUpstream(t)
	s := okToolServer(t, tp, upstream.URL)
	handler := otelUtils.GetHandlerWithOTEL(s.HTTPHandler(), "fission-mcp")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	seedCtx, seedSpan := tp.Tracer("seed").Start(t.Context(), "seed")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(seedCtx, carrier)
	seedSpan.End()
	require.NotEmpty(t, carrier["traceparent"])

	params := &mcp.CallToolParams{Name: "tool-a"}
	params.SetMeta(map[string]any{"traceparent": carrier["traceparent"]})
	res, err := sess.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.False(t, res.IsError)

	toolSpan, ok := findSpan(rec.Ended(), "execute_tool ")
	require.True(t, ok, "expected an execute_tool span")
	assert.Equal(t, seedSpan.SpanContext().TraceID(), toolSpan.SpanContext().TraceID(),
		"a real go-sdk client's params.SetMeta traceparent must survive the wire and be extracted server-side")
	assert.Equal(t, seedSpan.SpanContext().SpanID(), toolSpan.Parent().SpanID(),
		"execute_tool must be a CHILD of the client-set traceparent's span, not the fission-mcp server span")
}

// TestCallTool_MetaNonStringValuesIgnored pins the untrusted-input handling:
// a non-string value under a reserved trace-context key must be ignored
// silently (not type-asserted into a panic, not an error), and the call must
// still succeed.
func TestCallTool_MetaNonStringValuesIgnored(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	tp, rec := newTestTracerProvider(t)
	s := okToolServer(t, tp, upstream.URL)

	meta := mcp.Meta{
		"traceparent": 12345,              // wrong type: must be ignored, not asserted
		"tracestate":  []string{"a", "b"}, // wrong type
		"baggage":     true,               // wrong type
	}
	res, err := s.callTool(context.Background(), callToolReq("tool-a", meta))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	toolSpan, ok := findSpan(rec.Ended(), "execute_tool ")
	require.True(t, ok)
	assert.False(t, toolSpan.Parent().IsValid(), "non-string _meta trace values must be ignored, producing a root span")
}

// TestCallTool_OutboundProxyRequestCarriesExtractedTraceparent proves the
// whole point of starting the span before the proxy call: the router-internal
// request otelhttp injects into must carry the SAME trace-id as the _meta
// traceparent that was extracted -- end to end through callTool, the real
// Proxy, and the real otelhttp transport wired in proxy.go's NewProxy (no
// explicit propagator.Inject added to buildRequest: this test is what proves
// the transport's own auto-inject already suffices once a span is on ctx --
// see tracing.go's doc comment on why no such explicit Inject was added).
func TestCallTool_OutboundProxyRequestCarriesExtractedTraceparent(t *testing.T) {
	// Not t.Parallel(): withGlobalTracerProvider mutates global otel state
	// (proxy.go's otelhttp.NewTransport takes no TracerProvider option, so it
	// always resolves from the global at request time).
	tp, _ := newTestTracerProvider(t)
	withGlobalTracerProvider(t, tp)

	var gotTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	s := okToolServer(t, tp, upstream.URL)

	seedCtx, seedSpan := tp.Tracer("seed").Start(t.Context(), "seed")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(seedCtx, carrier)
	seedSpan.End()

	meta := mcp.Meta{"traceparent": carrier["traceparent"]}
	_, err := s.callTool(context.Background(), callToolReq("tool-a", meta))
	require.NoError(t, err)

	require.NotEmpty(t, gotTraceparent, "the outbound request must carry a traceparent header")
	parts := strings.Split(gotTraceparent, "-")
	require.Len(t, parts, 4, "traceparent must be 00-<trace-id>-<span-id>-<flags>")
	assert.Equal(t, seedSpan.SpanContext().TraceID().String(), parts[1], "outbound traceparent trace-id must match the _meta-extracted trace")
}
