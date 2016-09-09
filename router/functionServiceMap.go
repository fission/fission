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
	"errors"
	"log"
	"net/url"

	"github.com/platform9/fission"
)

type requestType int

const (
	LOOKUP   requestType = iota // lookup the map
	ASSIGN                      // assign function
	NEXT_GEN                    // increment current generation
	SWEEP                       // delete all but the current generation
)

type functionServiceMapResponse struct {
	serviceUrl url.URL
	error
}
type functionServiceMapRequest struct {
	fission.Function
	serviceUrl url.URL
	requestType
	responseChannel chan<- functionServiceMapResponse
}
type functionServiceMapEntry struct {
	serviceUrl url.URL
	generation uint64
}

type functionServiceMap struct {
	// map (funcname, uid) -> url
	svc               map[fission.Function]functionServiceMapEntry
	currentGeneration uint64
	requestChannel    chan *functionServiceMapRequest
}

func makeFunctionServiceMap() *functionServiceMap {
	fmap := &functionServiceMap{}
	fmap.requestChannel = make(chan *functionServiceMapRequest)
	fmap.svc = make(map[fission.Function]functionServiceMapEntry)
	go fmap.functionServiceMapWork()
	return fmap
}

func (fmap *functionServiceMap) functionServiceMapWork() {
	for {
		req := <-fmap.requestChannel
		switch req.requestType {
		case LOOKUP:
			e, present := fmap.svc[req.Function]
			if present {
				req.responseChannel <- functionServiceMapResponse{serviceUrl: e.serviceUrl}
			} else {
				req.responseChannel <- functionServiceMapResponse{error: errors.New("not found")}
			}
		case ASSIGN:
			fmap.svc[req.Function] =
				functionServiceMapEntry{serviceUrl: req.serviceUrl, generation: fmap.currentGeneration}
			// no response
		case NEXT_GEN:
			fmap.currentGeneration++
			// no response
		case SWEEP:
			log.Panic("not implemented")
		default:
			log.Panic("bad request")
		}
	}
}

func (fmap *functionServiceMap) lookup(f *fission.Function) (*url.URL, error) {
	respChannel := make(chan functionServiceMapResponse)
	fmap.requestChannel <- &functionServiceMapRequest{Function: *f, requestType: LOOKUP, responseChannel: respChannel}
	resp := <-respChannel
	if resp.error != nil {
		return nil, resp.error
	} else {
		return &resp.serviceUrl, nil
	}
}

func (fmap *functionServiceMap) assign(f *fission.Function, serviceUrl *url.URL) {
	fmap.requestChannel <- &functionServiceMapRequest{Function: *f, serviceUrl: *serviceUrl, requestType: ASSIGN}
}

func (fmap *functionServiceMap) nextGen() {
	fmap.requestChannel <- &functionServiceMapRequest{requestType: NEXT_GEN}
}

func (fmap *functionServiceMap) sweep() {
	fmap.requestChannel <- &functionServiceMapRequest{requestType: SWEEP}
}
