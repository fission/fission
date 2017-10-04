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
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func createBackendService(testResponseString string) *url.URL {
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testResponseString))
	}))

	backendURL, err := url.Parse(backendServer.URL)
	if err != nil {
		panic("error parsing url")
	}
	return backendURL
}

/*
   1. Create a service at some URL
   2. Add it to the function service map
   3. Create a http server with some trigger url pointed at function handler
   4. Send a request to that server, ensure it reaches the first service.
*/
func TestFunctionProxying(t *testing.T) {
	testResponseString := "hi"
	backendURL := createBackendService(testResponseString)
	log.Printf("Created backend svc at %v", backendURL)

	fn := &metav1.ObjectMeta{Name: "foo", Namespace: metav1.NamespaceDefault}

	fmap := makeFunctionServiceMap(0)
	fmap.assign(fn, backendURL)

	fh := &functionHandler{fmap: fmap, function: fn}
	functionHandlerServer := httptest.NewServer(http.HandlerFunc(fh.handler))
	fhURL := functionHandlerServer.URL

	testRequest(fhURL, testResponseString)
}
