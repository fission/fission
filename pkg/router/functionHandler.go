// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"errors"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/router/streaming"
	routerutil "github.com/fission/fission/pkg/router/util"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

const (
	// FORWARDED represents the 'Forwarded' request header
	FORWARDED = "Forwarded"

	// X_FORWARDED_HOST represents the 'X_FORWARDED_HOST' request header
	X_FORWARDED_HOST = "X-Forwarded-Host"
)

// Stream-abort causes, attached to the request context via context.WithCancelCause
// so the proxy error handler can distinguish a server-initiated stream abort from
// a genuine client disconnect (which also surfaces as context.Canceled).
var (
	errStreamIdleTimeout = errors.New("stream aborted: idle timeout")
	errStreamMaxDuration = errors.New("stream aborted: max duration")
)

type (
	functionHandler struct {
		logger logr.Logger
		fmap   *functionServiceMap
		// reader is the Manager's cache-backed client, used to re-read the current
		// Function before asking the executor to specialize it (the resolved
		// `function` snapshot can be stale — see getServiceEntryFromExecutor).
		reader                   client.Reader
		executor                 eclient.ClientInterface
		function                 *fv1.Function
		httpTrigger              *fv1.HTTPTrigger
		functionMap              map[string]*fv1.Function
		fnWeightDistributionList []functionWeightDistribution
		tsRoundTripperParams     *tsRoundTripperParams
		isDebugEnv               bool
		svcAddrUpdateThrottler   *throttler.Throttler
		functionTimeoutMap       map[k8stypes.UID]int
		unTapServiceTimeout      time.Duration
	}

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

	// RetryingRoundTripper is a layer on top of http.DefaultTransport, with retries.
	RetryingRoundTripper struct {
		logger           logr.Logger
		funcHandler      *functionHandler
		funcTimeout      time.Duration
		policy           proxyPolicy // resolved once in handler; drives streaming behavior
		closeContextFunc *context.CancelFunc
		serviceURL       *url.URL
		urlFromCache     bool
		totalRetry       int
	}

	// To keep the request body open during retries, we create an interface with Close operation being a no-op.
	// Details : https://github.com/flynn/flynn/pull/875
	fakeCloseReadCloser struct {
		io.ReadCloser
	}

	svcEntryRecord struct {
		svcURL   *url.URL
		cacheHit bool
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
	roundTripper.addForwardedHostHeader(req)
	transport := roundTripper.getDefaultTransport()

	executingTimeout := roundTripper.funcHandler.tsRoundTripperParams.timeout

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
	var fnMeta = &roundTripper.funcHandler.function.ObjectMeta

	logger := otelUtils.LoggerWithTraceID(ctx, roundTripper.logger).WithValues("function", fnMeta.Name, "namespace", fnMeta.Namespace)

	dumpReqFunc := func(request *http.Request) {
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
	dumpRespFunc := func(response *http.Response) {
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

	for i := 0; i < roundTripper.funcHandler.tsRoundTripperParams.maxRetries; i++ {
		// set service url of target service of request only when
		// trying to get new service url from cache/executor.
		if retryCounter == 0 {
			otelUtils.SpanTrackEvent(ctx, "getServiceEntry", otelUtils.MapToAttributes(map[string]string{
				"function-name":      fnMeta.Name,
				"function-namespace": fnMeta.Namespace})...)
			// get function service url from cache or executor
			roundTripper.serviceURL, roundTripper.urlFromCache, err = roundTripper.funcHandler.getServiceEntry(ctx)
			if err != nil {
				// We might want a specific error code or header for fission failures as opposed to
				// user function bugs.
				statusCode, errMsg := ferror.GetHTTPError(err)
				if statusCode == http.StatusTooManyRequests {
					return nil, err
				}
				if roundTripper.funcHandler.isDebugEnv {
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
				executingTimeout = executingTimeout * time.Duration(roundTripper.funcHandler.tsRoundTripperParams.timeoutExponent)
				continue
			}
			otelUtils.SpanTrackEvent(ctx, "serviceEntryReceived", otelUtils.MapToAttributes(map[string]string{
				"function-name":      fnMeta.Name,
				"function-namespace": fnMeta.Namespace,
				"service-entry":      roundTripper.serviceURL.String()})...)
			// Streaming functions untap in handler (after ServeHTTP fully drains the
			// stream), not here at RoundTrip return (which fires at headers, while
			// the body is still streaming).
			if roundTripper.funcHandler.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr &&
				!roundTripper.policy.streaming {
				defer func(ctx context.Context, fn *fv1.Function, serviceURL *url.URL) {
					go roundTripper.funcHandler.unTapService(context.Background(), fn, serviceURL) //nolint errcheck
				}(ctx, roundTripper.funcHandler.function, roundTripper.serviceURL)
			}

			// modify the request to reflect the service url
			// this service url comes from executor response
			req.URL.Scheme = roundTripper.serviceURL.Scheme
			req.URL.Host = roundTripper.serviceURL.Host

			// With addition of routing support from functions if function supports routing,
			// 1. we trim prefix url and forward request
			// 2. otherwise we just keep default request to root path
			// We leave the query string intact (req.URL.RawQuery) where as we manipuate
			// req.URL.Path according to httpTrigger specification.
			prefixTrim := ""
			functionURL := utils.UrlForFunction(fnMeta.Name, fnMeta.Namespace)
			keepPrefix := false
			if roundTripper.funcHandler.httpTrigger != nil && roundTripper.funcHandler.httpTrigger.Spec.Prefix != nil && *roundTripper.funcHandler.httpTrigger.Spec.Prefix != "" {
				prefixTrim = *roundTripper.funcHandler.httpTrigger.Spec.Prefix
				keepPrefix = roundTripper.funcHandler.httpTrigger.Spec.KeepPrefix
			} else if strings.HasPrefix(req.URL.Path, functionURL) {
				prefixTrim = functionURL
			}
			if prefixTrim != "" {
				if !keepPrefix {
					req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixTrim)
				}
				if !strings.HasPrefix(req.URL.Path, "/") {
					req.URL.Path = "/" + req.URL.Path
				}
			} else {
				req.URL.Path = "/"
			}

			logger.V(1).Info("function invoke url",
				"prefixTrim", prefixTrim,
				"keepPrefix", keepPrefix,
				"hitURL", req.URL.Path)
			// Overwrite request host with internal host,
			// or request will be blocked in some situations
			// (e.g. istio-proxy)
			req.Host = roundTripper.serviceURL.Host
		}

		// over-riding default settings.
		transport.DialContext = (&net.Dialer{
			Timeout:   executingTimeout,
			KeepAlive: roundTripper.funcHandler.tsRoundTripperParams.keepAliveTime,
		}).DialContext

		// Do NOT assign returned request to "req"
		// because the request used in the last round
		// will be canceled when calling setContext.
		newReq := roundTripper.setContext(req)

		if roundTripper.funcHandler.isDebugEnv {
			dumpReqFunc(newReq)
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
		if roundTripper.funcHandler.isDebugEnv {
			dumpRespFunc(resp)
		}
		if err == nil {
			// return response back to user
			return resp, nil
		}

		roundTripper.totalRetry++

		if i >= roundTripper.funcHandler.tsRoundTripperParams.maxRetries-1 {
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

		// Check whether an error is an timeout error ("dial tcp i/o timeout").
		if isNetTimeoutErr {
			logger.V(1).Info("request errored out - backing off before retrying",
				"url", req.URL.Host, "error", err.Error())
			retryCounter++
		}

		// If it's not a timeout error or retryCounter exceeded pre-defined threshold,
		if retryCounter >= roundTripper.funcHandler.tsRoundTripperParams.svcAddrRetryCount {
			logger.V(1).Info(fmt.Sprintf(
				"retry counter exceeded pre-defined threshold of %v",
				roundTripper.funcHandler.tsRoundTripperParams.svcAddrRetryCount))
			if roundTripper.urlFromCache {
				roundTripper.funcHandler.removeServiceEntryFromCache()
			}
			retryCounter = 0
		}

		logger.V(1).Info("Backing off before retrying", "backoff_time", executingTimeout, "error", err.Error())
		time.Sleep(jitter(executingTimeout))
		executingTimeout = executingTimeout * time.Duration(roundTripper.funcHandler.tsRoundTripperParams.timeoutExponent)
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
		DisableKeepAlives: roundTripper.funcHandler.tsRoundTripperParams.disableKeepAlive,
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

func (fh *functionHandler) tapService(fn *fv1.Function, serviceURL *url.URL) {
	if fh.executor == nil || serviceURL == nil {
		return
	}
	fh.executor.TapService(fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, *serviceURL)
}

func (fh functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	if fh.httpTrigger != nil && fh.httpTrigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionWeights {
		// canary deployment. need to determine the function to send request to now
		fn := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList)
		if fn == nil {
			fh.logger.Error(nil, "could not get canary backend",
				"fnMap", fh.functionMap,
				"distributionList", fh.fnWeightDistributionList)
			// TODO : write error to responseWrite and return response
			return
		}
		fh.function = fn
		fh.logger.V(1).Info("chosen function backend's metadata", "metadata", fh.function)
	}

	// url path
	setPathInfoToHeader(request)

	// system params
	setFunctionMetadataToHeader(&fh.function.ObjectMeta, request)

	director := func(req *http.Request) {
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}

	fnTimeout := fh.functionTimeoutMap[fh.function.GetUID()]
	if fnTimeout == 0 {
		fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
	}

	policy := resolveProxyPolicy(fh.function,
		time.Duration(fnTimeout)*time.Second,
		fh.tsRoundTripperParams.streamIdleDefault)

	// Streaming: scope the request to (a) a max-duration ceiling (if any) and (b)
	// an idle Watchdog re-armed on each upstream chunk. Both cancel the request
	// context, which tears the upstream connection down. Classic path: the request
	// context is used unchanged (byte-identical behavior).
	var (
		streamCancel context.CancelCauseFunc
		watchdog     *streaming.Watchdog
	)
	if policy.streaming {
		ctx, cancel := context.WithCancelCause(request.Context())
		streamCancel = cancel
		// The cancel callbacks log the abort at Info — this is the authoritative
		// signal, and the only one for a mid-stream abort (once headers are
		// flushed the status is already 200 and the proxy error handler never
		// runs, so without this a cut LLM/SSE stream would be silent).
		fnMeta := &fh.function.ObjectMeta
		if policy.maxDuration > 0 {
			timer := time.AfterFunc(policy.maxDuration, func() {
				fh.logger.Info("stream aborted: max duration exceeded",
					"function", fnMeta.Name, "namespace", fnMeta.Namespace, "maxDuration", policy.maxDuration)
				cancel(fmt.Errorf("%w (%s)", errStreamMaxDuration, policy.maxDuration))
			})
			context.AfterFunc(ctx, func() { timer.Stop() })
		}
		watchdog = streaming.NewWatchdog(policy.idleTimeout, func() {
			fh.logger.Info("stream aborted: idle timeout exceeded",
				"function", fnMeta.Name, "namespace", fnMeta.Namespace, "idleTimeout", policy.idleTimeout)
			cancel(fmt.Errorf("%w (%s)", errStreamIdleTimeout, policy.idleTimeout))
		})
		// Arm now (not at headers) so the idle timeout also bounds time-to-first-byte:
		// a streaming function that accepts the connection but never responds is
		// aborted at the idle window rather than hanging until the client disconnects.
		watchdog.Start()
		request = request.WithContext(ctx)
	}

	rrt := &RetryingRoundTripper{
		logger:      fh.logger.WithName("roundtripper"),
		funcHandler: &fh,
		funcTimeout: time.Duration(fnTimeout) * time.Second,
		policy:      policy,
	}

	start := time.Now()

	proxy := &httputil.ReverseProxy{
		Director:     director,
		Transport:    rrt,
		ErrorHandler: fh.getProxyErrorHandler(start, rrt),
		ModifyResponse: func(resp *http.Response) error {
			go fh.collectFunctionMetric(start, rrt, request, resp)
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
		// Streaming poolmgr functions untap here — after ServeHTTP has fully
		// drained the stream — rather than at RoundTrip return (headers).
		if policy.streaming && rrt.serviceURL != nil &&
			fh.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
			fn, svcURL := fh.function, rrt.serviceURL
			go fh.unTapService(context.Background(), fn, svcURL) //nolint:errcheck
		}
	}()

	otelUtils.SpanTrackEvent(request.Context(), "functionRequestProxy", otelUtils.GetAttributesForFunction(fh.function)...)
	proxy.ServeHTTP(responseWriter, request)
}

// onStreamResponse wires the streaming response: it arms the idle Watchdog, wraps
// resp.Body so each upstream chunk re-arms the idle window, and (for poolmgr)
// launches a keepalive heartbeat so the pod is not idle-reaped mid-stream. ctx is
// the stream context (cancelled on idle/max/client-disconnect), which also stops
// the heartbeat.
func (fh *functionHandler) onStreamResponse(ctx context.Context, rrt *RetryingRoundTripper, w *streaming.Watchdog, resp *http.Response) {
	// Keep the poolmgr pod tapped for the connection's lifetime — the router-driven,
	// environment-agnostic replacement for the legacy WebsocketFsvc reaper skip.
	// Covers SSE/chunked and WebSocket alike (ServeHTTP blocks until the socket
	// closes, so the handler defer untaps at the right time).
	if fh.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		interval := rrt.policy.idleTimeout / 2
		if interval <= 0 || interval > 30*time.Second {
			interval = 30 * time.Second
		}
		fh.startKeepaliveHeartbeat(ctx, fh.function, rrt.serviceURL, interval)
	}

	// A hijacked WebSocket (101) keeps resp.Body as an io.ReadWriteCloser that
	// ReverseProxy hijacks to pipe bytes both ways — we must NOT wrap it (the
	// wrapper is read-only and would break the hijack). Idle is not observable
	// without body reads, so rely on the heartbeat + TCP keepalive + the optional
	// max-duration ceiling.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if w != nil {
			w.Stop()
		}
		return
	}

	// SSE/chunked: the idle Watchdog was already armed in handler (so it also
	// covers time-to-first-byte); wrap resp.Body so each upstream chunk re-arms it.
	if resp.Body == nil {
		return
	}
	resp.Body = streaming.NewActivityReadCloser(
		resp.Body,
		func() {
			if w != nil {
				w.Reset()
			}
		},
		func() {}, // untap is handled by the handler defer
	)
}

// startKeepaliveHeartbeat re-taps the poolmgr service on an interval so the
// executor's idle reaper sees a fresh Atime for the lifetime of a stream. Stops
// when ctx is done (handler defer / client disconnect / idle/max cancel).
func (fh *functionHandler) startKeepaliveHeartbeat(ctx context.Context, fn *fv1.Function, serviceURL *url.URL, interval time.Duration) {
	if interval <= 0 || serviceURL == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fh.tapService(fn, serviceURL)
			}
		}
	}()
}

