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
	"k8s.io/apimachinery/pkg/api/resource"
)

type requestType int

const (
	getValue requestType = iota
	listAvailableValue
	setValue
	markAvailable
	deleteValue
	setCPUUtilization
)

type (
	// value used as "value" in cache
	value struct {
		val             interface{}
		activeRequests  int               // number of requests served by function pod
		currentCPUUsage resource.Quantity // current cpu usage of the specialized function pod
		cpuLimit        resource.Quantity // if currentCPUUsage is more than cpuLimit cache miss occurs in getValue request
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
		cpuUsage        resource.Quantity
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
					if values[addr].activeRequests < req.requestsPerPod && values[addr].currentCPUUsage.Cmp(values[addr].cpuLimit) < 1 {
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
			c.cache[req.function][req.address].activeRequests++
			c.cache[req.function][req.address].cpuLimit = req.cpuUsage
		case listAvailableValue:
			vals := make([]interface{}, 0)
			for _, values := range c.cache {
				for _, value := range values {
					if value.activeRequests == 0 {
						vals = append(vals, value.val)
					}
				}
			}
			resp.allValues = vals
			req.responseChannel <- resp
		case setCPUUtilization:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = make(map[interface{}]*value)
			}
			if _, ok := c.cache[req.function][req.address]; ok {
				c.cache[req.function][req.address].currentCPUUsage = req.cpuUsage
			}
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
func (c *Cache) GetValue(function interface{}, requestsPerPod int) (interface{}, int, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     getValue,
		function:        function,
		requestsPerPod:  requestsPerPod,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.totalActive, resp.error
}

// ListAvailableValue returns a list of the available function services stored in the Cache
func (c *Cache) ListAvailableValue() []interface{} {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     listAvailableValue,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.allValues
}

// SetValue marks the value at key [function][address] as active(begin used)
func (c *Cache) SetValue(function, address, value interface{}, cpuLimit resource.Quantity) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     setValue,
		function:        function,
		address:         address,
		value:           value,
		cpuUsage:        cpuLimit,
		responseChannel: respChannel,
	}
}

// SetCPUUtilization updates/sets the CPU utilization limit for the pod
func (c *Cache) SetCPUUtilization(function, address interface{}, cpuUsage resource.Quantity) {
	c.requestChannel <- &request{
		requestType:     setCPUUtilization,
		function:        function,
		address:         address,
		cpuUsage:        cpuUsage,
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
