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
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

type functionHandler struct {
	fmap           *functionServiceMap
	poolManagerUrl string
	function
}

func (*functionHandler) getServiceForFunction() (*url.URL, error) {
	return nil, errors.New("not implemented")
}

func (fh *functionHandler) handler(responseWriter http.ResponseWriter, request *http.Request) {
	serviceUrl, err := fh.fmap.lookup(&fh.function)
	if err != nil {
		// Cache miss: request the Pool Manager to make a new service.
		serviceUrl, poolErr := fh.getServiceForFunction()
		if poolErr != nil {
			// now we're really screwed
			log.Printf("Failed to get service for function (%v,%v): %v",
				fh.function.name, fh.function.uid, poolErr)
			responseWriter.WriteHeader(500) // TODO: make this smarter based on the actual error
			return
		}

		// add it to the map
		fh.fmap.assign(&fh.function, serviceUrl)
	}

	// Proxy off our request to the serviceUrl, and send the response back.
	// TODO: As an optimization we may want to cache proxies too -- this would get us
	// connection reuse and possibly better performance
	director := func(req *http.Request) {
		// send this request to serviceurl
		req.URL.Scheme = serviceUrl.Scheme
		req.URL.Host = serviceUrl.Host
		req.URL.Path = serviceUrl.Path
		// leave the query string intact (req.URL.RawQuery)

		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}
	proxy := &httputil.ReverseProxy{Director: director}
	proxy.ServeHTTP(responseWriter, request)

	// TODO: handle failures and possibly retry here.
}
