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
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"github.com/platform9/fission"
	controllerClient "github.com/platform9/fission/controller/client"
	poolmgrClient "github.com/platform9/fission/poolmgr/client"
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

func (ts *HTTPTriggerSet) getRouter() *mux.Router {
	muxRouter := mux.NewRouter()

	// HTTP triggers setup by the user
	for _, trigger := range ts.triggers {
		fh := &functionHandler{
			fmap:     ts.functionServiceMap,
			Function: trigger.Function,
			poolmgr:  ts.poolmgr,
		}
		muxRouter.HandleFunc(trigger.UrlPattern, fh.handler)
	}

	// Internal triggers for each function
	for _, function := range ts.functions {
		fh := &functionHandler{
			fmap:     ts.functionServiceMap,
			Function: function.Metadata,
			poolmgr:  ts.poolmgr,
		}
		muxRouter.HandleFunc(fission.UrlForFunction(&function.Metadata),
			fh.handler)
	}

	return muxRouter
}

func (ts *HTTPTriggerSet) watchTriggers() {
	if ts.controller == nil {
		return
	}

	// the number of connection failures we'll accept before quitting
	var err error
	maxFailures := 5
	maxFailuresEnv := os.Getenv("FISSION_ROUTER_MAX_FAILURES")
	if len(maxFailuresEnv) != 0 {
		maxFailures, err = strconv.Atoi(maxFailuresEnv)
		if err != nil {
			log.Fatalf("FISSION_ROUTER_MAX_FAILURES must be an integer, found %v", maxFailuresEnv)
		}
	}

	// amount of time to sleep between polling calls
	pollSleepSec := 3
	pollSleepEnv := os.Getenv("FISSION_ROUTER_POLL_SLEEP_SECONDS")
	if len(pollSleepEnv) != 0 {
		pollSleepSec, err = strconv.Atoi(pollSleepEnv)
		if err != nil {
			log.Fatalf("FISSION_ROUTER_POLL_SLEEP_SECONDS must be an integer, found %v", pollSleepEnv)
		}
	}

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
			time.Sleep(time.Duration(pollSleepSec) * time.Second)
			continue
		}
		ts.triggers = triggers

		functions, err := ts.controller.FunctionList()
		if err != nil {
			log.Fatalf("Failed to get function list")
		}
		ts.functions = functions

		ts.mutableRouter.updateRouter(ts.getRouter())
		time.Sleep(time.Duration(pollSleepSec) * time.Second)
	}
}
