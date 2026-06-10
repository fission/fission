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

		// streamIdleDefault is the idle timeout applied to streaming functions
		// when StreamingConfig.IdleTimeoutSeconds is unset (from the router's
		// ROUTER_STREAM_IDLE_TIMEOUT env, defaulting to DefaultStreamIdleSeconds).
		streamIdleDefault time.Duration

		// maxRetires is the max times for RetryingRoundTripper to retry a request.
		// Default maxRetries is 10, which means router will retry for
		// up to 10 times and abort it if still not succeeded.
		maxRetries int

		// svcAddrRetryCount is the max times for RetryingRoundTripper to retry with a specific service address
		// Router sends requests to a specific service address for each function.
		// A service address is considered as an invalid one if amount of non-network
		// errors router received is higher than svcAddrRetryCount.
		// Try to get a new one from executor.
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
		urlFromCache     bool
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

// RoundTrip is a custom transport with retries for http requests that forwards the request to the right serviceUrl, obtained
// from router's cache or from executor if router entry is stale.
//
// It first checks if the service address for this function came from router's cache.
// If it didn't, it makes a request to executor to get a new service for function. If that succeeds, it adds the address
// to it's cache and makes a request to that address with transport.RoundTrip call.
// Initial requests to new k8s services sometimes seem to fail, but retries work. So, it retries with an exponential
// back-off for maxRetries times.
//
// Else if it came from the cache, it makes a transport.RoundTrip with that cached address. If the response received is
// a network dial error (which means that the pod doesn't exist anymore), it removes the cache entry and makes a request
// to executor to get a new service for function. It then retries transport.RoundTrip with the new address.
//
// At any point in time, if the response received from transport.RoundTrip is other than dial network error, it is
// relayed as-is to the user, without any retries.
//
// While this RoundTripper handles the case where a previously cached address of the function pod isn't valid anymore
// (probably because the pod got deleted somehow), by making a request to executor to get a new service for this function,
// it doesn't handle a case where a newly specialized pod gets deleted just after the GetServiceForFunction succeeds.
// In such a case, the RoundTripper will retry requests against the new address and give up after maxRetries.
// However, the subsequent http call for this function will ensure the cache is invalidated.
//
// If GetServiceForFunction returns an error or if RoundTripper exits with an error, it gets translated into 502
// inside ServeHttp function of the reverseProxy.
// Earlier, GetServiceForFunction was called inside handler function and fission explicitly set http status code to 500
// if it returned an error.
func (roundTripper *RetryingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// set the timeout for transport context
	addForwardedHostHeader(roundTripper.logger, req)
	transport := roundTripper.getDefaultTransport()

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
				logger.Info("serviceURL is empty for function, retrying", "executingTimeout", executingTimeout)
				time.Sleep(jitter(executingTimeout))
				executingTimeout = executingTimeout * time.Duration(roundTripper.params.timeoutExponent)
				continue
			}
			otelUtils.SpanTrackEvent(ctx, "serviceEntryReceived", otelUtils.MapToAttributes(map[string]string{
				"function-name":      fnMeta.Name,
				"function-namespace": fnMeta.Namespace,
				"service-entry":      roundTripper.serviceURL.String()})...)
			// Streaming functions untap in handler (after ServeHTTP fully drains the
			// stream), not here at RoundTrip return (which fires at headers, while
			// the body is still streaming). A router-admitted endpoint (release !=
			// nil) returns its local slot instead of the RPC untap — the executor
			// did no accounting for it.
			if roundTripper.fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr &&
				!roundTripper.policy.streaming {
				defer func(fn *fv1.Function, serviceURL *url.URL, release func()) {
					if release != nil {
						release()
						return
					}
					go roundTripper.tapper.UnTap(context.Background(), fn, serviceURL) //nolint errcheck
				}(roundTripper.fn, roundTripper.serviceURL, roundTripper.release)
			}

			// rewrite the request to reflect the service url (which comes from
			// the executor response) and the trigger's prefix specification.
			rewriteFunctionURL(logger, req, roundTripper.trigger, fnMeta, roundTripper.serviceURL)
		}

		// over-riding default settings.
		transport.DialContext = (&net.Dialer{
			Timeout:   executingTimeout,
			KeepAlive: roundTripper.params.keepAliveTime,
		}).DialContext

		// Do NOT assign returned request to "req"
		// because the request used in the last round
		// will be canceled when calling setContext.
		newReq := roundTripper.setContext(req)

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
		var rt http.RoundTripper = otelhttp.NewTransport(transport)
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
			logger.Error(err, "encountered non-network dial error")
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

// getDefaultTransport returns a pointer to new copy of http.Transport object to prevent
// the value of http.DefaultTransport from being changed by goroutines.
func (roundTripper RetryingRoundTripper) getDefaultTransport() *http.Transport {
	// The transport setup here follows the configurations of http.DefaultTransport
	// but without Dialer since we will change it later.
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Default disables caching, Please refer to issue and specifically comment:
		// https://github.com/fission/fission/issues/723#issuecomment-398781995
		// You can change it by setting environment variable "ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE"
		// of router or helm variable "disableKeepAlive" before installation to false.
		DisableKeepAlives: roundTripper.params.disableKeepAlive,
	}
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
