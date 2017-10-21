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

package poolmgr

import (
	"sync"
	"time"

	"github.com/dchest/uniuri"
	"github.com/fission/fission/cache"
)

type (
	Poolmgr struct {
		gpm           *GenericPoolManager
		functionEnv   *cache.Cache // map[string]tpr.Environment
		fsCache       *functionServiceCache
		fissionClient *tpr.FissionClient

		fsCreateChannels map[string]*sync.WaitGroup // xxx no channels here, rename this
		requestChan      chan *createFuncServiceRequest
	}
)

func MakePoolmgr(gpm *GenericPoolManager, fissionClient *tpr.FissionClient, fissionNs string, fsCache *functionServiceCache) *Poolmgr {
	poolMgr := &Poolmgr{
		gpm:              gpm,
		functionEnv:      cache.MakeCache(10*time.Second, 0),
		fsCache:          fsCache,
		fissionClient:    fissionClient,
		fsCreateChannels: make(map[string]*sync.WaitGroup),
		requestChan:      make(chan *createFuncServiceRequest),
	}
	go poolMgr.serveCreateFuncServices()
	return poolMgr
}

func StartPoolmgr(fissionNamespace string, functionNamespace string, port int) error {
	//TBD To be removed

	return nil
}
