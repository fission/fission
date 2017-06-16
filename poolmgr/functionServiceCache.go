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

	"github.com/fission/fission"
	"github.com/fission/fission/cache"
)

type fscRequestType int

const (
	TOUCH fscRequestType = iota
	LISTOLD
	LOG
	DELETE_BY_POD
	DELETE_BY_FUNCNAME
)

type (
	functionServiceCache struct {
		byFunction   *cache.Cache // function -> funcSvc : map[fission.Metadata]*funcSvc
		byPodAddress *cache.Cache // address -> function : map[string]fission.Metadata
		bySvcAddress *cache.Cache // address -> function : map[string]fission.Metadata
		byPod        *cache.Cache // podname -> function : map[string]fission.Metadata

		requestChannel chan *fscRequest
	}
	fscRequest struct {
		requestType     fscRequestType
		address         string
		podName         string
		funcMeta        fission.Metadata
		age             time.Duration
		responseChannel chan *fscResponse
	}
	fscResponse struct {
		items   []fission.Metadata
		deleted bool
		error
	}
)

func MakeFunctionServiceCache() *functionServiceCache {
	fsc := &functionServiceCache{
		byFunction:     cache.MakeCache(0, 0),
		byPodAddress:   cache.MakeCache(0, 0),
		bySvcAddress:   cache.MakeCache(0, 0),
		byPod:          cache.MakeCache(0, 0),
		requestChannel: make(chan *fscRequest),
	}
	go fsc.service()
	return fsc
}

func (fsc *functionServiceCache) service() {
	for {
		req := <-fsc.requestChannel
		resp := &fscResponse{}
		switch req.requestType {
		case TOUCH:
			// update atime for this function svc
			resp.error = fsc._touchByAddress(req.address)
		case LISTOLD:
			// get svcs idle for > req.age
			byFunctionCopy := fsc.byFunction.Copy()
			items := make([]fission.Metadata, 0)
			for mI, fsvcI := range byFunctionCopy {
				m := mI.(fission.Metadata)
				fsvc := fsvcI.(*funcSvc)
				if time.Now().Sub(fsvc.atime) > req.age {
					items = append(items, m)
				}
			}
			resp.items = items
		case LOG:
			funcCopy := fsc.byFunction.Copy()
			log.Printf("Cache has %v entries", len(funcCopy))
			for mI, fsvcI := range funcCopy {
				m := mI.(fission.Metadata)
				fsvc := fsvcI.(*funcSvc)
				log.Printf("%v:%v\t%v", m.Name, m.Uid, fsvc.podName)
			}
		case DELETE_BY_POD:
			resp.deleted, resp.error = fsc._deleteByPod(req.podName, req.age)
		case DELETE_BY_FUNCNAME:
			resp.deleted, resp.error = fsc._deleteByFuncMeta(req.funcMeta, req.age)
		}
		req.responseChannel <- resp
	}
}

func (fsc *functionServiceCache) GetByFunction(m *fission.Metadata) (*funcSvc, error) {
	fsvcI, err := fsc.byFunction.Get(*m)
	if err != nil {
		return nil, err
	}
	// update atime
	fsvc := fsvcI.(*funcSvc)
	fsvc.atime = time.Now()

	fsvcCopy := *fsvc
	return &fsvcCopy, nil
}

func (fsc *functionServiceCache) Add(fsvc funcSvc) (error, *funcSvc) {
	err, existing := fsc.byFunction.Set(*fsvc.function, &fsvc)
	if err != nil {
		if existing != nil {
			f := existing.(*funcSvc)
			err2 := fsc.TouchByAddress(f.podAddress)
			if err2 != nil {
				return err2, nil
			}
			fCopy := *f
			return err, &fCopy
		}
		return err, nil
	}
	now := time.Now()
	fsvc.ctime = now
	fsvc.atime = now

	err, _ = fsc.byPodAddress.Set(fsvc.podAddress, *fsvc.function)
	if err != nil {
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	err, _ = fsc.bySvcAddress.Set(fsvc.svcAddress, *fsvc.function)
	if err != nil {
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	err, _ = fsc.byPod.Set(fsvc.podName, *fsvc.function)
	if err != nil {
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	return nil, nil
}

func (fsc *functionServiceCache) TouchByAddress(address string) error {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     TOUCH,
		address:         address,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.error
}

func (fsc *functionServiceCache) _touchByAddress(address string) error {
	var mI interface{}
	var err error

	// Lookup the address as both pod and svc address.
	mI, err = fsc.byPodAddress.Get(address)
	if err != nil {
		fe, ok := err.(fission.Error)
		if ok && fe.Code == fission.ErrorNotFound {
			mI, err = fsc.bySvcAddress.Get(address)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	m := mI.(fission.Metadata)
	fsvcI, err := fsc.byFunction.Get(m)
	if err != nil {
		return err
	}
	fsvc := fsvcI.(*funcSvc)
	fsvc.atime = time.Now()
	return nil
}

func (fsc *functionServiceCache) DeleteByPod(podName string, minAge time.Duration) (bool, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     DELETE_BY_POD,
		podName:         podName,
		age:             minAge,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.deleted, resp.error
}

func (fsc *functionServiceCache) DeleteByFuncMeta(funcMeta fission.Metadata, minAge time.Duration) (bool, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     DELETE_BY_FUNCNAME,
		funcMeta:        funcMeta,
		age:             minAge,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.deleted, resp.error
}

// _deleteByPod deletes the entry keyed by podName, but only if it is
// at least minAge old.
func (fsc *functionServiceCache) _deleteByPod(podName string, minAge time.Duration) (bool, error) {
	mI, err := fsc.byPod.Get(podName)
	if err != nil {
		return false, err
	}
	m := mI.(fission.Metadata)

	return fsc._deleteByFuncMeta(m, minAge)
}

func (fsc *functionServiceCache) _deleteByFuncMeta(m fission.Metadata, minAge time.Duration) (bool, error) {
	fsvcI, err := fsc.byFunction.Get(m)
	if err != nil {
		return false, err
	}
	fsvc := fsvcI.(*funcSvc)

	if time.Now().Sub(fsvc.atime) < minAge {
		return false, nil
	}

	fsc.byFunction.Delete(m)
	fsc.byPodAddress.Delete(fsvc.podAddress)
	fsc.bySvcAddress.Delete(fsvc.svcAddress)
	fsc.byPod.Delete(fsvc.podName)
	return true, nil
}

func (fsc *functionServiceCache) ListOld(age time.Duration) ([]fission.Metadata, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     LISTOLD,
		age:             age,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.items, resp.error
}

func (fsc *functionServiceCache) Log() {
	log.Printf("--- FunctionService Cache Contents")
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     LOG,
		responseChannel: responseChannel,
	}
	<-responseChannel
	log.Printf("--- FunctionService Cache Contents End")
}
