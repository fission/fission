// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	routerutil "github.com/fission/fission/pkg/router/util"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	tsRoundTripperParams struct {
		timeout          time.Duration
		timeoutExponent  int
		disableKeepAlive bool
		keepAliveTime    time.Duration

		// maxIdleConnsPerHost bounds the shared transport's idle pool per
		// function address (each poolmgr pod is its own host). Go's default
		// of 2 would throttle per-pod reuse below requestsPerPod ceilings.
		// From ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST; 0 means the
		// defaultMaxIdleConnsPerHost.
		maxIdleConnsPerHost int

		// The shared transport (RFC-0014): ONE pool for the router process,
		// built lazily on first use. Before this, a fresh http.Transport was
		// constructed per request, which silently defeated keep-alive
		// entirely — every proxied request dialed TCP fresh.
		transportOnce sync.Once
		transport     *http.Transport
		otelTransport http.RoundTripper

		// streamIdleDefault is the idle timeout applied to streaming functions
		// when StreamingConfig.IdleTimeoutSeconds is unset (from the router's
		// ROUTER_STREAM_IDLE_TIMEOUT env, defaulting to DefaultStreamIdleSeconds).
		streamIdleDefault time.Duration

		// maxRetries is the max times for RetryingRoundTripper to retry a request.
		// Default maxRetries is 10, which means router will retry for
		// up to 10 times and abort it if still not succeeded.
		maxRetries int

		// svcAddrRetryCount is the max times for RetryingRoundTripper to retry
		// with a specific cached service address: after svcAddrRetryCount
		// network timeout errors the address is invalidated and a fresh one is
		// resolved. (Non-timeout errors are relayed to the caller without
		// retrying; index-admitted endpoints are quarantined on the first dial
		// error instead of climbing this ladder.)
		// Default svcAddrRetryCount is 5.
		svcAddrRetryCount int
	}

	// RetryingRoundTripper is a layer on top of http.DefaultTransport, with
	// retries. It depends on small injected seams — AddressResolver for
	// function→address resolution and Tapper for poolmgr request-slot release —
	// so it is testable against fakes (RFC-0002 structural track).
	RetryingRoundTripper struct {
		logger      logr.Logger
		resolver    AddressResolver
		tapper      Tapper
		fn          *fv1.Function
		trigger     *fv1.HTTPTrigger
		params      *tsRoundTripperParams
		isDebugEnv  bool
		funcTimeout time.Duration
		policy      proxyPolicy // resolved once in handler; drives streaming behavior

		closeContextFunc *context.CancelFunc
		serviceURL       *url.URL
		// tapURL is the liveness-tap target for serviceURL (differs from it
		// only for endpoint-LB entries; see ResolvedEntry.TapURL).
		tapURL       *url.URL
		urlFromCache bool
		// release returns the router-local admission slot for the last resolved
		// endpoint (nil when accounting is executor-side; see ResolvedEntry).
		release    func()
		totalRetry int
	}

	// To keep the request body open during retries, we create an interface with Close operation being a no-op.
	// Details : https://github.com/flynn/flynn/pull/875
	fakeCloseReadCloser struct {
		io.ReadCloser
	}
)

func (w *fakeCloseReadCloser) Close() error {
	return nil
}

func (w *fakeCloseReadCloser) RealClose() error {
	if w.ReadCloser == nil {
		return nil
	}
	return w.ReadCloser.Close()
}

// settle returns the request slot held by one resolution once its request
// completes: router-admitted entries (poolmgr index admission or newdeploy/
// container endpoint-LB) return their local slot via release; executor-
// resolved poolmgr entries release via the async UnTap RPC (the executor did
// the accounting); deploy-backed VIP entries hold no slot. This is the single
// dispatch point for the two accounting modes — they must never mix (see
// ResolvedEntry.Release).
func (roundTripper *RetryingRoundTripper) settle(release func(), tapURL *url.URL) {
	if release != nil {
		release()
		return
	}
	if roundTripper.fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		go roundTripper.tapper.UnTap(context.Background(), roundTripper.fn, tapURL) //nolint:errcheck
	}
}

