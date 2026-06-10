// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/go-logr/logr"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/router/streaming"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

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
	tsRoundTripperParams     *tsRoundTripperParams
	isDebugEnv               bool
	functionTimeoutMap       map[k8stypes.UID]int
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
		logger:      fh.logger.WithName("roundtripper"),
		resolver:    fh.resolver,
		tapper:      fh.tapper,
		fn:          fh.function,
		trigger:     fh.httpTrigger,
		params:      fh.tsRoundTripperParams,
		isDebugEnv:  fh.isDebugEnv,
		funcTimeout: time.Duration(fnTimeout) * time.Second,
		policy:      policy,
	}

	start := time.Now()

	proxy := &httputil.ReverseProxy{
		Director:     director,
		Transport:    rrt,
		ErrorHandler: fh.getProxyErrorHandler(start, rrt),
		ModifyResponse: func(resp *http.Response) error {
			// One goroutine for metric collection + the cached-URL tap (the
			// historical pairing — the tap is a buffered channel send and does
			// not warrant a spawn of its own).
			go func() {
				fh.collectFunctionMetric(start, rrt, request, resp)
				if rrt.urlFromCache {
					fh.tapper.Tap(fh.function, rrt.serviceURL)
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
		// Streaming poolmgr functions untap here — after ServeHTTP has fully
		// drained the stream — rather than at RoundTrip return (headers). A
		// router-admitted endpoint returns its local slot instead of the RPC
		// untap.
		if policy.streaming && rrt.serviceURL != nil &&
			fh.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
			if rrt.release != nil {
				rrt.release()
			} else {
				fn, svcURL := fh.function, rrt.serviceURL
				go fh.tapper.UnTap(context.Background(), fn, svcURL) //nolint:errcheck
			}
		}
	}()

	otelUtils.SpanTrackEvent(request.Context(), "functionRequestProxy", otelUtils.GetAttributesForFunction(fh.function)...)
	proxy.ServeHTTP(responseWriter, request)
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

		go func() {
			fh.collectFunctionMetric(start, rrt, req, &http.Response{
				StatusCode:    status,
				ContentLength: req.ContentLength,
			})
			// tapService for cached service urls, matching the historical
			// error-path behavior (the tap used to ride inside
			// collectFunctionMetric).
			if rrt.urlFromCache {
				fh.tapper.Tap(fh.function, rrt.serviceURL)
			}
		}()

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
