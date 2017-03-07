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
	"time"

	"github.com/gorilla/mux"

	"github.com/fission/fission"
	controllerClient "github.com/fission/fission/controller/client"
	poolmgrClient "github.com/fission/fission/poolmgr/client"
)

type HTTPTriggerSet struct {
	*functionServiceMap
	*mutableRouter
	controller *controllerClient.Client
	poolmgr    *poolmgrClient.Client
	triggers   []fission.HTTPTrigger
	functions  []fission.Function
}

func makeHTTPTriggerSet(fmap *functionServiceMap, controller *controllerClient.Client, poolmgr *poolmgrClient.Client) *HTTPTriggerSet {
	triggers := make([]fission.HTTPTrigger, 1)
	return &HTTPTriggerSet{
		functionServiceMap: fmap,
		triggers:           triggers,
		controller:         controller,
		poolmgr:            poolmgr,
	}
}

func (ts *HTTPTriggerSet) subscribeRouter(mr *mutableRouter) {
	ts.mutableRouter = mr
	mr.updateRouter(ts.getRouter())
	go ts.watchTriggers()
}

func defaultHomeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (ts *HTTPTriggerSet) getRouter() *mux.Router {
	muxRouter := mux.NewRouter()

	// make a function name -> latest version map
	latestVersions := make(map[string]string)
	for _, f := range ts.functions {
		latestVersions[f.Metadata.Name] = f.Metadata.Uid
	}

	// HTTP triggers setup by the user
	homeHandled := false
	for _, trigger := range ts.triggers {
		m := trigger.Function
		if len(m.Uid) == 0 {
			// explicitly use the latest function version
			m.Uid = latestVersions[m.Name]
		}
		fh := &functionHandler{
			fmap:     ts.functionServiceMap,
			Function: m,
			poolmgr:  ts.poolmgr,
		}
		muxRouter.HandleFunc(trigger.UrlPattern, fh.handler).Methods(trigger.Method)
		if trigger.UrlPattern == "/" && trigger.Method == "GET" {
			homeHandled = true
		}
	}
	if !homeHandled {
		//
		// This adds a no-op handler that returns 200-OK to make sure that the
		// "GET /" request succeeds.  This route is used by GKE Ingress (and
		// perhaps other ingress implementations) as a health check, so we don't
		// want it to be a 404 even if the user doesn't have a function mapped to
		// this route.
		//
		muxRouter.HandleFunc("/", defaultHomeHandler).Methods("GET")
	}

	// Internal triggers for (the latest version of) each function
	for _, function := range ts.functions {
		m := fission.Metadata{Name: function.Metadata.Name}
		fh := &functionHandler{
			fmap:     ts.functionServiceMap,
			Function: function.Metadata,
			poolmgr:  ts.poolmgr,
		}
		muxRouter.HandleFunc(fission.UrlForFunction(&m), fh.handler)
	}

	return muxRouter
}

func (ts *HTTPTriggerSet) watchTriggers() {
	if ts.controller == nil {
		return
	}

	// the number of connection failures we'll accept before quitting
	maxFailures := 5

	// amount of time to sleep between polling calls
	pollSleepDuration := 3 * time.Second

	// Watch controller for updates to triggers and update the router accordingly.
	// TODO change this to use a watch API; or maybe even watch etcd directly.
	failureCount := 0
	for {
		triggers, err := ts.controller.HTTPTriggerList()
		if err != nil {
			failureCount += 1
			if failureCount >= maxFailures {
				log.Fatalf("Failed to connect to controller after %v retries: %v", failureCount, err)
			}
			time.Sleep(pollSleepDuration)
			continue
		}
		ts.triggers = triggers

		functions, err := ts.controller.FunctionList()
		if err != nil {
			log.Fatalf("Failed to get function list")
		}
		ts.functions = functions

		ts.mutableRouter.updateRouter(ts.getRouter())
		time.Sleep(pollSleepDuration)
	}
}