// RoundTrip forwards the request to the function address obtained from the
// injected AddressResolver, with a bounded retry loop:
//
//   - Each iteration resolves an address (index-admitted, cached, or a fresh
//     executor RPC — the resolver decides; see the inline comments for how a
//     previously held admission slot is released on re-resolve) and dials it.
//   - A network dial error invalidates the address (quarantine for
//     index-admitted endpoints, cache eviction after svcAddrRetryCount timeouts
//     for executor-cached ones) and retries with exponential back-off, up to
//     maxRetries.
//   - Any non-dial response or error is relayed to the caller as-is, without
//     retrying; a resolver error surfaces as 502 via the reverse proxy's error
//     handler (429 from ensureCapacity passes through unchanged).
func (roundTripper *RetryingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// set the timeout for transport context
	addForwardedHostHeader(roundTripper.logger, req)
	transport, otelTransport := roundTripper.params.sharedTransport()

	executingTimeout := roundTripper.params.timeout

	// wrap the req.Body with another ReadCloser interface.
	if req.Body != nil {
		req.Body = &fakeCloseReadCloser{req.Body}
	}

	// close req body
	defer func() {
		if req.Body != nil {
			err := req.Body.(*fakeCloseReadCloser).RealClose()
			if err != nil {
				roundTripper.logger.Error(err, "Error closing body")
			}
		}
	}()

	// The reason for request failure may vary from case to case.
	// After some investigation, found most of the failure are due to
	// network timeout or target function is under heavy workload. In
	// such cases, if router keeps trying to get new function service
	// will increase executor burden and cause 502 error.
	//
	// The "retryCounter" was introduced to solve this problem by retrying
	// requests for "limited threshold". Once a request's retryCounter higher
	// than the predefined threshold, reset retryCounter and remove service
	// cache, then retry to get new svc record from executor again.
	var retryCounter int
	var err error
	var fnMeta = &roundTripper.fn.ObjectMeta

	logger := otelUtils.LoggerWithTraceID(ctx, roundTripper.logger).WithValues("function", fnMeta.Name, "namespace", fnMeta.Namespace)

	for i := 0; i < roundTripper.params.maxRetries; i++ {
		// set service url of target service of request only when
		// trying to get new service url from cache/executor.
		if retryCounter == 0 {
			otelUtils.SpanTrackEvent(ctx, "getServiceEntry", otelUtils.MapToAttributes(map[string]string{
				"function-name":      fnMeta.Name,
				"function-namespace": fnMeta.Namespace})...)
			// get function service url from cache or executor
			var entry ResolvedEntry
			entry, err = roundTripper.resolver.Resolve(ctx, roundTripper.fn)
			// Return any previously-admitted slot this re-resolve abandons. The
			// per-resolve defers below cover the classic path at exit, but a
			// streaming request defers its release to the handler, which only
			// sees the LAST resolution — without this, every abandoned slot
			// would pin its pod's in-flight counter forever (sync.Once makes
			// the duplicate call from the classic defers a no-op).
			if roundTripper.release != nil {
				roundTripper.release()
			}
			roundTripper.serviceURL, roundTripper.urlFromCache, roundTripper.release = entry.SvcURL, entry.FromCache, entry.Release
			roundTripper.tapURL = entry.tapTarget()
			if err != nil {
				// We might want a specific error code or header for fission failures as opposed to
				// user function bugs.
				statusCode, errMsg := ferror.GetHTTPError(err)
				if statusCode == http.StatusTooManyRequests {
					return nil, err
				}
				if roundTripper.isDebugEnv {
					return &http.Response{
						StatusCode:    statusCode,
						Proto:         req.Proto,
						ProtoMajor:    req.ProtoMajor,
						ProtoMinor:    req.ProtoMinor,
						Body:          io.NopCloser(bytes.NewBufferString(errMsg)),
						ContentLength: int64(len(errMsg)),
						Request:       req,
						Header:        make(http.Header),
					}, nil
				}
				return nil, ferror.MakeError(http.StatusInternalServerError, err.Error())
			}
			if roundTripper.serviceURL == nil {
				// No current resolver returns Release with a nil SvcURL, but if
				// one ever did, skipping the defer registration below on the
				// FINAL iteration would leak the slot — return it here (the
				// sync.Once makes a later duplicate release a no-op).
				if roundTripper.release != nil {
					roundTripper.release()
				}
				logger.Info("serviceURL is empty for function, retrying", "executingTimeout", executingTimeout)
				time.Sleep(jitter(executingTimeout))
				executingTimeout = executingTimeout * time.Duration(roundTripper.params.timeoutExponent)
				continue
			}
			otelUtils.SpanTrackEvent(ctx, "serviceEntryReceived", otelUtils.MapToAttributes(map[string]string{
				"function-name":      fnMeta.Name,
				"function-namespace": fnMeta.Namespace,
				"service-entry":      roundTripper.serviceURL.String()})...)
			// Streaming functions settle in the handler (after ServeHTTP fully
			// drains the stream), not here at RoundTrip return (which fires at
			// headers, while the body is still streaming). One defer per
			// resolution: an executor-resolved retry chain unTaps each pod the
			// executor allotted; router-admitted releases are sync.Once-idempotent.
			if !roundTripper.policy.streaming {
				defer func(release func(), tapURL *url.URL) {
					roundTripper.settle(release, tapURL)
				}(roundTripper.release, roundTripper.tapURL)
			}

			// rewrite the request to reflect the service url (which comes from
			// the executor response) and the trigger's prefix specification.
			rewriteFunctionURL(logger, req, roundTripper.trigger, fnMeta, roundTripper.serviceURL)
		}

		// Do NOT assign returned request to "req"
		// because the request used in the last round
		// will be canceled when calling setContext.
		newReq := roundTripper.setContext(req)
		// Per-attempt dial deadline (the cold-pod fast-retry ladder) rides the
		// context into the SHARED transport's DialContext; a pooled-conn hit
		// skips the dial entirely, which is correct — there is nothing to
		// time out.
		newReq = newReq.WithContext(context.WithValue(newReq.Context(), dialTimeoutKey{}, executingTimeout))

		if roundTripper.isDebugEnv {
			debugDumpRequest(logger, newReq)
		}

		// forward the request to the function service
		otelUtils.SpanTrackEvent(ctx, "roundtrip", otelUtils.MapToAttributes(map[string]string{
			"function-name":      fnMeta.Name,
			"function-namespace": fnMeta.Namespace,
			"function-url":       newReq.URL.String(),
			"retryCounter":       fmt.Sprintf("%d", retryCounter)})...)
		// otelhttp wraps the response body, which breaks the io.ReadWriteCloser
		// that ReverseProxy needs to hijack a 101 Switching Protocols (WebSocket)
		// response. Forward upgrade requests on the raw transport so the
		// connection can be hijacked; instrument everything else. This applies to
		// ALL WebSocket requests (streaming and classic) on purpose — otel wrapping
		// breaks the hijack regardless of Spec.Streaming, so this also fixes classic
		// WebSocket functions. The only cost is no otel span for the upgrade itself
		// (a hijacked bidirectional connection isn't meaningfully traceable anyway).
		var rt http.RoundTripper = otelTransport
		if routerutil.IsWebsocketRequest(newReq) {
			rt = transport
		}
		resp, err := rt.RoundTrip(newReq)
		if roundTripper.isDebugEnv {
			debugDumpResponse(logger, resp)
		}
		if err == nil {
			// return response back to user
			return resp, nil
		}

		roundTripper.totalRetry++

		if i >= roundTripper.params.maxRetries-1 {
			// return here if we are in the last round
			logger.Error(err, "error getting response from function")
			return nil, err
		}

		// if transport.RoundTrip returns a non-network dial error, then relay it back to user
		netErr := network.Adapter(err)

		// dial timeout or dial network errors goes here
		var isNetDialErr, isNetTimeoutErr bool
		if netErr != nil {
			isNetDialErr = netErr.IsDialError()
			isNetTimeoutErr = netErr.IsTimeoutError()
		}

		// if transport.RoundTrip returns a non-network dial error (e.g. "context canceled"), then relay it back to user
		if !isNetDialErr {
			// A canceled context is a client disconnect (the caller went away
			// or its deadline fired), not a server-side error — log it quietly
			// so client churn doesn't flood the router log at Error level.
			if errors.Is(err, context.Canceled) {
				logger.V(1).Info("request context canceled by client", "url", req.URL.Host)
			} else {
				logger.Error(err, "encountered non-network dial error")
			}
			return resp, err
		}

		// close response body before entering next loop
		if resp != nil {
			resp.Body.Close()
		}

		// An index-admitted endpoint (release != nil) that fails ANY dial is
		// quarantined immediately: unlike the executor-RPC path — where only
		// the timeout ladder below invalidates, because a fresh RPC re-picks a
		// pod anyway — re-resolving the index would happily re-admit the same
		// dead endpoint (connection refused never increments retryCounter)
		// until maxRetries burn out. The quarantine lifts on the next slice
		// event for the function.
		if roundTripper.release != nil {
			roundTripper.resolver.Invalidate(roundTripper.fn, roundTripper.serviceURL)
		}

		// Check whether an error is an timeout error ("dial tcp i/o timeout").
		if isNetTimeoutErr {
			logger.V(1).Info("request errored out - backing off before retrying",
				"url", req.URL.Host, "error", err.Error())
			retryCounter++
		}

		// If it's not a timeout error or retryCounter exceeded pre-defined threshold,
		if retryCounter >= roundTripper.params.svcAddrRetryCount {
			logger.V(1).Info(fmt.Sprintf(
				"retry counter exceeded pre-defined threshold of %v",
				roundTripper.params.svcAddrRetryCount))
			if roundTripper.urlFromCache {
				roundTripper.resolver.Invalidate(roundTripper.fn, roundTripper.serviceURL)
			}
			retryCounter = 0
		}

		logger.V(1).Info("Backing off before retrying", "backoff_time", executingTimeout, "error", err.Error())
		time.Sleep(jitter(executingTimeout))
		executingTimeout = executingTimeout * time.Duration(roundTripper.params.timeoutExponent)
	}

	e := errors.New("unable to get service url for connection")
	logger.Error(e, "exceeded max retries for function")
	return nil, e
}

