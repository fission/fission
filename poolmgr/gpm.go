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
	"time"

	"k8s.io/client-go/1.5/kubernetes"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
)

type requestType int

const (
	GET_POOL requestType = iota
	CLEANUP_POOLS
)

type (
	GenericPoolManager struct {
		pools            map[fission.Environment]*GenericPool
		kubernetesClient *kubernetes.Clientset
		namespace        string
		controllerUrl    string
		controllerClient *client.Client
		fsCache          *functionServiceCache
		instanceId       string
		requestChannel   chan *request
	}
	request struct {
		requestType
		env             *fission.Environment
		envList         []fission.Environment
		responseChannel chan *response
	}
	response struct {
		error
		pool *GenericPool
	}
)

func MakeGenericPoolManager(
	controllerUrl string,
	kubernetesClient *kubernetes.Clientset,
	namespace string,
	fsCache *functionServiceCache,
	instanceId string) *GenericPoolManager {

	gpm := &GenericPoolManager{
		pools:            make(map[fission.Environment]*GenericPool),
		kubernetesClient: kubernetesClient,
		namespace:        namespace,
		controllerUrl:    controllerUrl,
		controllerClient: client.MakeClient(controllerUrl),
		fsCache:          fsCache,
		instanceId:       instanceId,
		requestChannel:   make(chan *request),
	}
	go gpm.service()
	go gpm.eagerPoolCreator()

	return gpm
}

func (gpm *GenericPoolManager) service() {
	for {
		req := <-gpm.requestChannel
		switch req.requestType {
		case GET_POOL:
			var err error
			pool, ok := gpm.pools[*req.env]
			if !ok {
				pool, err = MakeGenericPool(
					gpm.controllerUrl, gpm.kubernetesClient, req.env,
					3, // TODO configurable/autoscalable
					gpm.namespace, gpm.fsCache, gpm.instanceId)
				if err != nil {
					req.responseChannel <- &response{error: err}
					continue
				}
				gpm.pools[*req.env] = pool
			}
			req.responseChannel <- &response{pool: pool}
		case CLEANUP_POOLS:
			uids := make(map[string]bool)
			for _, env := range req.envList {
				uids[env.Metadata.Uid] = true
			}
			for env, pool := range gpm.pools {
				_, ok := uids[env.Metadata.Uid]
				if !ok {
					// Env no longer exists -- remove our cache
					log.Printf("Destroying generic pool for environment [%v]", env)
					delete(gpm.pools, env)

					// and delete the pool asynchronously.
					go pool.destroy()
				}
			}
			// no response, caller doesn't wait
		}
	}
}

func (gpm *GenericPoolManager) GetPool(env *fission.Environment) (*GenericPool, error) {
	c := make(chan *response)
	gpm.requestChannel <- &request{
		requestType:     GET_POOL,
		env:             env,
		responseChannel: c,
	}
	resp := <-c
	return resp.pool, resp.error
}

func (gpm *GenericPoolManager) CleanupPools(envs []fission.Environment) {
	gpm.requestChannel <- &request{
		requestType: CLEANUP_POOLS,
		envList:     envs,
	}
}

func (gpm *GenericPoolManager) eagerPoolCreator() {
	failureCount := 0
	maxFailures := 5
	pollSleep := time.Duration(2 * time.Second)
	for {
		time.Sleep(pollSleep)

		// get list of envs from controller
		envs, err := gpm.controllerClient.EnvironmentList()
		if err != nil {
			failureCount++
			if failureCount >= maxFailures {
				log.Fatalf("Failed to connect to controller %v times: %v", maxFailures, err)
			}
		}

		// Create pools for all envs.  TODO: we should make this a bit less eager, only
		// creating pools for envs that are actually used by functions.  Also we might want
		// to keep these eagerly created pools smaller than the ones created when there are
		// actual function calls.
		for _, env := range envs {
			_, err := gpm.GetPool(&env)
			if err != nil {
				log.Printf("eager-create pool failed: %v", err)
			}
		}

		// Clean up pools whose env was deleted
		gpm.CleanupPools(envs)
	}
}