// findCeil picks a function from the functionWeightDistribution list based on the
// random number generated. It uses the prefix calculated for the function weights.
func findCeil(randomNumber int, wtDistrList []functionWeightDistribution) string {
	low := 0
	high := len(wtDistrList) - 1

	for low < high {
		mid := (low + high) / 2
		if randomNumber >= wtDistrList[mid].sumPrefix {
			low = mid + 1
		} else {
			high = mid
		}
	}

	if wtDistrList[low].sumPrefix >= randomNumber {
		return wtDistrList[low].name
	}
	return ""
}

// picks a function to route to based on a random number generated
func getCanaryBackend(fnMap map[string]*fv1.Function, fnWtDistributionList []functionWeightDistribution) *fv1.Function {
	randomNumber := rand.Intn(fnWtDistributionList[len(fnWtDistributionList)-1].sumPrefix + 1)
	fnName := findCeil(randomNumber, fnWtDistributionList)
	return fnMap[fnName]
}

// addForwardedHostHeader add "forwarded host" to request header
func (roundTripper RetryingRoundTripper) addForwardedHostHeader(req *http.Request) {
	// for more detailed information, please visit:
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Forwarded

	if len(req.Header.Get(FORWARDED)) > 0 || len(req.Header.Get(X_FORWARDED_HOST)) > 0 {
		// forwarded headers were set by external proxy, leave them intact
		return
	}

	// Format of req.Host is <host>:<port>
	// We need to extract hostname from it, than
	// check whether a host is ipv4 or ipv6 or FQDN
	reqURL := fmt.Sprintf("%s://%s", req.Proto, req.Host)
	u, err := url.Parse(reqURL)
	if err != nil {
		roundTripper.logger.Error(err, "error parsing request url while adding forwarded host headers", "url", reqURL)
		return
	}

	var host string

	// ip will be nil if the Hostname is a FQDN string
	ip := net.ParseIP(u.Hostname())

	// ip == nil -> hostname is FQDN instead of ip address
	// The order of To4() and To16() here matters, To16() will
	// converts an IPv4 address to IPv6 format address and may
	// cause router append wrong host value to header. To prevent
	// this we need to check whether To4() is nil first.
	if ip == nil || (ip != nil && ip.To4() != nil) {
		host = fmt.Sprintf(`host=%s;`, req.Host)
	} else if ip != nil && ip.To16() != nil {
		// For the "Forwarded" header, if a host is an IPv6 address it should be quoted
		host = fmt.Sprintf(`host="%s";`, req.Host)
	}

	req.Header.Set(FORWARDED, host)
	req.Header.Set(X_FORWARDED_HOST, req.Host)
}