// jitter adds up to 20% positive random jitter to a backoff duration so that
// many concurrent retriers (and multiple router replicas) don't retry in
// lockstep and stampede a function pod as it recovers.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + time.Duration(rand.Float64()*0.2*float64(d))
}

// dialTimeoutKey carries the per-attempt dial timeout through the request
// context into the shared transport's DialContext. The backoff-scaled
// executingTimeout ladder is NOT just a timeout — it is the fast-retry
// mechanism for cold pods (a not-yet-listening pod must fail the dial
// quickly so the loop re-resolves), so it must survive the move to a shared
// transport whose Dialer cannot carry per-request state.
type dialTimeoutKey struct{}

const (
	// defaultMaxIdleConnsPerHost: each poolmgr pod is its own host (ip:8888),
	// so this is effectively the per-pod pooled-connection ceiling.
	defaultMaxIdleConnsPerHost = 32
	// transportIdleConnTimeout is deliberately shorter than the poolmgr idle
	// reap window (120s) and aligned with the RFC-0002 drain grace floor
	// (30s), bounding how long a pooled connection can outlive its pod.
	transportIdleConnTimeout = 30 * time.Second
)

// sharedTransport returns the process-wide pooled transport (and its
// otel-instrumented wrapper), building both once. Connection reuse across
// requests is the point (RFC-0014); per-attempt dial deadlines arrive via
// dialTimeoutKey on the request context — net.Dialer.DialContext honors ctx
// deadlines, and a deadline-exceeded dial classifies exactly like the old
// Dialer.Timeout (*net.OpError{Op: "dial"} with Timeout() == true), keeping
// the retry-ladder semantics byte-identical (pinned by
// TestDialLadderTimeoutClassification).
func (p *tsRoundTripperParams) sharedTransport() (*http.Transport, http.RoundTripper) {
	p.transportOnce.Do(func() {
		perHost := p.maxIdleConnsPerHost
		if perHost <= 0 {
			perHost = defaultMaxIdleConnsPerHost
		}
		// No Dialer.Timeout: the per-attempt deadline comes from the context
		// (cancelling the derived ctx after a successful dial does not affect
		// the established connection, per net.Dialer docs).
		dialer := &net.Dialer{KeepAlive: p.keepAliveTime}
		p.transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if d, ok := ctx.Value(dialTimeoutKey{}).(time.Duration); ok && d > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, d)
					defer cancel()
				}
				return dialer.DialContext(ctx, network, addr)
			},
			MaxIdleConns:          1024,
			MaxIdleConnsPerHost:   perHost,
			IdleConnTimeout:       transportIdleConnTimeout,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// The escape hatch back to per-request connections
			// (ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE / helm
			// router.roundTrip.disableKeepAlive); see
			// https://github.com/fission/fission/issues/723 for the original
			// stale-connection concern, now mitigated by FIN-driven pool
			// eviction, the short idle timeout, and the transport's automatic
			// retry of replayable requests on a reused-conn failure.
			DisableKeepAlives: p.disableKeepAlive,
		}
		p.otelTransport = otelhttp.NewTransport(p.transport)
	})
	return p.transport, p.otelTransport
}

