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
<<<<<<< HEAD
	"fmt"
=======
>>>>>>> executor_abstraction
	"log"
	"os"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/cache"
	"github.com/fission/fission/crd"
<<<<<<< HEAD
	"github.com/fission/fission/executor/fcache"
	"github.com/fission/fission/executor/newdeploy"
=======
	"github.com/fission/fission/executor/fscache"
>>>>>>> executor_abstraction
	"github.com/fission/fission/executor/poolmgr"
)

type (
	Executor struct {
		gpm           *poolmgr.GenericPoolManager
<<<<<<< HEAD
		ndm           *newdeploy.NewDeploy
		functionEnv   *cache.Cache
		fissionClient *crd.FissionClient
		fsCache       *fcache.FunctionServiceCache
=======
		functionEnv   *cache.Cache
		fissionClient *crd.FissionClient
		fsCache       *fscache.FunctionServiceCache
>>>>>>> executor_abstraction

		requestChan chan *createFuncServiceRequest
		fsCreateWg  map[string]*sync.WaitGroup
	}
	createFuncServiceRequest struct {
		funcMeta *metav1.ObjectMeta
		respChan chan *createFuncServiceResponse
	}

	createFuncServiceResponse struct {
<<<<<<< HEAD
		address string
=======
		funcSvc *fscache.FuncSvc
>>>>>>> executor_abstraction
		err     error
	}
)

<<<<<<< HEAD
func MakeExecutor(gpm *poolmgr.GenericPoolManager, ndm *newdeploy.NewDeploy, fissionClient *crd.FissionClient, fsCache *fcache.FunctionServiceCache) *Executor {
	executor := &Executor{
		gpm:           gpm,
		ndm:           ndm,
=======
func MakeExecutor(gpm *poolmgr.GenericPoolManager, fissionClient *crd.FissionClient, fsCache *fscache.FunctionServiceCache) *Executor {
	executor := &Executor{
		gpm:           gpm,
>>>>>>> executor_abstraction
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
		wg, found := executor.fsCreateWg[crd.CacheKey(m)]
		if !found {
			// create a waitgroup for other requests for
			// the same function to wait on
			wg := &sync.WaitGroup{}
			wg.Add(1)
			executor.fsCreateWg[crd.CacheKey(m)] = wg

			// launch a goroutine for each request, to parallelize
			// the specialization of different functions
			go func() {
<<<<<<< HEAD
				address, err := executor.createServiceForFunction(m)
				req.respChan <- &createFuncServiceResponse{
					address: address,
=======
				fsvc, err := executor.createServiceForFunction(m)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
>>>>>>> executor_abstraction
					err:     err,
				}
				delete(executor.fsCreateWg, crd.CacheKey(m))
				wg.Done()
			}()
		} else {
			// There's an existing request for this function, wait for it to finish
			go func() {
				log.Printf("Waiting for concurrent request for the same function: %v", m)
				wg.Wait()

				// get the function service from the cache
				fsvc, err := executor.fsCache.GetByFunction(m)
<<<<<<< HEAD
				address := ""
				if err == nil {
					address = fsvc.Address
				}
				req.respChan <- &createFuncServiceResponse{
					address: address,
=======
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
>>>>>>> executor_abstraction
					err:     err,
				}
			}()
		}
	}
}

<<<<<<< HEAD
func (executor *Executor) createServiceForFunction(meta *metav1.ObjectMeta) (string, error) {
	log.Printf("[%v] No cached function service found, creating one", meta.Name)

	// from Func -> get Env
	log.Printf("[%v] getting environment for function", meta.Name)
	env, err := executor.getFunctionEnv(meta)
	if err != nil {
		return "", err
	}
	// Appropriate backend handles the service creation
	backend, err := executor.chooseBackend(meta)

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

func (executor *Executor) chooseBackend(meta *metav1.ObjectMeta) (string, error) {
	return os.Getenv("EXECUTOR_BACKEND"), nil
}

=======
func (executor *Executor) createServiceForFunction(m *metav1.ObjectMeta) (*fscache.FuncSvc, error) {
	log.Printf("[%v] No cached function service found, creating one", m.Name)

	env, err := executor.getFunctionEnv(m)
	if err != nil {
		return nil, err
	}
	// Appropriate backend handles the service creation
	backend := os.Getenv("EXECUTOR_BACKEND")
	switch backend {
	case "DEPLOY":
		return nil, nil
	default:
		pool, err := executor.gpm.GetPool(env)
		if err != nil {
			return nil, err
		}
		// from GenericPool -> get one function container
		// (this also adds to the cache)
		log.Printf("[%v] getting function service from pool", m.Name)
		fsvc, err := pool.GetFuncSvc(m)
		if err != nil {
			return nil, err
		}
		return fsvc, nil
	}
}

>>>>>>> executor_abstraction
func (executor *Executor) getFunctionEnv(m *metav1.ObjectMeta) (*crd.Environment, error) {
	var env *crd.Environment

	// Cached ?
	result, err := executor.functionEnv.Get(crd.CacheKey(m))
	if err == nil {
		env = result.(*crd.Environment)
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
	executor.functionEnv.Set(crd.CacheKey(m), env)

	return env, nil
}

// StartExecutor Starts executor and the backend components that executor uses such as Poolmgr,
// deploymgr and potential future backends
func StartExecutor(fissionNamespace string, functionNamespace string, port int) error {
	fissionClient, kubernetesClient, _, err := crd.MakeFissionClient()
	if err != nil {
		log.Printf("Failed to get kubernetes client: %v", err)
		return err
	}
<<<<<<< HEAD
	fsCache := fcache.MakeFunctionServiceCache()

	poolID := uniuri.NewLen(8)
	poolmgr.CleanupOldPoolmgrResources(kubernetesClient, functionNamespace, poolID)
	gpm := poolmgr.MakeGenericPoolManager(
		fissionClient, kubernetesClient, fissionNamespace,
		functionNamespace, fsCache, poolID)

	ndm := newdeploy.MakeNewDeploy(
		fissionClient, kubernetesClient,
		functionNamespace)

	api := MakeExecutor(gpm, ndm, fissionClient, fsCache)
=======

	instanceID := uniuri.NewLen(8)
	poolmgr.CleanupOldPoolmgrResources(kubernetesClient, functionNamespace, instanceID)

	fsCache := fscache.MakeFunctionServiceCache()
	gpm := poolmgr.MakeGenericPoolManager(
		fissionClient, kubernetesClient, fissionNamespace,
		functionNamespace, fsCache, instanceID)

	api := MakeExecutor(gpm, fissionClient, fsCache)
>>>>>>> executor_abstraction
	go api.Serve(port)

	return nil
}
