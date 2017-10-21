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
	"log"
	"sync"

	"github.com/dchest/uniuri"
	"github.com/fission/fission/cache"
	"github.com/fission/fission/tpr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type (
	Executor struct {
		functionEnv   *cache.Cache
		fissionClient *tpr.FissionClient
		fsCache       *functionServiceCache

		requestChan chan *createFuncServiceRequest
		fsCreateWg  map[string]*sync.WaitGroup // xxx no channels here, rename this
	}
)

func MakeExecutor(pm *Poolmgr) *Executor {
	executor := &Executor{}
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
					address = fsvc.address
				}
				req.respChan <- &createFuncServiceResponse{
					address: address,
					err:     err,
				}
			}()
		}
	}
}

func (executor *Executor) createServiceForFunction(m *metav1.ObjectMeta) (string, error) {
	return "nil", nil
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

	instanceID := uniuri.NewLen(8)
	cleanupOldPoolmgrResources(kubernetesClient, functionNamespace, instanceID)

	fsCache := MakeFunctionServiceCache()
	gpm := MakeGenericPoolManager(
		fissionClient, kubernetesClient, fissionNamespace,
		functionNamespace, fsCache, instanceID)

	pm := MakePoolmgr(gpm, fissionClient, fissionNamespace, fsCache)
	api := MakeExecutor(pm)
	go api.Serve(port)

	return nil
}
