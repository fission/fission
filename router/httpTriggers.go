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
	"github.com/gorilla/mux"
)

type HTTPTriggerSet struct {
	*functionServiceMap
	*mutableRouter
	controllerUrl  string
	poolManagerUrl string
	triggers       []HTTPTrigger
}

func makeHTTPTriggerSet(fmap *functionServiceMap, controllerUrl string, poolManagerUrl string) *HTTPTriggerSet {
	triggers := make([]HTTPTrigger, 1)
	return &HTTPTriggerSet{
		functionServiceMap: fmap,
		triggers:           triggers,
		controllerUrl:      controllerUrl,
		poolManagerUrl:     poolManagerUrl,
	}
}

func (triggers *HTTPTriggerSet) subscribeRouter(mr *mutableRouter) {
	triggers.mutableRouter = mr
	mr.updateRouter(triggers.getRouterFromTriggers())
	go triggers.watchTriggers()
}

func (triggers *HTTPTriggerSet) getRouterFromTriggers() *mux.Router {
	muxRouter := mux.NewRouter()
	for _, trigger := range triggers.triggers {
		fh := &functionHandler{
			fmap:           triggers.functionServiceMap,
			Function:       trigger.Function,
			poolManagerUrl: triggers.poolManagerUrl,
		}
		muxRouter.HandleFunc(trigger.UrlPattern, fh.handler)
	}
	return muxRouter
}

func (triggers *HTTPTriggerSet) watchTriggers() {
	// watch controller for updates to triggers and update the router accordingly
}
