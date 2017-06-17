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
)

type (
	functionServiceCache struct {
		byFunction *cache.Cache // function -> funcSvc : map[fission.Metadata]*funcSvc
		byAddress  *cache.Cache // address -> function : map[string]fission.Metadata
		byPod      *cache.Cache // podname -> function : map[string]fission.Metadata

		requestChannel chan *fscRequest
	}
	fscRequest struct {
		requestType     fscRequestType
		address         string
		podName         string
		age             time.Duration
		responseChannel chan *fscResponse
	}
	fscResponse struct {
		podNames []string
		deleted  bool
		error
	}
)

func MakeFunctionServiceCache() *functionServiceCache {
	fsc := &functionServiceCache{
		byFunction:     cache.MakeCache(0, 0),
		byAddress:      cache.MakeCache(0, 0),
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
			byPodCopy := fsc.byPod.Copy()
			pods := make([]string, 0)
			for podNameI, mI := range byPodCopy {
				m := mI.(fission.Metadata)
				fsvcI, err := fsc.byFunction.Get(m)
				if err != nil {
					resp.error = err
				} else {
					fsvc := fsvcI.(*funcSvc)
					if time.Now().Sub(fsvc.atime) > req.age {
						podName := podNameI.(string)
						pods = append(pods, podName)
					}
				}
			}
			resp.podNames = pods
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
			err2 := fsc.TouchByAddress(f.address)
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

	err, _ = fsc.byAddress.Set(fsvc.address, *fsvc.function)
	if err != nil {
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	err, _ = fsc.byPod.Set(fsvc.podName, *fsvc.function)
	if err != nil {
		log.Printf("error caching fsvc: %v", err)
		return err, nil
	}
	setFuncAlive(fsvc.function.Name, fsvc.function.Uid, true)
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
	mI, err := fsc.byAddress.Get(address)
	if err != nil {
		return err
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

// _deleteByPod deletes the entry keyed by podName, but only if it is
// at least minAge old.
func (fsc *functionServiceCache) _deleteByPod(podName string, minAge time.Duration) (bool, error) {
	mI, err := fsc.byPod.Get(podName)
	if err != nil {
		return false, err
	}
	m := mI.(fission.Metadata)
	fsvcI, err := fsc.byFunction.Get(m)
	if err != nil {
		return false, err
	}
	fsvc := fsvcI.(*funcSvc)

	if time.Now().Sub(fsvc.atime) < minAge {
		return false, nil
	}

	fsc.byFunction.Delete(m)
	fsc.byAddress.Delete(fsvc.address)
	fsc.byPod.Delete(podName)
	observeFuncRunningTime(fsvc.function.Name, fsvc.function.Uid, fsvc.atime.Sub(fsvc.ctime).Seconds())
	observeFuncAliveTime(fsvc.function.Name, fsvc.function.Uid, time.Now().Sub(fsvc.ctime).Seconds())
	setFuncAlive(fsvc.function.Name, fsvc.function.Uid, false)
	return true, nil
}

func (fsc *functionServiceCache) ListOld(age time.Duration) ([]string, error) {
	responseChannel := make(chan *fscResponse)
	fsc.requestChannel <- &fscRequest{
		requestType:     LISTOLD,
		age:             age,
		responseChannel: responseChannel,
	}
	resp := <-responseChannel
	return resp.podNames, resp.error
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
