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

package executor

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

type (
	Executor struct {
		poolmgrUrl string
	}
)

func MakeExecutor(url string) *Executor {
	executor := &Executor{
		poolmgrUrl: url,
	}
	return executor
}

func (executor *Executor) getServiceForFunctionApi(w http.ResponseWriter, r *http.Request) {
	svcUrl, err := url.Parse(executor.poolmgrUrl + "/v2/getServiceForFunction")
	if err != nil {
		log.Printf("Failed to establish proxy server for PoolMgr: %v", err)
		http.Error(w, "Failed to read request", 500)
		return
	}
	director := func(req *http.Request) {
		req.URL.Scheme = svcUrl.Scheme
		req.URL.Host = svcUrl.Host
		req.URL.Path = svcUrl.Path
	}
	proxy := &httputil.ReverseProxy{
		Director: director,
	}
	proxy.ServeHTTP(w, r)
}

func (executor *Executor) tapService(w http.ResponseWriter, r *http.Request) {
	//http.Redirect(w, r, executor.poolmgrUrl+"/v2/tapService", 301)
	svcUrl, err := url.Parse(executor.poolmgrUrl + "/v2/tapService")
	if err != nil {
		log.Printf("Failed to establish proxy server for PoolMgr: %v", err)
		http.Error(w, "Failed to read request", 500)
		return
	}
	director := func(req *http.Request) {
		req.URL.Scheme = svcUrl.Scheme
		req.URL.Host = svcUrl.Host
		req.URL.Path = svcUrl.Path
	}
	proxy := &httputil.ReverseProxy{
		Director: director,
	}
	proxy.ServeHTTP(w, r)
}

func (executor *Executor) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionApi).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST")
	address := fmt.Sprintf(":%v", port)
	log.Printf("starting executor at port %v", port)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
