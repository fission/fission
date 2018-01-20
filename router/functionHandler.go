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
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	executorClient "github.com/fission/fission/executor/client"
	"github.com/prometheus/client_golang/prometheus"

	"strconv"
)

type functionHandler struct {
	fmap     *functionServiceMap
	fmetrics *functionMetricsMap
	executor *executorClient.Client
	function *metav1.ObjectMeta
}

func (fh *functionHandler) getServiceForFunction() (*url.URL, error) {
	// call executor, get a url for a function
	svcName, err := fh.executor.GetServiceForFunction(fh.function)
	if err != nil {
		return nil, err
	}
	svcUrl, err := url.Parse(fmt.Sprintf("http://%v", svcName))
	if err != nil {
		return nil, err
	}
	return svcUrl, nil
}

// A layer on top of http.DefaultTransport, with retries.
type RetryingRoundTripper struct {
	maxRetries    int
	initalTimeout time.Duration
}

func (rrt RetryingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	timeout := rrt.initalTimeout
	transport := http.DefaultTransport.(*http.Transport)

	// Do max-1 retries; the last one uses default transport timeouts
	for i := rrt.maxRetries - 1; i > 0; i-- {
		// update timeout in transport
		transport.DialContext = (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext

		resp, err := transport.RoundTrip(req)
		if err == nil {
			return resp, nil
		}

		timeout *= time.Duration(2)
		log.Printf("Retrying request to %v in %v", req.URL.Host, timeout)
		time.Sleep(timeout)
	}

	// finally, one more retry with the default timeout
	return http.DefaultTransport.RoundTrip(req)
}

func (fh *functionHandler) tapService(serviceUrl *url.URL) {
	if fh.executor == nil {
		return
	}
	fh.executor.TapService(serviceUrl)
}

func (lrw *LoggedResponse) WriteHeader(code int) {

	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (fh *functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	reqStartTime := time.Now()

	loggedWriter := &LoggedResponse{
		ResponseWriter: responseWriter,
		status:         200,
	}
	//TODO when the FunctionMetricsMap cache misses, we create and register ALL the prometheus metrics
	log.Println("incrementing prometheus request counter")
	metrics, err := fh.fmetrics.lookup(fh.function)
	if err != nil {
		log.Println("functionmetricsmap cache miss, create and register Prometheus metrics")
		//function metrics cache miss
		requestCounter := prometheus.NewCounter(prometheus.CounterOpts{
			Name: fh.function.Name + "_request_count",
			Help: "Number of requests to this particular function route",
		})
		errorCounter := prometheus.NewCounter(prometheus.CounterOpts{
			Name: fh.function.Name + "_error_count",
			Help: "Number of errors from this particular function",
		})
		executorLatencyObserver := prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: fh.function.Name + "_latency_overhead",
			Help: "Time this function spent waiting for poolmgr/executor",
		})
		totalLatencyObserver := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: fh.function.Name + "_total_latency",
			Help: "Total request latency from beginning of handler() at reqStartTime to when the request is Served",
		},
			[]string{"code", "method"})

		regErr := prometheus.Register(requestCounter)
		if regErr != nil {
			log.Println("error registering request counter: ", regErr)
			return
		}
		requestCounter.Inc()

		regErr = prometheus.Register(errorCounter)
		if regErr != nil {
			log.Println("error registering error counter: ", regErr)
			return
		}

		regErr = prometheus.Register(executorLatencyObserver)
		if regErr != nil {
			log.Println("error registering latency overhead observer: ", regErr)
			return
		}

		regErr = prometheus.Register(totalLatencyObserver)
		if regErr != nil {
			log.Println("error registering total latency observer: ", regErr)
			return
		}

		newfunctionMetrics := &functionMetrics{
			requestCount:       requestCounter,
			functionErrorCount: errorCounter,
			executorLatency:    executorLatencyObserver,
			totalLatency:       totalLatencyObserver,
		}
		fh.fmetrics.assign(fh.function, newfunctionMetrics)
	} else {
		metrics.requestCount.Inc()
	}

	metrics, err = fh.fmetrics.lookup(fh.function)
	if err != nil {
		log.Printf("Failed to get cached function metrics for function %v", fh.function.Name)
		// We might want a specific error code or header for fission
		// failures as opposed to user function bugs.
		http.Error(responseWriter, "Internal server error (fission)", 500)
		return
	}

	// retrieve url params and add them to request header
	vars := mux.Vars(request)
	for k, v := range vars {
		request.Header.Add(fmt.Sprintf("X-Fission-Params-%v", k), v)
	}

	// System Params
	MetadataToHeaders(HEADERS_FISSION_FUNCTION_PREFIX, fh.function, request)

	// cache lookup

	serviceUrl, err := fh.fmap.lookup(fh.function)
	executorLatencyStart := time.Now()
	if err != nil {
		// Cache miss: request the Pool Manager to make a new service.
		log.Printf("Not cached, getting new service for %v", fh.function)

		var poolErr error
		serviceUrl, poolErr = fh.getServiceForFunction()
		executorTimeElapsed := time.Since(executorLatencyStart)
		msExecutorLatency := executorTimeElapsed / time.Millisecond
		metrics.executorLatency.Observe(float64(msExecutorLatency))

		if poolErr != nil {
			log.Printf("Failed to get service for function %v: %v", fh.function.Name, poolErr)
			// We might want a specific error code or header for fission
			// failures as opposed to user function bugs.
			http.Error(responseWriter, "Internal server error (fission)", 500)
			return
		}

		// add it to the map
		fh.fmap.assign(fh.function, serviceUrl)
	} else {
		// if we're using our cache, asynchronously tell
		// executor we're using this service
		go fh.tapService(serviceUrl)
		executorTimeElapsed := time.Since(executorLatencyStart)
		msExecutorLatency := executorTimeElapsed / time.Millisecond
		metrics.executorLatency.Observe(float64(msExecutorLatency))
	}

	// Proxy off our request to the serviceUrl, and send the response back.
	// TODO: As an optimization we may want to cache proxies too -- this might get us
	// connection reuse and possibly better performance
	director := func(req *http.Request) {
		log.Printf("Proxying request for %v to %v", req.URL, serviceUrl.Host)

		// send this request to serviceurl
		req.URL.Scheme = serviceUrl.Scheme
		req.URL.Host = serviceUrl.Host

		// To keep the function run container simple, it
		// doesn't do any routing.  In the future if we have
		// multiple functions per container, we could use the
		// function metadata here.
		req.URL.Path = "/"

		// Overwrite request host with internal host,
		// or request will be blocked in some situations
		// (e.g. istio-proxy)
		req.Host = serviceUrl.Host

		// leave the query string intact (req.URL.RawQuery)

		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}

	// Initial requests to new k8s services sometimes seem to
	// fail, but retries work.  So use a transport that does retries.
	proxy := &httputil.ReverseProxy{
		Director: director,
		Transport: RetryingRoundTripper{
			maxRetries: 10,

			initalTimeout: 50 * time.Millisecond,
		},
	}
	delay := time.Now().Sub(reqStartTime)
	if delay > 100*time.Millisecond {
		log.Printf("Request delay for %v: %v", serviceUrl, delay)
	}

	proxy.ServeHTTP(loggedWriter, request)
	totalElapsedTime := time.Since(reqStartTime)
	totalElapsedMS := totalElapsedTime / time.Millisecond
	metrics.totalLatency.WithLabelValues(strconv.Itoa(loggedWriter.status), request.Method).Observe(float64(totalElapsedMS))

	log.Printf("Status code returned by function: %v", loggedWriter.status)
	if loggedWriter.status != 200 {
		log.Printf("Incrementing function error counter")
		metrics.functionErrorCount.Inc()
	}

}
