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
	"os"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/cache"
	"github.com/fission/fission/executor/fcache"
	"github.com/fission/fission/executor/newdeploy"
	"github.com/fission/fission/executor/poolmgr"
	"github.com/fission/fission/tpr"
)

type (
	Executor struct {
		gpm           *poolmgr.GenericPoolManager
		ndm           *newdeploy.NewDeploy
		functionEnv   *cache.Cache
		fissionClient *tpr.FissionClient
		fsCache       *fcache.FunctionServiceCache

		requestChan chan *createFuncServiceRequest
		fsCreateWg  map[string]*sync.WaitGroup
	}
	createFuncServiceRequest struct {
		funcMeta *metav1.ObjectMeta
		respChan chan *createFuncServiceResponse
	}

	createFuncServiceResponse struct {
		address string
		err     error
	}
)

func MakeExecutor(gpm *poolmgr.GenericPoolManager, ndm *newdeploy.NewDeploy, fissionClient *tpr.FissionClient, fsCache *fcache.FunctionServiceCache) *Executor {
	executor := &Executor{
		gpm:           gpm,
		ndm:           ndm,
		functionEnv:   cache.MakeCache(10*time.Second, 0),
		fissionClient: fissionClient,
		fsCache:       fsCache,

		requestChan: make(chan *createFuncServiceRequest),
		fsCreateWg:  make(map[string]*sync.WaitGroup),
	}
	go executor.serveCreateFuncServices()
	return executor
}

// All non-cached function service requests go through this goroutine
// serially. It parallelizes requests for different functions, and
// ensures that for a given function, only one request causes a pod to
// get specialized. In other words, it ensures that when there's an
// ongoing request for a certain function, all other requests wait for
// that request to complete.
func (executor *Executor) serveCreateFuncServices() {
	for {
		req := <-executor.requestChan
		m := req.funcMeta

		// Cache miss -- is this first one to request the func?
		wg, found := executor.fsCreateWg[tpr.CacheKey(m)]
		if !found {
			// create a waitgroup for other requests for
			// the same function to wait on
			wg := &sync.WaitGroup{}
			wg.Add(1)
			executor.fsCreateWg[tpr.CacheKey(m)] = wg

			// launch a goroutine for each request, to parallelize
			// the specialization of different functions
			go func() {
				address, err := executor.createServiceForFunction(m)
				req.respChan <- &createFuncServiceResponse{
					address: address,
					err:     err,
				}
				delete(executor.fsCreateWg, tpr.CacheKey(m))
				wg.Done()
			}()
		} else {
			// There's an existing request for this function, wait for it to finish
			go func() {
				log.Printf("Waiting for concurrent request for the same function: %v", m)
				wg.Wait()

				// get the function service from the cache
				fsvc, err := executor.fsCache.GetByFunction(m)
				address := ""
				if err == nil {
					address = fsvc.Address
				}
				req.respChan <- &createFuncServiceResponse{
					address: address,
					err:     err,
				}
			}()
		}
	}
}

func (executor *Executor) createServiceForFunction(meta *metav1.ObjectMeta) (string, error) {
	log.Printf("[%v] No cached function service found, creating one", meta.Name)

	// from Func -> get Env
	log.Printf("[%v] getting environment for function", meta.Name)
	env, err := executor.getFunctionEnv(meta)
	if err != nil {
		return "", err
	}
	// Appropriate backend handles the service creation
	backend := os.Getenv("EXECUTOR_BACKEND")

	switch backend {
	case "NEWDEPLOY":
		fs, err := executor.ndm.GetFuncSvc(meta, env)
		if err != nil {
			return "", err
		}
		return fs.Address, nil

	default:
		pool, err := executor.gpm.GetPool(env)
		if err != nil {
			return "", err
		}
		// from GenericPool -> get one function container
		// (this also adds to the cache)
		log.Printf("[%v] getting function service from pool", meta.Name)
		fsvc, err := pool.GetFuncSvc(meta)
		if err != nil {
			return "", err
		}
		fmt.Println("Returning address of service:", fsvc.Address)
		return fsvc.Address, nil
	}
}

func (executor *Executor) getFunctionEnv(m *metav1.ObjectMeta) (*tpr.Environment, error) {
	var env *tpr.Environment

	// Cached ?
	result, err := executor.functionEnv.Get(tpr.CacheKey(m))
	if err == nil {
		env = result.(*tpr.Environment)
		return env, nil
	}

	// Cache miss -- get func from controller
	f, err := executor.fissionClient.Functions(m.Namespace).Get(m.Name)
	if err != nil {
		return nil, err
	}

	// Get env from metadata
	log.Printf("[%v] getting env", m)
	env, err = executor.fissionClient.Environments(f.Spec.Environment.Namespace).Get(f.Spec.Environment.Name)
	if err != nil {
		return nil, err
	}

	// cache for future lookups
	executor.functionEnv.Set(tpr.CacheKey(m), env)

	return env, nil
}

// StartExecutor Starts executor and the backend components that executor uses such as Poolmgr,
// deploymgr and potential future backends
func StartExecutor(fissionNamespace string, functionNamespace string, port int) error {
	fissionClient, kubernetesClient, err := tpr.MakeFissionClient()
	if err != nil {
		log.Printf("Failed to get kubernetes client: %v", err)
		return err
	}
	fsCache := fcache.MakeFunctionServiceCache()

	poolID := uniuri.NewLen(8)
	poolmgr.CleanupOldPoolmgrResources(kubernetesClient, functionNamespace, poolID)
	gpm := poolmgr.MakeGenericPoolManager(
		fissionClient, kubernetesClient, fissionNamespace,
		functionNamespace, fsCache, poolID)

	ndm := newdeploy.MakeNewDeploy(
		fissionClient, kubernetesClient,
		functionNamespace, fsCache)

	api := MakeExecutor(gpm, ndm, fissionClient, fsCache)
	go api.Serve(port)

	return nil
}