// unTapservice marks the serviceURL in executor's cache as inactive, so that it can be reused
func (fh functionHandler) unTapService(ctx context.Context, fn *fv1.Function, serviceUrl *url.URL) error {
	fh.logger.V(1).Info("UnTapService Called")
	ctx, cancel := context.WithTimeoutCause(ctx, fh.unTapServiceTimeout, fmt.Errorf("unTapService timeout (%f)s exceeded", fh.unTapServiceTimeout.Seconds()))
	defer cancel()
	err := fh.executor.UnTapService(ctx, fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, serviceUrl)
	if err != nil {
		statusCode, errMsg := ferror.GetHTTPError(err)
		fh.logger.Error(err, "error from UnTapService", "error_message", errMsg,
			"function", fh.function,
			"status_code", statusCode)
		return err
	}
	return nil
}

// getServiceEntryFromCache returns service url entry returns from cache
func (fh functionHandler) getServiceEntryFromCache() (serviceUrl *url.URL, err error) {
	// cache lookup to get serviceUrl
	serviceUrl, err = fh.fmap.lookup(&fh.function.ObjectMeta)
	if err != nil {
		var errMsg string

		e, ok := err.(ferror.Error)
		if !ok {
			errMsg = fmt.Sprintf("Unknown error when looking up service entry: %v", err)
		} else {
			// Ignore ErrorNotFound error here, it's an expected error,
			// roundTripper will try to get service url later.
			if e.Code == ferror.ErrorNotFound {
				return nil, nil
			}
			errMsg = fmt.Sprintf("Error getting function %v;s service entry from cache: %v", fh.function.Name, err)
		}
		return nil, ferror.MakeError(http.StatusInternalServerError, errMsg)
	}
	return serviceUrl, nil
}

