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

package newcache

import (
	"fmt"
	"time"

	ferror "github.com/fission/fission/pkg/error"
)

type requestType int

const (
	GET requestType = iota
	SET
	DELETE
	EXPIRE
	GETALL
	UNSET
	TOTALACTIVE
)

type (
	Value struct {
		value    interface{}
		isActive bool
	}
	Cache struct {
		cache          map[interface{}]map[interface{}]*Value
		requestChannel chan *request
	}

	request struct {
		requestType
		function        interface{}
		address         interface{}
		value           interface{}
		responseChannel chan *response
	}
	response struct {
		error
		allFsvcs    []interface{}
		value       interface{}
		totalActive int
	}
)

func MakeCache(ctimeExpiry, atimeExpiry time.Duration) *Cache {
	c := &Cache{
		cache:          make(map[interface{}]map[interface{}]*Value),
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
			values, ok := c.cache[req.function]
			found := false
			if !ok {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("function Name '%v' not found", req.function))
			} else {
				for addr := range values {
					if !values[addr].isActive {
						// update atime
						// mark active
						values[addr].isActive = true
						resp.value = values[addr].value
						found = true
						break
					}
				}
			}
			if !found {
				resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("funtion '%v' No inactive function found", req.function))
			}
			req.responseChannel <- resp
		case SET:
			if _, ok := c.cache[req.function]; ok {
				c.cache[req.function][req.address] = &Value{
					value:    req.value,
					isActive: true,
				}
			} else {
				c.cache[req.function] = make(map[interface{}]*Value)
				c.cache[req.function][req.address] = &Value{
					value:    req.value,
					isActive: true,
				}
			}
		case DELETE:
			delete(c.cache[req.function], req.address)
			req.responseChannel <- resp
		case GETALL:
			vals := make([]interface{}, 0)
			for _, values := range c.cache {
				for _, value := range values {
					vals = append(vals, value.value)
				}
			}
			resp.allFsvcs = vals
			req.responseChannel <- resp
		case UNSET:
			if _, ok := c.cache[req.function]; ok {
				if _, ok = c.cache[req.function][req.address]; ok {
					c.cache[req.function][req.address].isActive = false
				}
			}
		case TOTALACTIVE:
			if values, ok := c.cache[req.function]; ok {
				for addr := range values {
					if values[addr].isActive {
						resp.totalActive++
					}
				}
			}

			req.responseChannel <- resp
		default:
			resp.error = ferror.MakeError(ferror.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

func (c *Cache) Get(function interface{}) (interface{}, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     GET,
		function:        function,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.error
}

func (c *Cache) GetTotalActive(function interface{}) int {

	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     TOTALACTIVE,
		function:        function,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.totalActive
}

func (c *Cache) Set(function, address, value interface{}) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     SET,
		function:        function,
		address:         address,
		value:           value,
		responseChannel: respChannel,
	}
}

func (c *Cache) UnSet(function, address interface{}) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     UNSET,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
}

func (c *Cache) Delete(function, address interface{}) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     DELETE,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}

func (c *Cache) GetAll() []interface{} {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     GETALL,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.allFsvcs
}