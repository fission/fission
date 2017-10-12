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
)

type functionHandler struct {
	fmap     *functionServiceMap
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
	fh.executor.TapService(serviceUrl.String())
}

func (fh *functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	reqStartTime := time.Now()

	// retrieve url params and add them to request header
	vars := mux.Vars(request)
	for k, v := range vars {
		request.Header.Add(fmt.Sprintf("X-Fission-Params-%v", k), v)
	}

	// System Params
	MetadataToHeaders(HEADERS_FISSION_FUNCTION_PREFIX, fh.function, request)

	// cache lookup
	serviceUrl, err := fh.fmap.lookup(fh.function)
	if err != nil {
		// Cache miss: request the Pool Manager to make a new service.
		log.Printf("Not cached, getting new service for %v", fh.function)

		var poolErr error
		serviceUrl, poolErr = fh.getServiceForFunction()
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
		// poolmgr we're using this service
		go fh.tapService(serviceUrl)
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
			maxRetries:    10,
			initalTimeout: 50 * time.Millisecond,
		},
	}
	delay := time.Now().Sub(reqStartTime)
	if delay > 100*time.Millisecond {
		log.Printf("Request delay for %v: %v", serviceUrl, delay)
	}
	proxy.ServeHTTP(responseWriter, request)
}