// addServiceEntryToCache add service url entry to cache
func (fh functionHandler) addServiceEntryToCache(serviceURL *url.URL) {
	fh.fmap.assign(&fh.function.ObjectMeta, serviceURL)
}

// removeServiceEntryFromCache removes service url entry from cache
func (fh functionHandler) removeServiceEntryFromCache() {
	fh.fmap.remove(&fh.function.ObjectMeta)
}

func (fh functionHandler) getServiceEntryFromExecutor(ctx context.Context) (serviceUrl *url.URL, err error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fh.logger)

	// The executor specializes a pod from exactly this function's package spec. The
	// resolved `function` snapshot can be stale: the resolver caches the resolved
	// function keyed by the *trigger's* ResourceVersion, so a `fission fn update
	// --pkg` (which changes the function but not the trigger) doesn't invalidate
	// it. For poolmgr — which specializes on demand from the function we pass —
	// that means the executor would keep serving the old package. Re-read the
	// current Function from the Manager cache so the latest spec is specialized.
	fn := fh.function
	if fh.reader != nil {
		fresh := &fv1.Function{}
		if gerr := fh.reader.Get(ctx, k8stypes.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}, fresh); gerr == nil {
			fn = fresh
		} else {
			logger.V(1).Info("could not re-read current function; using resolved snapshot",
				"function", fn.Name, "namespace", fn.Namespace, "error", gerr)
		}
	}

	// send a request to executor to specialize a new pod
	fh.logger.V(1).Info("function timeout specified", "timeout", fn.Spec.FunctionTimeout)

	var fContext context.Context
	if fn.Spec.FunctionTimeout > 0 {
		timeout := time.Second * time.Duration(fn.Spec.FunctionTimeout)
		f, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("function service entry timeout (%f)s exceeded", timeout.Seconds()))
		fContext = f
		defer cancel()
	} else {
		fContext = ctx
	}

	service, err := fh.executor.GetServiceForFunction(fContext, fn)
	if err != nil {
		statusCode, errMsg := ferror.GetHTTPError(err)
		logger.Error(err, "error from GetServiceForFunction", "error_message", errMsg,
			"function", fn,
			"status_code", statusCode)
		return nil, err
	}
	// parse the address into url
	rawURL := fmt.Sprintf("http://%v", service)
	svcURL, err := url.Parse(rawURL)
	if err != nil {
		// svcURL is nil on a parse error — log the raw string, not svcURL.String().
		logger.Error(err, "error parsing service url", "service_url", rawURL)
		return nil, err
	}
	return svcURL, err
}

