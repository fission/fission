// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/router/streaming"
	"github.com/fission/fission/pkg/utils/correlation"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// proxyResponseBufferPool backs every ReverseProxy's response-copy buffer. A
// fresh ReverseProxy is built per request, but the copy buffer is process-wide
// and reused: with a nil BufferPool the proxy allocates a 32 KiB scratch buffer
// per response (httputil.ReverseProxy.copyResponse), which at warm-path RPS is
// pure GC pressure. Pooling reuses one buffer per concurrent copy instead.
var proxyResponseBufferPool httputil.BufferPool = &bufferPool{
	pool: sync.Pool{New: func() any { b := make([]byte, 32*1024); return &b }},
}

type bufferPool struct{ pool sync.Pool }

func (b *bufferPool) Get() []byte  { return *b.pool.Get().(*[]byte) }
func (b *bufferPool) Put(s []byte) { b.pool.Put(&s) }

// functionHandler orchestrates one trigger's (or one internal route's) request
// path: canary backend selection, proxy policy resolution, and the reverse
// proxy wiring. Address resolution and tap accounting live behind the injected
// AddressResolver and Tapper seams (RFC-0002 structural track).
type functionHandler struct {
	logger                   logr.Logger
	resolver                 AddressResolver
	tapper                   Tapper
	function                 *fv1.Function
	httpTrigger              *fv1.HTTPTrigger
	functionMap              map[string]*fv1.Function
	fnWeightDistributionList []functionWeightDistribution
	// stickySource is the Function (RFC-0025 Task 5) whose Spec.State.Sticky
	// governs sticky routing for this route: the single resolved function for
	// an unweighted route, or the LIVE function for a weighted alias's split
	// (see resolveResult.stickySource). nil for the legacy
	// FunctionReferenceTypeFunctionWeights canary, which has no single
	// canonical sticky config and so never extracts a key (pure random pick,
	// unchanged from pre-Task-5 behavior).
	stickySource         *fv1.Function
	tsRoundTripperParams *tsRoundTripperParams
	isDebugEnv           bool
	structuredErrors     bool
	accessLog            bool
	// functionTimeoutMap and policyByUID are keyed by crd.CacheKeyUG (UID,
	// Generation), not UID alone: an RFC-0025 versioned backend (a weighted
	// alias's primary/secondary target, or the internal route's fresh
	// resolve on a live-Function event) can produce two *fv1.Function
	// snapshots sharing a UID but differing in Generation, and each needs
	// its own timeout/policy — collapsing on UID would let one snapshot's
	// entry silently serve the other's streaming/proxy policy.
	functionTimeoutMap map[crd.CacheKeyUG]int
	// Hoisted per-route state (RFC-0014): computed once at mux build instead
	// of per request. rtLogger is the round tripper's named logger;
	// policyByUID holds the resolved proxy policy per backend function (the
	// canary path selects the backend per request, hence per-key);
	// basesByUID holds each backend function's internal-listener URL prefix
	// candidates (functionURLBases) the same way, for trimFunctionPrefix.
	rtLogger    logr.Logger
	policyByUID map[crd.CacheKeyUG]proxyPolicy
	basesByUID  map[crd.CacheKeyUG][]string
	// asyncInvoker enqueues RFC-0024 async invocations. Set on both the public
	// HTTPTrigger handlers and the internal direct-function handlers, so a signed
	// direct caller can go async; the dispatcher's own deliveries are excluded by
	// the X-Fission-Invocation-Id guard in handler(), not by a nil invoker. May be
	// nil (or hold a nil queue) when the feature is off.
	asyncInvoker *asyncInvoker
}

// proxyPolicyFor returns the hoisted policy for fn, computing it on the spot
// only when the route was built without a precomputed map (test harnesses).
func (fh *functionHandler) proxyPolicyFor(fn *fv1.Function, fnTimeout time.Duration) proxyPolicy {
	if p, ok := fh.policyByUID[crd.CacheKeyUGFromMeta(&fn.ObjectMeta)]; ok {
		return p
	}
	return resolveProxyPolicy(fn, fnTimeout, fh.tsRoundTripperParams.streamIdleDefault)
}

