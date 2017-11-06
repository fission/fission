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

package fcache

import (
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
	"github.com/fission/fission/crd"
)

type fscRequestType int
type backendType int

const (
	TOUCH fscRequestType = iota
	LISTOLD
	LOG
	DELETE_BY_OBJECT
)

const (
	POOLMGR backendType = iota
	NEWDEPLOY
)

type (
	funcSvc struct {
		function         *metav1.ObjectMeta  // function this pod/service is for
		environment      *crd.Environment    // function's environment
		address          string              // Host:Port or IP:Port that the function's service can be reached at.
		kubernetesObject api.ObjectReference // Kubernetes Object (within the function namespace)
		backend          backendType

		Ctime time.Time
		Atime time.Time
	}

	functionServiceCache struct {
		byFunction   *cache.Cache // function-key -> funcSvc  : map[string]*funcSvc
		byAddress    *cache.Cache // address      -> function : map[string]metav1.ObjectMeta
		byKubeObject *cache.Cache // obj          -> function : map[api.ObjectReference]metav1.ObjectMeta

		requestChannel chan *fscRequest
	}
	fscRequest struct {
		requestType      fscRequestType
		address          string
		kubernetesObject api.ObjectReference
		age              time.Duration
		env              *metav1.ObjectMeta // used for ListOld
		responseChannel  chan *fscResponse
	}
	fscResponse struct {
		objects []api.ObjectReference
		deleted bool
		error
	}
)

func MakeFunctionServiceCache() *FunctionServiceCache {
	fsc := &FunctionServiceCache{
		byFunction:     cache.MakeCache(0, 0),
		byAddress:      cache.MakeCache(0, 0),
		byKubeObject:   cache.MakeCache(0, 0),
		requestChannel: make(chan *fscRequest),
	}
	go fsc.service()
	return fsc
}

func (fsc *FunctionServiceCache) service() {
	for {
		req := <-fsc.requestChannel
		resp := &fscResponse{}
		switch req.requestType {
		case TOUCH:
			// update atime for this function svc
			resp.error = fsc._touchByAddress(req.address)
		case LISTOLD:
			// get svcs idle for > req.age
			byKubeObjectCopy := fsc.byKubeObject.Copy()
			kubeObjects := make([]api.ObjectReference, 0)
			for objI, mI := range byKubeObjectCopy {
				m := mI.(metav1.ObjectMeta)
				fsvcI, err := fsc.byFunction.Get(crd.CacheKey(&m))
				if err != nil {
					resp.error = err
				} else {
					fsvc := fsvcI.(*FuncSvc)
					if fsvc.Environment.Metadata.UID == req.env.UID &&
						time.Now().Sub(fsvc.Atime) > req.age {

						obj := objI.(api.ObjectReference)
						kubeObjects = append(kubeObjects, obj)
					}
				}
			}
			resp.objects = kubeObjects
		case LOG:
			funcCopy := fsc.byFunction.Copy()
			log.Printf("Cache has %v entries", len(funcCopy))
			for key, fsvcI := range funcCopy {
				fsvc := fsvcI.(*funcSvc)
				log.Printf("%v\t%v\t%v", key, fsvc.kubernetesObject.Kind, fsvc.kubernetesObject.Name)
			}
		case DELETE_BY_OBJECT:
			resp.deleted, resp.error = fsc._deleteByKubeObject(req.kubernetesObject, req.age)
		}
		req.responseChannel <- resp
	}
}

func (fsc *FunctionServiceCache) GetByFunction(m *metav1.ObjectMeta) (*FuncSvc, error) {
	key := crd.CacheKey(m)

	fsvcI, err := fsc.byFunction.Get(key)
	if err != nil {
		return nil, err
	}

	// update atime
	fsvc := fsvcI.(*FuncSvc)
	fsvc.Atime = time.Now()

	fsvcCopy := *fsvc
	return &fsvcCopy, nil
}

// TODO: error should be second return
func (fsc *FunctionServiceCache) Add(fsvc FuncSvc) (error, *FuncSvc) {
	err, existing := fsc.byFunction.Set(crd.CacheKey(fsvc.Function), &fsvc)
	if err != nil {
		if existing != nil {
			f := existing.(*FuncSvc)
			err2 := fsc.TouchByAddress(f.Address)
			if err2 != nil {
				return err2, nil
			}
			fCopy := *f
			return err, &fCopy
		}
		return err, nil
	}
	now := time.Now()
	fsvc.Ctime = now
	fsvc.Atime = now

	// Add to byAddress and byKubernetesObject caches. Ignore NameExists errors
	// because of multiple-specialization. See issue #331.
	err, _ = fsc.byAddress.Set(fsvc.Address, *fsvc.Function)
	if err != nil {
		if fe, ok := err.(fission.Error); ok {
			if fe.Code == fission.ErrorNameExists {
				err = nil
			}
		}
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	err, _ = fsc.byKubeObject.Set(fsvc.kubernetesObject, *fsvc.function)
	if err != nil {
		if fe, ok := err.(fission.Error); ok {
			if fe.Code == fission.ErrorNameExists {
				err = nil
			}
		}
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	return nil, nil
}

func (fsc *FunctionServiceCache) TouchByAddress(address string) error {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     TOUCH,
		address:         address,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.error
}

func (fsc *FunctionServiceCache) _touchByAddress(address string) error {
	mI, err := fsc.byAddress.Get(address)
	if err != nil {
		return err
	}
	m := mI.(metav1.ObjectMeta)
	fsvcI, err := fsc.byFunction.Get(crd.CacheKey(&m))
	if err != nil {
		return err
	}
	fsvc := fsvcI.(*FuncSvc)
	fsvc.Atime = time.Now()
	return nil
}

func (fsc *functionServiceCache) DeleteByKubeObject(obj api.ObjectReference, minAge time.Duration) (bool, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:      DELETE_BY_OBJECT,
		kubernetesObject: obj,
		age:              minAge,
		responseChannel:  responseChannel,
	}
	resp := <-responseChannel
	return resp.deleted, resp.error
}

// _deleteByKubeObject deletes the entry keyed by Kubernetes Object, but only if it is
// at least minAge old.
func (fsc *functionServiceCache) _deleteByKubeObject(obj api.ObjectReference, minAge time.Duration) (bool, error) {
	mI, err := fsc.byKubeObject.Get(obj)
	if err != nil {
		return false, err
	}
	m := mI.(metav1.ObjectMeta)
	fsvcI, err := fsc.byFunction.Get(crd.CacheKey(&m))
	if err != nil {
		return false, err
	}
	fsvc := fsvcI.(*FuncSvc)

	if time.Now().Sub(fsvc.Atime) < minAge {
		return false, nil
	}

	fsc.byFunction.Delete(crd.CacheKey(&m))
<<<<<<< HEAD
	fsc.byAddress.Delete(fsvc.address)
	fsc.byKubeObject.Delete(obj)
=======
	fsc.byAddress.Delete(fsvc.Address)
	fsc.byPod.Delete(podName)
>>>>>>> Rebased from master and changed references to tpr -> crd
	return true, nil
}

func (fsc *functionServiceCache) ListOld(env *metav1.ObjectMeta, age time.Duration) ([]api.ObjectReference, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     LISTOLD,
		age:             age,
		env:             env,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.objects, resp.error
}

func (fsc *FunctionServiceCache) Log() {
	log.Printf("--- FunctionService Cache Contents")
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     LOG,
		responseChannel: responseChannel,
	}
	<-responseChannel
	log.Printf("--- FunctionService Cache Contents End")
}