// getServiceEntryFromExecutor returns service url entry returns from executor
func (fh functionHandler) getServiceEntry(ctx context.Context) (svcURL *url.URL, cacheHit bool, err error) {
	if fh.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		svcURL, err = fh.getServiceEntryFromExecutor(ctx)
		return svcURL, false, err
	}
	// Check if service URL present in cache
	svcURL, err = fh.getServiceEntryFromCache()
	if err == nil && svcURL != nil {
		return svcURL, true, nil
	} else if err != nil {
		return nil, false, err
	}

	fnMeta := &fh.function.ObjectMeta
	recordObj, err := fh.svcAddrUpdateThrottler.RunOnce(
		crd.CacheKeyURFromMeta(fnMeta).String(),
		func(firstToTheLock bool) (any, error) {
			if !firstToTheLock {
				svcURL, err := fh.getServiceEntryFromCache()
				if err != nil {
					return nil, err
				}
				return svcEntryRecord{svcURL: svcURL, cacheHit: true}, err
			}
			svcURL, err = fh.getServiceEntryFromExecutor(ctx)
			if err != nil {
				return nil, err
			}
			fh.addServiceEntryToCache(svcURL)
			return svcEntryRecord{
				svcURL:   svcURL,
				cacheHit: false,
			}, nil
		},
	)

	if recordObj == nil {
		return nil, false, fmt.Errorf("empty service entry: %w", err)
	}

	record, ok := recordObj.(svcEntryRecord)
	if !ok {
		return nil, false, fmt.Errorf("unexpected type of recordObj %T: %w", recordObj, err)
	}
	return record.svcURL, record.cacheHit, err
}

