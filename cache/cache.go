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

package cache

import (
	"time"

	"fmt"
	"github.com/platform9/fission"
)

type requestType int

const (
	GET requestType = iota
	SET
	DELETE
)

type (
	Value struct {
		ctime time.Time
		atime time.Time
		value interface{}
	}
	Cache struct {
		cache          map[interface{}]Value
		requestChannel chan *request
	}

	request struct {
		requestType
		key             interface{}
		value           interface{}
		responseChannel chan *response
	}
	response struct {
		error
		value interface{}
	}
)

func MakeCache() *Cache {
	c := &Cache{
		cache:          make(map[interface{}]Value),
		requestChannel: make(chan *request),
	}
	go c.service()
	return c
}

func (c *Cache) service() {
	for {
		req := <-c.requestChannel
		resp := &response{}
		switch req.requestType {
		case GET:
			val, ok := c.cache[req.key]
			if !ok {
				resp.error = fission.MakeError(fission.ErrorNotFound,
					fmt.Sprintf("key '%v' not found", req.key))
			}
			val.atime = time.Now()
			c.cache[req.key] = val

			resp.value = val.value
			req.responseChannel <- resp
		case SET:
			now := time.Now()
			c.cache[req.key] = Value{
				value: req.value,
				ctime: now,
				atime: now,
			}
			req.responseChannel <- resp
		case DELETE:
			delete(c.cache, req.key)
			req.responseChannel <- resp
		default:
			resp.error = fission.MakeError(fission.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

func (c *Cache) Get(key interface{}) (interface{}, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     GET,
		key:             key,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.error
}

func (c *Cache) Set(key interface{}, value interface{}) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     SET,
		key:             key,
		value:           value,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}

func (c *Cache) Delete(key interface{}) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     DELETE,
		key:             key,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}