// roundTripperLogger returns the hoisted per-route logger, falling back to
// deriving it for handlers constructed without one (test harnesses).
func (fh *functionHandler) roundTripperLogger() logr.Logger {
	if fh.rtLogger.GetSink() != nil {
		return fh.rtLogger
	}
	return fh.logger.WithName("roundtripper")
}

// basesFor returns the hoisted internal-listener URL prefix candidates for
// fn (see functionURLBases), computing them on the spot only when the route
// was built without a precomputed map (test harnesses) — same fallback
// pattern as proxyPolicyFor/roundTripperLogger above.
func (fh *functionHandler) basesFor(fn *fv1.Function) []string {
	if b, ok := fh.basesByUID[crd.CacheKeyUGFromMeta(&fn.ObjectMeta)]; ok {
		return b
	}
	return functionURLBases(&fn.ObjectMeta)
}

// asyncRequested reports whether request should be enqueued for RFC-0024 async
// delivery rather than proxied. A request is async when it carries
// X-Fission-Invoke-Mode: async, or a public trigger forces it via
// spec.invocationMode=async (for callers that cannot set headers, e.g. third-party
// webhooks). It fires on BOTH the public trigger path and the internal
// direct-function path (/fission-function/...), so a signed direct caller — e.g.
// `fission function test --async` — can enqueue too.
//
// The X-Fission-Invocation-Id guard applies ONLY on the internal direct-function
// path (httpTrigger == nil): that is where the dispatcher delivers, and its own
// delivery — which carries that header — must proxy synchronously so it can never
// re-enqueue. On the public trigger path the dispatcher never delivers, so the
// header is not a delivery signal there; it is also user-spoofable, so honoring it
// would let a caller bypass spec.invocationMode=async and force a sync invocation.
// Hence the public path decides purely on the header / trigger mode.
func (fh functionHandler) asyncRequested(request *http.Request) bool {
	headerAsync := strings.EqualFold(request.Header.Get(asyncinvoke.HeaderInvokeMode), asyncinvoke.InvokeModeAsync)
	if fh.httpTrigger != nil {
		return headerAsync || strings.EqualFold(fh.httpTrigger.Spec.InvocationMode, asyncinvoke.InvokeModeAsync)
	}
	// Internal direct-function path: never re-enqueue the dispatcher's own delivery.
	if request.Header.Get(asyncinvoke.HeaderInvocationID) != "" {
		return false
	}
	return headerAsync
}