// getProxyErrorHandler returns a reverse proxy error handler
func (fh functionHandler) getProxyErrorHandler(start time.Time, rrt *RetryingRoundTripper) func(rw http.ResponseWriter, req *http.Request, err error) {
	return func(rw http.ResponseWriter, req *http.Request, err error) {
		var status int
		var msg string
		ctx := req.Context()
		logger := otelUtils.LoggerWithTraceID(ctx, fh.logger)
		// A server-initiated streaming abort (idle/max-duration) surfaces as
		// context.Canceled too, but carries a cause we set via WithCancelCause.
		// Surface it as a 504 with the real reason instead of masquerading as a
		// client-close 499, and log it where an operator can see it.
		streamCause := context.Cause(ctx)
		switch {
		case errors.Is(streamCause, errStreamIdleTimeout) || errors.Is(streamCause, errStreamMaxDuration):
			status = http.StatusGatewayTimeout
			msg = streamCause.Error()
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
			logger.V(1).Info(msg, "function", fh.function, "status", "Client Closed Request")
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
			msg = "no response from function before timeout"
			logger.Info(msg, "function", fh.function, "status", http.StatusText(status))
		default:
			code, _ := ferror.GetHTTPError(err)
			status = code
			msg = "error sending request to function"
			logger.Info(msg, "function", fh.function,
				"status", http.StatusText(status), "code", code)
		}

		go fh.collectFunctionMetric(start, rrt, req, &http.Response{
			StatusCode:    status,
			ContentLength: req.ContentLength,
		})

		// TODO: return error message that contains traceable UUID back to user. Issue #693
		rw.WriteHeader(status)
		_, err = rw.Write([]byte(msg))
		if err != nil {
			logger.Error(err,
				"error writing HTTP response", "function", fh.function,
			)
		}
	}
}

