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

	"github.com/fission/fission"
	poolmgrClient "github.com/fission/fission/poolmgr/client"
)

type functionHandler struct {
	fmap     *functionServiceMap
	poolmgr  *poolmgrClient.Client
	Function fission.Metadata
}

func (fh *functionHandler) getServiceForFunction() (*url.URL, error) {
	// call poolmgr, get a url for a function
	svcName, err := fh.poolmgr.GetServiceForFunction(&fh.Function)
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
	if fh.poolmgr == nil {
		return
	}
	err := fh.poolmgr.TapService(serviceUrl)
	if err != nil {
		log.Printf("tap service error for %v: %v", serviceUrl.String(), err)
	}
}

func (fh *functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	reqStartTime := time.Now()

	// cache lookup
	serviceUrl, err := fh.fmap.lookup(&fh.Function)
	if err != nil {
		// Cache miss: request the Pool Manager to make a new service.
		log.Printf("Not cached, getting new service for %v", fh.Function)

		var poolErr error
		serviceUrl, poolErr = fh.getServiceForFunction()
		if poolErr != nil {
			log.Printf("Failed to get service for function (%v,%v): %v",
				fh.Function.Name, fh.Function.Uid, poolErr)
			// We might want a specific error code or header for fission
			// failures as opposed to user function bugs.
			http.Error(responseWriter, "Internal server error (fission)", 500)
			return
		}

		// add it to the map
		fh.fmap.assign(&fh.Function, serviceUrl)
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
