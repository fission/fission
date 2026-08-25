// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// tracing.go implements the execute_tool span (GenAI semantic conventions,
// pre-stable) and _meta trace-context extraction for callTool (server.go).
//
// Blast-radius note (see the plan's Global Constraints): pkg/mcp ships
// independently of agentRuntime.enabled and serves non-agent MCP clients, so
// everything here is ADDITIVE-ONLY. _meta is UNTRUSTED caller input: a
// missing or malformed traceparent/tracestate/baggage never errors and never
// logs above V(1) -- the call proceeds exactly as it would if _meta were
// absent. _meta present but empty of the reserved keys, or absent entirely,
// must be byte-identical in observable behavior to today (pre-this-slice).
package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// mcpTracerName scopes the execute_tool span's Tracer (see Server.tracer).
const mcpTracerName = "fission/mcp"

// traceContextKeys are the exact top-level _meta keys the MCP spec (revision
// 2026-07-28, "General fields > _meta > OpenTelemetry trace context")
// reserves for W3C trace-context propagation -- verified against the spec
// itself (modelcontextprotocol.io/specification/2026-07-28/basic/index#_meta),
// not assumed: the vendored go-sdk v1.7.0 module cache contains ZERO
// traceparent/otel material of its own (plan-review verified), so the SDK
// gave no signal either way. The spec text: "the keys traceparent,
// tracestate, and baggage are reserved for OpenTelemetry trace context
// propagation. When present, their values MUST follow W3C Trace Context and
// W3C Baggage formats respectively" -- an explicit exception to the
// otherwise-mandatory `<prefix>/<name>` _meta key format, so these three
// keys sit at the top level with no reverse-DNS prefix, matching
// propagation.TraceContext{} + propagation.Baggage{}'s own header names.
var traceContextKeys = [...]string{"traceparent", "tracestate", "baggage"}

// traceContextFromMeta extracts W3C trace context + baggage from the
// caller-supplied _meta fields, if present, and returns the resulting
// context; ctx unchanged when there is nothing to extract.
//
// _meta is UNTRUSTED caller input (any MCP client can set it to anything):
// only STRING-typed values under the three reserved keys are honored -- a
// non-string value (an object, a number, a bool) is silently ignored, never
// an error, matching the "malformed extracts to nothing, call proceeds"
// constraint. A present-but-malformed traceparent string (bad format, bad
// trace-id) is likewise not an error at this layer: propagation.TraceContext
// .Extract validates the W3C format internally and simply returns ctx
// unchanged when parsing fails (see go.opentelemetry.io/otel/propagation's
// TraceContext.Extract), so an attacker sending garbage cannot inject a
// spoofed parent span -- worst case is the call proceeds as if _meta carried
// no trace context at all, which is the additive-only, fail-open contract
// this function exists to guarantee.
func traceContextFromMeta(ctx context.Context, meta mcp.Meta) context.Context {
	if len(meta) == 0 {
		return ctx
	}
	carrier := propagation.MapCarrier{}
	for _, key := range traceContextKeys {
		v, ok := meta[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue // untrusted input: ignore silently, never error
		}
		carrier[key] = s
	}
	if len(carrier) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// SetTracerProvider wires the execute_tool span's TracerProvider seam
// post-construction, mirroring agentruntime's Dispatcher.SetTracerProvider
// (pkg/agentruntime/dispatcher.go) -- same construction-cycle rationale:
// NewServer's signature is the stable production wiring call sites use, and
// unit tests need a hermetic sdktrace.NewTracerProvider + tracetest.
// SpanRecorder instead of process-wide global state. nil (the zero value,
// production's default) makes tracer() fall back to otel.GetTracerProvider()
// -- what fission-bundle's main installs in production
// (pkg/utils/otel/provider.go).
func (s *Server) SetTracerProvider(tp trace.TracerProvider) {
	s.tracerProvider = tp
}

// tracer returns the Tracer callTool starts its execute_tool span from.
//
// GenAI semantic conventions are PRE-STABLE (experimental) as of this pin:
// go.opentelemetry.io/otel/semconv/v1.37.0 supplies the gen_ai.* attribute
// keys used in callTool (GenAIOperationNameExecuteTool = "execute_tool",
// GenAIToolName = "gen_ai.tool.name") -- the same version pin
// pkg/agentruntime/dispatcher.go uses for invoke_agent. Re-verify attribute
// names against the semconv package on any future bump; the spec has moved
// these before.
func (s *Server) tracer() trace.Tracer {
	tp := s.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer(mcpTracerName)
}

// endToolSpan records the outcome on span -- Error status (+ RecordError)
// for a protocol-level error, Ok otherwise -- mirroring the invoke_agent
// span's status discipline (pkg/agentruntime/dispatcher.go's DispatchTurn).
// A tool-level failure (function 4xx/5xx, timeout, oversized body) is NOT a
// protocol-level error here: Proxy.Invoke/InvokeStreaming map those into a
// CallToolResult with IsError set and a nil error (see proxy.go's doc
// comment), so this span's status reflects whether the MCP call itself
// completed, not whether the tool's own output was an error the agent can
// see and self-correct on.
func endToolSpan(span trace.Span, res *mcp.CallToolResult, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	return res, err
}
