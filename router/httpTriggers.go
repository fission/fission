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
	mr.updateRouter(ts.getRouterFromTriggers())
	go ts.watchTriggers()
}

func (ts *HTTPTriggerSet) getRouterFromTriggers() *mux.Router {
	muxRouter := mux.NewRouter()
	for _, trigger := range ts.triggers {
		fh := &functionHandler{
			fmap:     ts.functionServiceMap,
			Function: trigger.Function,
			poolmgr:  ts.poolmgr,
		}
		muxRouter.HandleFunc(trigger.UrlPattern, fh.handler)
	}
	return muxRouter
}

func (ts *HTTPTriggerSet) watchTriggers() {
	if ts.controller == nil {
		return
	}

	failureCount := 0
	maxFailures := 5

	// Watch controller for updates to triggers and update the router accordingly.
	// TODO change this to use a watch API; or maybe even watch etcd directly.
	for {
		triggers, err := ts.controller.HTTPTriggerList()
		if err != nil {
			failureCount += 1
			if failureCount >= maxFailures {
				log.Fatalf("Failed to connect to controller after %v retries: %v", failureCount, err)
			}
		}
		log.Printf("Updating router, %v triggers", len(triggers))

		ts.triggers = triggers
		ts.mutableRouter.updateRouter(ts.getRouterFromTriggers())

		time.Sleep(3 * time.Second)
	}
}