func (fh functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	// RFC-0023/0025: extract the sticky key off the resolution's stable
	// sticky-config source (fh.stickySource -- the live function for any
	// alias resolution, the single resolved function otherwise; see
	// resolveResult.stickySource) BEFORE any per-request backend pick. The
	// SAME key then drives both the weighted pick below and the round
	// tripper's Admit ranking (transport.go, via RetryingRoundTripper.
	// stickyKey) -- computing it once and passing it down is what guarantees
	// the two can never disagree for a weighted alias.
	//
	// fh.stickySource is nil for the legacy FunctionReferenceTypeFunctionWeights
	// canary (its backends are distinct named functions, each with its own
	// independent StickyConfig, so there is no single config to key the PICK
	// on) -- stickyKey starts "" there and getCanaryBackend falls back to its
	// pre-Task-5 random pick. The pre-Task-5 Admit-side behavior (honor
	// whichever backend the pick landed on) is restored below, AFTER the
	// pick, once fh.function is known.
	stickyKey := stickyKeyFromRequest(fh.stickySource, request)

	if len(fh.fnWeightDistributionList) > 0 {
		// Weighted backend selection: the legacy FunctionReferenceTypeFunctionWeights
		// canary AND an RFC-0025 weighted FunctionAlias (Spec.Weight != nil) both
		// resolve to a resolveResultMultipleFunctions with a non-empty weight
		// distribution — pick the per-request backend from it now rather than at
		// mux-build time. NOT gated on fh.httpTrigger != nil: a weighted alias's
		// materialized `:<alias>` internal route (buildInternalAliasHandler,
		// routeshape.go) carries the same distribution with no httpTrigger at
		// all, so MQ/timer/kubewatcher/MCP invocations through that route see
		// the identical split an HTTPTrigger referencing the alias would —
		// "weighted aliases work uniformly on all trigger types for free,
		// because the weighted pick happens router-side" (RFC-0025).
		//
		// stickyKey is "" for the legacy canary going in (stickySource is nil
		// there), so getCanaryBackend falls back to its pre-Task-5 random
		// pick; for a keyed request against a weighted alias, the pick is
		// deterministic.
		fn := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList, stickyKey)
		if fn == nil {
			fh.logger.Error(nil, "could not get canary backend",
				"fnMap", fh.functionMap,
				"distributionList", fh.fnWeightDistributionList)
			// TODO : write error to responseWrite and return response
			return
		}
		fh.function = fn
		fh.logger.V(1).Info("chosen function backend's metadata", "metadata", fh.function)

		// Legacy canary Admit-side restore (see the top-of-function comment):
		// fh.stickySource is nil, so stickyKey is still "" here -- recompute
		// it from the just-chosen fn, restoring the pre-Task-5 behavior of
		// honoring whichever backend's own StickyConfig the random pick
		// landed on. Safe to overwrite: the pick above was never keyed
		// (stickyKey was "" going in), so there is no pick/admit disagreement
		// to reintroduce.
		if fh.stickySource == nil {
			stickyKey = stickyKeyFromRequest(fn, request)
		}
	}

	// url path
	setPathInfoToHeader(request)

	// system params
	setFunctionMetadataToHeader(&fh.function.ObjectMeta, request)

	// RFC-0024: async invocation. handle() writes 501 when the feature is off (nil
	// invoker/queue), answering an async-mode request honestly.
	if fh.asyncRequested(request) {
		fh.asyncInvoker.handle(responseWriter, request, fh.function)
		return
	}

	director := func(req *http.Request) {
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}

	fnTimeout := fh.functionTimeoutMap[crd.CacheKeyUGFromMeta(&fh.function.ObjectMeta)]
	if fnTimeout == 0 {
		fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
	}

	policy := fh.proxyPolicyFor(fh.function, time.Duration(fnTimeout)*time.Second)

	// Streaming: scope the request to a max-duration ceiling and an idle
	// Watchdog (see setupStreamContext). Classic path: the request context is
	// used unchanged (byte-identical behavior).
	var (
		streamCancel context.CancelCauseFunc
		watchdog     *streaming.Watchdog
	)
	if policy.streaming {
		request, watchdog, streamCancel = fh.setupStreamContext(request, policy)
	}

	rrt := &RetryingRoundTripper{
		logger:      fh.roundTripperLogger(),
		resolver:    fh.resolver,
		tapper:      fh.tapper,
		fn:          fh.function,
		trigger:     fh.httpTrigger,
		params:      fh.tsRoundTripperParams,
		isDebugEnv:  fh.isDebugEnv,
		funcTimeout: time.Duration(fnTimeout) * time.Second,
		policy:      policy,
		stickyKey:   stickyKey,
		bases:       fh.basesFor(fh.function),
	}

	start := time.Now()

	proxy := &httputil.ReverseProxy{
		Director:     director,
		Transport:    rrt,
		BufferPool:   proxyResponseBufferPool,
		ErrorHandler: fh.getProxyErrorHandler(start, rrt),
		ModifyResponse: func(resp *http.Response) error {
			// One goroutine for metric collection + the cached-URL tap (the
			// historical pairing — the tap is a buffered channel send and does
			// not warrant a spawn of its own).
			go func() {
				fh.collectFunctionMetric(start, rrt, request, resp)
				if rrt.urlFromCache {
					fh.tapper.Tap(fh.function, rrt.tapURL)
				}
			}()
			if policy.streaming {
				fh.onStreamResponse(request.Context(), rrt, watchdog, resp)
			}
			return nil
		},
	}
	if policy.streaming {
		// Flush every write so SSE/chunked chunks reach the client as produced
		// (Go also auto-selects this for text/event-stream).
		proxy.FlushInterval = -1
	}

	defer func() {
		// If the context is closed when RoundTrip returns, client may receive
		// truncated response body due to "context canceled" error. To avoid
		// this, we need to close request context after proxy.ServeHTTP finished.
		//
		// NOTE: rrt.closeContext() must be put in the defer function; otherwise,
		// reverseProxy may panic when failed to write response and the context
		// will not be closed.
		//
		// ref: https://github.com/golang/go/issues/28239
		if watchdog != nil {
			watchdog.Stop()
		}
		if streamCancel != nil {
			streamCancel(nil)
		}
		rrt.closeContext()
		// Streaming functions settle here — after ServeHTTP has fully drained
		// the stream — rather than at RoundTrip return (headers). settle
		// dispatches between the two accounting modes (local release vs RPC
		// untap).
		if policy.streaming && rrt.serviceURL != nil {
			rrt.settle(rrt.release, rrt.tapURL)
		}
	}()

	if otelUtils.SpanIsRecording(request.Context()) {
		otelUtils.SpanTrackEvent(request.Context(), "functionRequestProxy", otelUtils.GetAttributesForFunction(fh.function)...)
	}
	proxy.ServeHTTP(responseWriter, request)
}

