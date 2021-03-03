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

// Package poolcache implements a simple cache implementation having values mapped by two keys.
// As of now this package is only used by poolmanager executor
package poolcache

import (
	"fmt"

	ferror "github.com/fission/fission/pkg/error"
)

type requestType int

const (
	getValue requestType = iota
	listAvailableValue
	getTotalAvailable
	setValue
	markAvailable
	deleteValue
	setCPUPercentage
)

type (
	// value used as "value" in cache
	value struct {
		val            interface{}
		activeRequests int
		isActive       bool
		cpuPercentage  float64
	}
	// Cache is simple cache having two keys [function][address] mapped to value and requestChannel for operation on it
	Cache struct {
		cache          map[interface{}]map[interface{}]*value
		requestChannel chan *request
	}

	request struct {
		requestType
		function        interface{}
		address         interface{}
		value           interface{}
		requestsPerPod  int
		cpuLimit        float64
		responseChannel chan *response
	}
	response struct {
		error
		allValues   []interface{}
		value       interface{}
		totalActive int
	}
)

// NewPoolCache create a Cache object
func NewPoolCache() *Cache {
	c := &Cache{
		cache:          make(map[interface{}]map[interface{}]*value),
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
		case getValue:
			values, ok := c.cache[req.function]
			found := false
			if !ok {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("function Name '%v' not found", req.function))
			} else {
				for addr := range values {
					if values[addr].activeRequests < req.requestsPerPod && values[addr].cpuPercentage < req.cpuLimit {
						// mark active
						values[addr].activeRequests++
						resp.value = values[addr].val
						found = true
						break
					}
				}
				if !found {
					resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%v' all functions are busy", req.function))
				}
				resp.totalActive = len(values)
			}
			req.responseChannel <- resp
		case setValue:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = make(map[interface{}]*value)
			}
			if _, ok := c.cache[req.function][req.address]; !ok {
				c.cache[req.function][req.address] = &value{}
			}
			c.cache[req.function][req.address].val = req.value
		case setCPUPercentage:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = make(map[interface{}]*value)
			}
			if _, ok := c.cache[req.function][req.address]; !ok {
				c.cache[req.function][req.address] = &value{}
			}
			c.cache[req.function][req.address].cpuPercentage = req.cpuLimit
		case markAvailable:
			if _, ok := c.cache[req.function]; ok {
				if _, ok = c.cache[req.function][req.address]; ok {
					c.cache[req.function][req.address].activeRequests--
				}
			}
		case deleteValue:
			delete(c.cache[req.function], req.address)
			req.responseChannel <- resp
		default:
			resp.error = ferror.MakeError(ferror.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

// GetValue returns a value interface with status inActive else return error
func (c *Cache) GetValue(function interface{}, requestsPerPod int, cpuLimit float64) (interface{}, int, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     getValue,
		function:        function,
		requestsPerPod:  requestsPerPod,
		cpuLimit:        cpuLimit,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.totalActive, resp.error
}

// SetValue marks the value at key [function][address] as active(begin used)
func (c *Cache) SetValue(function, address, value interface{}) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     setValue,
		function:        function,
		address:         address,
		value:           value,
		responseChannel: respChannel,
	}
}

// SetCPUPercentage updates/sets the CPU utilization limit for the pod
func (c *Cache) SetCPUPercentage(function, address interface{}, cpuLimit float64) {
	c.requestChannel <- &request{
		requestType:     setCPUPercentage,
		function:        function,
		address:         address,
		cpuLimit:        cpuLimit,
		responseChannel: make(chan *response),
	}
}

// MarkAvailable marks the value at key [function][address] as available
func (c *Cache) MarkAvailable(function, address interface{}) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     markAvailable,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
}

// DeleteValue deletes the value at key composed of [function][address]
func (c *Cache) DeleteValue(function, address interface{}) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     deleteValue,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}