func (fh functionHandler) collectFunctionMetric(start time.Time, rrt *RetryingRoundTripper, req *http.Request, resp *http.Response) {
	duration := time.Since(start)
	var path string

	if fh.httpTrigger != nil {
		if fh.httpTrigger.Spec.Prefix != nil && *fh.httpTrigger.Spec.Prefix != "" {
			path = *fh.httpTrigger.Spec.Prefix
		} else {
			path = fh.httpTrigger.Spec.RelativeURL
		}
	}

	functionCalls.WithLabelValues(fh.function.ObjectMeta.Namespace,
		fh.function.ObjectMeta.Name, path, req.Method,
		fmt.Sprint(resp.StatusCode)).Inc()

	if resp.StatusCode >= 400 {
		functionCallErrors.WithLabelValues(fh.function.ObjectMeta.Namespace,
			fh.function.ObjectMeta.Name, path, req.Method,
			fmt.Sprint(resp.StatusCode)).Inc()
	}

	functionCallOverhead.WithLabelValues(fh.function.ObjectMeta.Namespace,
		fh.function.ObjectMeta.Name, path, req.Method,
		fmt.Sprint(resp.StatusCode)).
		Observe(float64(duration.Nanoseconds()) / 1e9)

	// tapService before invoking roundTrip for the serviceUrl
	if rrt.urlFromCache {
		fh.tapService(fh.function, rrt.serviceURL)
	}

	fh.logger.V(1).Info("Request complete", "function", fh.function.Name,
		"retry", rrt.totalRetry, "total-time", duration,
		"content-length", resp.ContentLength)
}