// classifyFunctionError returns the stable reason for a function-side
// round-trip error (the component is always ComponentFunction) (RFC-0015).
// Connection-refused is checked before dial because a refused connection is
// itself a dial error; everything else is the function returning or closing
// unexpectedly.
func classifyFunctionError(err error) string {
	if netErr := network.Adapter(err); netErr != nil {
		switch {
		case netErr.IsConnRefusedError():
			return ferror.ReasonConnectionRefused
		case netErr.IsDialError():
			return ferror.ReasonDialError
		}
	}
	return ferror.ReasonFunctionError
}

// getProxyErrorHandler returns a reverse proxy error handler that, in addition
// to the legacy status mapping, attributes the failure to a component + reason
// (RFC-0015) and — unless ROUTER_STRUCTURED_ERRORS is off — returns a JSON body
// carrying that attribution plus the request id and trace id. Status codes are
// identical to the legacy handler.
func (fh functionHandler) getProxyErrorHandler(start time.Time, rrt *RetryingRoundTripper) func(rw http.ResponseWriter, req *http.Request, err error) {
	return func(rw http.ResponseWriter, req *http.Request, err error) {
		var status int
		var msg string
		var component ferror.Component
		var reason string
		ctx := req.Context()
		logger := otelUtils.LoggerWithTraceID(ctx, fh.logger)
		// A server-initiated streaming abort (idle/max-duration) surfaces as
		// context.Canceled too, but carries a cause we set via WithCancelCause.
		// Surface it as a 504 with the real reason instead of masquerading as a
		// client-close 499, and log it where an operator can see it.
		streamCause := context.Cause(ctx)
		var invErr *ferror.InvocationError
		switch {
		case errors.Is(streamCause, errStreamIdleTimeout) || errors.Is(streamCause, errStreamMaxDuration):
			status = http.StatusGatewayTimeout
			msg = streamCause.Error()
			component = ferror.ComponentTimeout
			reason = ferror.ReasonStreamMaxDuration
			if errors.Is(streamCause, errStreamIdleTimeout) {
				reason = ferror.ReasonStreamIdle
			}
			// The abort was already logged at Info by the watchdog/max-duration
			// callback; this is just the HTTP outcome for a pre-first-byte abort.
			logger.V(1).Info(msg, "function", fh.function, "status", http.StatusText(status))
		case errors.Is(err, context.Canceled):
			// 499 CLIENT CLOSED REQUEST
			// A non-standard status code introduced by nginx for the case
			// when a client closes the connection while nginx is processing the request.
			// Reference: https://httpstatuses.com/499
			status = 499
			msg = "client closes the connection"
			component = ferror.ComponentRouter
			reason = ferror.ReasonClientDisconnect
			logger.V(1).Info(msg, "function", fh.function, "status", "Client Closed Request")
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
			msg = "no response from function before timeout"
			component = ferror.ComponentTimeout
			reason = ferror.ReasonFunctionTimeout
			logger.Info(msg, "function", fh.function, "status", http.StatusText(status))
		case errors.As(err, &invErr):
			// The round-tripper already attributed this failure (executor /
			// resolver origin); the wrapped error keeps the status unchanged.
			status, _ = ferror.GetHTTPError(err)
			msg = "error sending request to function"
			component = invErr.Component
			reason = invErr.Reason
			logger.Info(msg, "function", fh.function,
				"status", http.StatusText(status), "component", component, "reason", reason)
		default:
			code, _ := ferror.GetHTTPError(err)
			status = code
			msg = "error sending request to function"
			component = ferror.ComponentFunction
			reason = classifyFunctionError(err)
			logger.Info(msg, "function", fh.function,
				"status", http.StatusText(status), "code", code, "component", component, "reason", reason)
		}

		go func() {
			fh.collectFunctionMetric(start, rrt, req, &http.Response{
				StatusCode:    status,
				ContentLength: req.ContentLength,
			})
			// tapService for cached service urls, matching the historical
			// error-path behavior (the tap used to ride inside
			// collectFunctionMetric).
			if rrt.urlFromCache {
				fh.tapper.Tap(fh.function, rrt.tapURL)
			}
		}()

		// A client disconnect (499) is benign churn, not a server-side failure,
		// so it is excluded from the failure-attribution counter.
		if status != 499 {
			invocationFailures.Add(req.Context(), 1, metric.WithAttributes(
				attribute.String("component", string(component)),
				attribute.String("reason", reason),
			))
		}

		fh.writeInvocationError(rw, req, status, component, reason, msg, err)
	}
}

