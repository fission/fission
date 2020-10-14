/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/pkg/errors"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/zap"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	executorClient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/throttler"
)

const (
	FORWARDED        = "Forwarded"
	X_FORWARDED_HOST = "X-Forwarded-Host"
)

type (
	functionHandler struct {
		logger                   *zap.Logger
		fmap                     *functionServiceMap
		executor                 *executorClient.Client
		function                 *fv1.Function
		httpTrigger              *fv1.HTTPTrigger
		functionMap              map[string]*fv1.Function
		fnWeightDistributionList []FunctionWeightDistribution
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

	// A layer on top of http.DefaultTransport, with retries.
	RetryingRoundTripper struct {
		logger           *zap.Logger
		funcHandler      *functionHandler
		funcTimeout      time.Duration
		closeContextFunc *context.CancelFunc
		serviceUrl       *url.URL
		urlFromCache     bool
		totalRetry       int
	}

	// To keep the request body open during retries, we create an interface with Close operation being a no-op.
	// Details : https://github.com/flynn/flynn/pull/875
	fakeCloseReadCloser struct {
		io.ReadCloser
	}
)

func init() {
	// just seeding the random number for getting the canary function
	rand.Seed(time.Now().UnixNano())
}

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
// If GetServiceForFunction returns an error or if RoundTripper exits with an error, it get's translated into 502
// inside ServeHttp function of the reverseProxy.
// Earlier, GetServiceForFunction was called inside handler function and fission explicitly set http status code to 500
// if it returned an error.
func (roundTripper *RetryingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// set the timeout for transport context
	roundTripper.addForwardedHostHeader(req)

	transport := roundTripper.getDefaultTransport()
	ocRoundTripper := &ochttp.Transport{Base: transport}

	executingTimeout := roundTripper.funcHandler.tsRoundTripperParams.timeout

	// wrap the req.Body with another ReadCloser interface.
	if req.Body != nil {
		req.Body = &fakeCloseReadCloser{req.Body}
	}

	// close req body
	defer func() {
		if req.Body != nil {
			req.Body.(*fakeCloseReadCloser).RealClose()
		}
	}()

	roundTripper.logger.Debug("request headers", zap.Any("headers", req.Header))

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

	for i := 0; i < roundTripper.funcHandler.tsRoundTripperParams.maxRetries; i++ {
		// set service url of target service of request only when
		// trying to get new service url from cache/executor.
		if retryCounter == 0 {
			// get function service url from cache or executor
			roundTripper.serviceUrl, err = roundTripper.funcHandler.getServiceEntryFromExecutor()
			if err != nil {
				// We might want a specific error code or header for fission failures as opposed to
				// user function bugs.
				statusCode, errMsg := ferror.GetHTTPError(err)
				// if statusCode == http.StatusTooManyRequests {
				// 	time.Sleep(executingTimeout)
				// 	executingTimeout = executingTimeout * time.Duration(roundTripper.funcHandler.tsRoundTripperParams.timeoutExponent)
				// 	continue
				// } else {
				if roundTripper.funcHandler.isDebugEnv {
					return &http.Response{
						StatusCode:    statusCode,
						Proto:         req.Proto,
						ProtoMajor:    req.ProtoMajor,
						ProtoMinor:    req.ProtoMinor,
						Body:          ioutil.NopCloser(bytes.NewBufferString(errMsg)),
						ContentLength: int64(len(errMsg)),
						Request:       req,
						Header:        make(http.Header),
					}, nil
				}
				return nil, ferror.MakeError(http.StatusInternalServerError, err.Error())
				// }
			}
			if roundTripper.funcHandler.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
				defer func(fn *fv1.Function, serviceUrl *url.URL) {
					go roundTripper.funcHandler.unTapService(fn, serviceUrl)
				}(roundTripper.funcHandler.function, roundTripper.serviceUrl)
			}

			// modify the request to reflect the service url
			// this service url comes from executor response
			req.URL.Scheme = roundTripper.serviceUrl.Scheme
			req.URL.Host = roundTripper.serviceUrl.Host

			// To keep the function run container simple, it
			// doesn't do any routing.  In the future if we have
			// multiple functions per container, we could use the
			// function metadata here.
			// leave the query string intact (req.URL.RawQuery)
			req.URL.Path = "/"

			// Overwrite request host with internal host,
			// or request will be blocked in some situations
			// (e.g. istio-proxy)
			req.Host = roundTripper.serviceUrl.Host
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

		// forward the request to the function service
		resp, err := ocRoundTripper.RoundTrip(newReq)
		if err == nil {
			// return response back to user
			return resp, nil
		}

		roundTripper.totalRetry++

		if i >= roundTripper.funcHandler.tsRoundTripperParams.maxRetries-1 {
			// return here if we are in the last round
			roundTripper.logger.Error("error getting response from function",
				zap.String("function_name", fnMeta.Name),
				zap.Error(err))
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
			roundTripper.logger.Error("encountered non-network dial error", zap.Error(err))
			return resp, err
		}

		// close response body before entering next loop
		if resp != nil {
			resp.Body.Close()
		}

		// Check whether an error is an timeout error ("dial tcp i/o timeout").
		if isNetTimeoutErr {
			roundTripper.logger.Debug("request errored out - backing off before retrying",
				zap.String("url", req.URL.Host),
				zap.String("function_name", fnMeta.Name),
				zap.Error(err))
			retryCounter++
		}

		// If it's not a timeout error or retryCounter exceeded pre-defined threshold,
		if retryCounter >= roundTripper.funcHandler.tsRoundTripperParams.svcAddrRetryCount {
			roundTripper.logger.Debug(fmt.Sprintf(
				"retry counter exceeded pre-defined threshold of %v",
				roundTripper.funcHandler.tsRoundTripperParams.svcAddrRetryCount))
			retryCounter = 0
		}

		roundTripper.logger.Debug("Backing off before retrying", zap.Any("backoff_time", executingTimeout), zap.Error(err))
		time.Sleep(executingTimeout)
		executingTimeout = executingTimeout * time.Duration(roundTripper.funcHandler.tsRoundTripperParams.timeoutExponent)
	}

	e := errors.New("Unable to get service url for connection")
	roundTripper.logger.Error(e.Error(), zap.String("function_name", fnMeta.Name))
	return nil, e
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
	ctx, closeCtx := context.WithTimeout(req.Context(), roundTripper.funcTimeout)
	roundTripper.closeContextFunc = &closeCtx

	return req.WithContext(ctx)
}

// closeContext closes the context to release resources.
func (roundTripper *RetryingRoundTripper) closeContext() {
	if roundTripper.closeContextFunc != nil {
		(*roundTripper.closeContextFunc)()
	}
}

func (fh *functionHandler) tapService(fn *fv1.Function, serviceUrl *url.URL) {
	if fh.executor == nil {
		return
	}
	fh.executor.TapService(fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, serviceUrl)
}

func (fh functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	if fh.httpTrigger != nil && fh.httpTrigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionWeights {
		// canary deployment. need to determine the function to send request to now
		fn := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList)
		if fn == nil {
			fh.logger.Error("could not get canary backend",
				zap.Any("fnMap", fh.functionMap),
				zap.Any("distributionList", fh.fnWeightDistributionList))
			// TODO : write error to responseWrite and return response
			return
		}
		fh.function = fn
		fh.logger.Debug("chosen function backend's metadata", zap.Any("metadata", fh.function))
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

	fnTimeout := fh.functionTimeoutMap[fh.function.ObjectMeta.GetUID()]
	if fnTimeout == 0 {
		fnTimeout = fv1.DEFAULT_FUNCTION_TIMEOUT
	}

	rrt := &RetryingRoundTripper{
		logger:      fh.logger.Named("roundtripper"),
		funcHandler: &fh,
		funcTimeout: time.Duration(fnTimeout) * time.Second,
	}

	start := time.Now()

	proxy := &httputil.ReverseProxy{
		Director:     director,
		Transport:    rrt,
		ErrorHandler: fh.getProxyErrorHandler(start, rrt),
		ModifyResponse: func(resp *http.Response) error {
			go fh.collectFunctionMetric(start, rrt, request, resp)
			return nil
		},
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
		rrt.closeContext()
	}()

	proxy.ServeHTTP(responseWriter, request)
}

// findCeil picks a function from the functionWeightDistribution list based on the
// random number generated. It uses the prefix calculated for the function weights.
func findCeil(randomNumber int, wtDistrList []FunctionWeightDistribution) string {
	low := 0
	high := len(wtDistrList) - 1

	for {
		if low >= high {
			break
		}

		mid := low + high/2
		if randomNumber >= wtDistrList[mid].sumPrefix {
			low = mid + 1
		} else {
			high = mid
		}
	}

	if wtDistrList[low].sumPrefix >= randomNumber {
		return wtDistrList[low].name
	} else {
		return ""
	}
}

// picks a function to route to based on a random number generated
func getCanaryBackend(fnMap map[string]*fv1.Function, fnWtDistributionList []FunctionWeightDistribution) *fv1.Function {
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
		roundTripper.logger.Error("error parsing request url while adding forwarded host headers",
			zap.Error(err),
			zap.String("url", reqURL))
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
func (fh functionHandler) unTapService(fn *fv1.Function, serviceUrl *url.URL) error {
	fh.logger.Info("UnTapService Called")
	ctx, cancel := context.WithTimeout(context.Background(), fh.unTapServiceTimeout)
	defer cancel()
	err := fh.executor.UnTapService(ctx, fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, serviceUrl)
	if err != nil {
		statusCode, errMsg := ferror.GetHTTPError(err)
		fh.logger.Error("error from UnTapService",
			zap.Error(err),
			zap.String("error_message", errMsg),
			zap.Any("function", fh.function),
			zap.Int("status_code", statusCode))
		return err
	}
	return nil
}

// getServiceEntryFromExecutor returns service url entry returns from executor
func (fh functionHandler) getServiceEntryFromExecutor() (*url.URL, error) {
	// send a request to executor to specialize a new pod
	fh.logger.Debug("function timeout specified", zap.Int("timeout", fh.function.Spec.FunctionTimeout))
	timeout := 30 * time.Second
	if fh.function.Spec.FunctionTimeout > 0 {
		timeout = time.Second * time.Duration(fh.function.Spec.FunctionTimeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	service, err := fh.executor.GetServiceForFunction(ctx, &fh.function.ObjectMeta)
	if err != nil {
		statusCode, errMsg := ferror.GetHTTPError(err)
		fh.logger.Error("error from GetServiceForFunction",
			zap.Error(err),
			zap.String("error_message", errMsg),
			zap.Any("function", fh.function),
			zap.Int("status_code", statusCode))
		return nil, err
	}

	// parse the address into url
	serviceUrl, err := url.Parse(fmt.Sprintf("http://%v", service))
	if err != nil {
		fh.logger.Error("error parsing service url",
			zap.Error(err),
			zap.String("service_url", serviceUrl.String()))
		return nil, err
	}

	return serviceUrl, nil
}

// getProxyErrorHandler returns a reverse proxy error handler
func (fh functionHandler) getProxyErrorHandler(start time.Time, rrt *RetryingRoundTripper) func(rw http.ResponseWriter, req *http.Request, err error) {
	return func(rw http.ResponseWriter, req *http.Request, err error) {
		var status int
		var msg string

		switch err {
		case context.Canceled:
			// 499 CLIENT CLOSED REQUEST
			// A non-standard status code introduced by nginx for the case
			// when a client closes the connection while nginx is processing the request.
			// Reference: https://httpstatuses.com/499
			status = 499
			msg = "client closes the connection"
			fh.logger.Debug(msg, zap.Any("function", fh.function), zap.Any("request_header", req.Header))
		case context.DeadlineExceeded:
			status = http.StatusGatewayTimeout
			msg = "function not responses before the timeout"
			fh.logger.Error(msg, zap.Any("function", fh.function), zap.Any("request_header", req.Header))
		default:
			status = http.StatusBadGateway
			msg = "error sending request to function"
			fh.logger.Error(msg, zap.Error(err), zap.Any("function", fh.function), zap.Any("request_header", req.Header))
		}

		go fh.collectFunctionMetric(start, rrt, req, &http.Response{
			StatusCode:    status,
			ContentLength: 0,
		})

		// TODO: return error message that contains traceable UUID back to user. Issue #693
		rw.WriteHeader(status)
		rw.Write([]byte(msg))
	}
}

func (fh functionHandler) collectFunctionMetric(start time.Time, rrt *RetryingRoundTripper, req *http.Request, resp *http.Response) {
	duration := time.Since(start)

	// Metrics stuff
	funcMetricLabels := &functionLabels{
		namespace: fh.function.ObjectMeta.Namespace,
		name:      fh.function.ObjectMeta.Name,
	}
	httpMetricLabels := &httpLabels{
		method: req.Method,
	}
	if fh.httpTrigger != nil {
		httpMetricLabels.host = fh.httpTrigger.Spec.Host
		httpMetricLabels.path = fh.httpTrigger.Spec.RelativeURL
	}

	// Track metrics
	httpMetricLabels.code = resp.StatusCode
	funcMetricLabels.cached = rrt.urlFromCache

	functionCallCompleted(funcMetricLabels, httpMetricLabels,
		duration, duration, resp.ContentLength)

	// tapService before invoking roundTrip for the serviceUrl
	if rrt.urlFromCache {
		fh.tapService(fh.function, rrt.serviceUrl)
	}

	fh.logger.Debug("Request complete", zap.String("function", fh.function.ObjectMeta.Name),
		zap.Int("retry", rrt.totalRetry), zap.Duration("total-time", duration),
		zap.Int64("content-length", resp.ContentLength))
}
