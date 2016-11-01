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
	"github.com/platform9/fission"
	"k8s.io/client-go/1.4/kubernetes"
)

type (
	GenericPoolManager struct {
		pools            map[fission.Environment]*GenericPool
		kubernetesClient *kubernetes.Clientset
		namespace        string
		controllerUrl    string

		requestChannel chan *request
	}
	request struct {
		env             *fission.Environment
		responseChannel chan *response
	}
	response struct {
		error
		pool *GenericPool
	}
)

func MakeGenericPoolManager(controllerUrl string, kubernetesClient *kubernetes.Clientset, namespace string) *GenericPoolManager {
	gpm := &GenericPoolManager{
		pools:            make(map[fission.Environment]*GenericPool),
		kubernetesClient: kubernetesClient,
		namespace:        namespace,
		controllerUrl:    controllerUrl,
		requestChannel:   make(chan *request),
	}
	go gpm.service()
	return gpm
}

func (gpm *GenericPoolManager) service() {
	for {
		select {
		case req := <-gpm.requestChannel:
			pool, ok := gpm.pools[*req.env]
			if !ok {
				pool, err := MakeGenericPool(gpm.controllerUrl, gpm.kubernetesClient, req.env, 3, gpm.namespace)
				if err != nil {
					req.responseChannel <- &response{error: err}
					continue
				}
				gpm.pools[*req.env] = pool
				req.responseChannel <- &response{pool: pool}
			}
			req.responseChannel <- &response{pool: pool}
		}
	}
}

func (gpm *GenericPoolManager) GetPool(env *fission.Environment) (*GenericPool, error) {
	c := make(chan *response)
	gpm.requestChannel <- &request{env: env, responseChannel: c}
	resp := <-c
	return resp.pool, resp.error
}