// writeInvocationError writes the failure response: a structured JSON body
// (RFC-0015) carrying the attribution, request id, and trace id, or — when
// ROUTER_STRUCTURED_ERRORS is off — the legacy plain-text body verbatim. The
// raw error detail is included only when the caller opted in via X-Fission-Debug
// AND the router runs in debug mode, so internal detail never leaks by default.
// Issue #693 (a traceable id in the response) is resolved here.
func (fh functionHandler) writeInvocationError(rw http.ResponseWriter, req *http.Request, status int, component ferror.Component, reason, legacyMsg string, cause error) {
	if !fh.structuredErrors {
		rw.WriteHeader(status)
		if _, werr := rw.Write([]byte(legacyMsg)); werr != nil {
			fh.logger.Error(werr, "error writing HTTP response", "function", fh.function)
		}
		return
	}

	ctx := req.Context()
	body := ferror.InvocationError{
		Component: component,
		Reason:    reason,
		RequestID: correlation.FromContext(ctx),
		TraceID:   otelUtils.TraceIDFromContext(ctx),
	}
	if fh.isDebugEnv && cause != nil && strings.EqualFold(req.Header.Get(correlation.HeaderDebug), "true") {
		body.Message = cause.Error()
	}

	// Set the attribution header before writing the status so it survives even
	// the marshal-failure fallback below.
	rw.Header().Set(correlation.HeaderComponent, string(component))

	payload, merr := json.Marshal(body)
	if merr != nil {
		// Never emit a half-written body: fall back to plain text.
		rw.WriteHeader(status)
		if _, werr := rw.Write([]byte(legacyMsg)); werr != nil {
			fh.logger.Error(werr, "error writing fallback HTTP response", "function", fh.function)
		}
		fh.logger.Error(merr, "error marshaling structured error body; wrote plain text", "function", fh.function)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	if _, werr := rw.Write(payload); werr != nil {
		fh.logger.Error(werr, "error writing HTTP response", "function", fh.function)
	}
}
