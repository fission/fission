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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
)

func (executor *Executor) getServiceForFunctionApi(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	// get function metadata
	m := metav1.ObjectMeta{}
	err = json.Unmarshal(body, &m)
	if err != nil {
		http.Error(w, "Failed to parse request", 400)
		return
	}

	serviceName, err := executor.getServiceForFunction(&m)
	if err != nil {
		code, msg := fission.GetHTTPError(err)
		log.Printf("Error: %v: %v", code, msg)
		http.Error(w, msg, code)
		return
	}

	w.Write([]byte(serviceName))
}

func (executor *Executor) getServiceForFunction(m *metav1.ObjectMeta) (string, error) {
	// Check function -> svc cache
	log.Printf("[%v] Checking for cached function service", m.Name)
	fsvc, err := executor.fsCache.GetByFunction(m)
	if err == nil {
		// Cached, return svc address
		return fsvc.Address, nil
	}

	respChan := make(chan *createFuncServiceResponse)
	executor.requestChan <- &createFuncServiceRequest{
		funcMeta: m,
		respChan: respChan,
	}
	resp := <-respChan
	return resp.address, resp.err
}

// find funcSvc and update its atime
func (executor *Executor) tapService(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}
	svcName := string(body)
	svcHost := strings.TrimPrefix(svcName, "http://")

	err = executor.fsCache.TouchByAddress(svcHost)
	if err != nil {
		log.Printf("funcSvc tap error: %v", err)
		http.Error(w, "Not found", 404)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (executor *Executor) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v2/getServiceForFunction", executor.getServiceForFunctionApi).Methods("POST")
	r.HandleFunc("/v2/tapService", executor.tapService).Methods("POST")
	address := fmt.Sprintf(":%v", port)
	log.Printf("starting executor at port %v", port)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
