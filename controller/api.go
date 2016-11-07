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

package controller

import (
	"fmt"
	"net/http"
	"os"
	"runtime/debug"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"

	"github.com/platform9/fission"
)

type API struct {
	FunctionStore
	HTTPTriggerStore
	EnvironmentStore
}

func (api *API) respondWithSuccess(w http.ResponseWriter, resp []byte) {
	_, err := w.Write(resp)
	if err != nil {
		// this will probably fail too, but try anyway
		api.respondWithError(w, err)
	}
}

func (api *API) respondWithError(w http.ResponseWriter, err error) {
	debug.PrintStack()
	code, msg := fission.GetHTTPError(err)
	log.Errorf("Error: %v: %v", code, msg)
	http.Error(w, msg, code)
}

func (api *API) HomeHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "{message: \"Fission API\", version: \"0.1.0\"}\n")
}

func (api *API) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/", api.HomeHandler)

	r.HandleFunc("/v1/functions", api.FunctionApiList).Methods("GET")
	r.HandleFunc("/v1/functions", api.FunctionApiCreate).Methods("POST")
	r.HandleFunc("/v1/functions/{function}", api.FunctionApiGet).Methods("GET")
	r.HandleFunc("/v1/functions/{function}", api.FunctionApiUpdate).Methods("PUT")
	r.HandleFunc("/v1/functions/{function}", api.FunctionApiDelete).Methods("DELETE")

	r.HandleFunc("/v1/triggers/http", api.HTTPTriggerApiList).Methods("GET")
	r.HandleFunc("/v1/triggers/http", api.HTTPTriggerApiCreate).Methods("POST")
	r.HandleFunc("/v1/triggers/http/{httpTrigger}", api.HTTPTriggerApiGet).Methods("GET")
	r.HandleFunc("/v1/triggers/http/{httpTrigger}", api.HTTPTriggerApiUpdate).Methods("PUT")
	r.HandleFunc("/v1/triggers/http/{httpTrigger}", api.HTTPTriggerApiDelete).Methods("DELETE")

	r.HandleFunc("/v1/environments", api.EnvironmentApiList).Methods("GET")
	r.HandleFunc("/v1/environments", api.EnvironmentApiCreate).Methods("POST")
	r.HandleFunc("/v1/environments/{environment}", api.EnvironmentApiGet).Methods("GET")
	r.HandleFunc("/v1/environments/{environment}", api.EnvironmentApiUpdate).Methods("PUT")
	r.HandleFunc("/v1/environments/{environment}", api.EnvironmentApiDelete).Methods("DELETE")

	address := fmt.Sprintf(":%v", port)

	log.WithFields(log.Fields{"port": port}).Info("Server started")
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