// setContext returns a shallow copy of request with a new timeout context.
func (roundTripper *RetryingRoundTripper) setContext(req *http.Request) *http.Request {
	if roundTripper.closeContextFunc != nil {
		(*roundTripper.closeContextFunc)()
	}
	// pass request context as parent context for the case
	// that user aborts connection before timeout. Otherwise,
	// the request won't be canceled until the deadline exceeded
	// which may be a potential security issue.
	//
	// Streaming: the per-attempt context inherits the request context (which the
	// handler has already scoped to the idle Watchdog + max-duration ceiling).
	// No wall-clock funcTimeout deadline here, or the body copy would be killed
	// mid-stream. Classic: a fresh funcTimeout deadline per attempt (unchanged).
	if roundTripper.policy.streaming {
		ctx, closeCtx := context.WithCancel(req.Context())
		roundTripper.closeContextFunc = &closeCtx
		return req.WithContext(ctx)
	}
	ctx, closeCtx := context.WithTimeoutCause(req.Context(), roundTripper.funcTimeout, fmt.Errorf("roundtripper timeout (%f)s exceeded", roundTripper.funcTimeout.Seconds()))
	roundTripper.closeContextFunc = &closeCtx

	return req.WithContext(ctx)
}

// closeContext closes the context to release resources.
func (roundTripper *RetryingRoundTripper) closeContext() {
	if roundTripper.closeContextFunc != nil {
		(*roundTripper.closeContextFunc)()
	}
}

// debugDumpRequest logs a dump of the request (without body) at V(1); used
// only when the router runs with DEBUG_ENV.
func debugDumpRequest(logger logr.Logger, request *http.Request) {
	if request == nil {
		return
	}
	reqMsg, err := httputil.DumpRequest(request, false)
	if err != nil {
		logger.Error(err, "failed to dump request")
	} else {
		logger.V(1).Info("round tripper request", "request", string(reqMsg))
	}
}

// debugDumpResponse logs a dump of the response (without body) at V(1); used
// only when the router runs with DEBUG_ENV.
func debugDumpResponse(logger logr.Logger, response *http.Response) {
	if response == nil {
		return
	}
	respMsg, err := httputil.DumpResponse(response, false)
	if err != nil {
		logger.Error(err, "failed to dump response")
	} else {
		logger.V(1).Info("round tripper response", "response", string(respMsg))
	}
}
