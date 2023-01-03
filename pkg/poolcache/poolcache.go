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
	"context"
	"fmt"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"

	ferror "github.com/fission/fission/pkg/error"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type requestType int

const (
	getValue requestType = iota
	listAvailableValue
	setValue
	markAvailable
	deleteValue
	setCPUUtilization
	getTotPodAndReq
)

type (
	// value used as "value" in cache
	Value struct {
		Val             interface{}
		ActiveRequests  int               // number of requests served by function pod
		CurrentCPUUsage resource.Quantity // current cpu usage of the specialized function pod
		CpuLimit        resource.Quantity // if currentCPUUsage is more than cpuLimit cache miss occurs in getValue request
	}
	// Cache is simple cache having two keys [function][address] mapped to value and requestChannel for operation on it
	Cache struct {
		cache          map[string]map[string]*Value
		requestChannel chan *request
		logger         *zap.Logger
	}

	request struct {
		requestType
		ctx             context.Context
		function        string
		address         string
		value           interface{}
		requestsPerPod  int
		activePods      int
		activeRequests  int
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
func NewPoolCache(logger *zap.Logger) *Cache {
	c := &Cache{
		cache:          make(map[string]map[string]*Value),
		requestChannel: make(chan *request),
		logger:         logger,
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
					if values[addr].ActiveRequests < req.requestsPerPod && values[addr].CurrentCPUUsage.Cmp(values[addr].CpuLimit) < 1 {
						// mark active
						values[addr].ActiveRequests++
						if c.logger.Core().Enabled(zap.DebugLevel) {
							otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Increase active requests with getValue", zap.String("function", req.function), zap.String("address", addr), zap.Int("activeRequests", values[addr].ActiveRequests))
						}
						resp.value = values[addr].Val
						found = true
						break
					}
				}
				if !found {
					// resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%v' all functions are busy", req.function))
					if c.logger.Core().Enabled(zap.DebugLevel) {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("function address not found", zap.String("function", req.function))
					}
					resp.error = ferror.MakeError(ferror.ErrorNotFound,
						fmt.Sprintf("function address not found for %s", req.function))
				}
				resp.totalActive = len(values)
			}
			req.responseChannel <- resp
		case setValue:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = make(map[string]*Value)
			}
			if _, ok := c.cache[req.function][req.address]; !ok {
				c.cache[req.function][req.address] = &Value{}
			}
			c.cache[req.function][req.address].Val = req.value
			c.cache[req.function][req.address].ActiveRequests++
			if c.logger.Core().Enabled(zap.DebugLevel) {
				otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Increase active requests with setValue", zap.String("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function][req.address].ActiveRequests))
			}
			c.cache[req.function][req.address].CpuLimit = req.cpuUsage
		case listAvailableValue:
			vals := make([]interface{}, 0)
			for key1, values := range c.cache {
				for key2, value := range values {
					debugLevel := c.logger.Core().Enabled(zap.DebugLevel)
					if debugLevel {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Reading active requests", zap.String("function", key1), zap.String("address", key2), zap.Int("activeRequests", value.ActiveRequests))
					}
					if value.ActiveRequests == 0 {
						if debugLevel {
							otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Function service with no active requests", zap.String("function", key1), zap.String("address", key2), zap.Int("activeRequests", value.ActiveRequests))
						}
						vals = append(vals, value.Val)
					}
				}
			}
			resp.allValues = vals
			req.responseChannel <- resp
		case setCPUUtilization:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = make(map[string]*Value)
			}
			if _, ok := c.cache[req.function][req.address]; ok {
				c.cache[req.function][req.address].CurrentCPUUsage = req.cpuUsage
			}
		case markAvailable:
			if _, ok := c.cache[req.function]; ok {
				if _, ok = c.cache[req.function][req.address]; ok {
					if c.cache[req.function][req.address].ActiveRequests > 0 {
						c.cache[req.function][req.address].ActiveRequests--
						if c.logger.Core().Enabled(zap.DebugLevel) {
							otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Decrease active requests", zap.String("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function][req.address].ActiveRequests))
						}
					} else {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Error("Invalid request to decrease active requests", zap.String("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function][req.address].ActiveRequests))
					}
				}
			}
		case deleteValue:
			delete(c.cache[req.function], req.address)
			req.responseChannel <- resp
		case getTotPodAndReq:
			values, ok := c.cache[req.function]
			// found := false
			if !ok {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("function Name '%v' not found", req.function))
			} else {
				// for addr := range values {
				// 	if values[addr].activeRequests < req.requestsPerPod && values[addr].currentCPUUsage.Cmp(values[addr].cpuLimit) < 1 {
				// 		// mark active
				// 		values[addr].activeRequests++
				// 		if c.logger.Core().Enabled(zap.DebugLevel) {
				// 			otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Increase active requests with getValue", zap.String("function", req.function), zap.String("address", addr), zap.Int("activeRequests", values[addr].activeRequests))
				// 		}
				// 		resp.value = values[addr].val
				// 		found = true
				// 		break
				// 	}
				// }
				// if !found {
				// 	resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%v' all functions are busy", req.function))
				// }
				// resp.totalActive = len(values)
				resp.value = values
			}
			req.responseChannel <- resp
		default:
			resp.error = ferror.MakeError(ferror.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

// GetValue returns a value interface with status inActive else return error
func (c *Cache) GetValue(ctx context.Context, function string, requestsPerPod, activePods, activeRequests int) (interface{}, int, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     getValue,
		function:        function,
		requestsPerPod:  requestsPerPod,
		activePods:      activePods,
		activeRequests:  activeRequests,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.totalActive, resp.error
}

// GetValue returns a value interface with status inActive else return error
func (c *Cache) GetPods(ctx context.Context, function string) (interface{}, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     getTotPodAndReq,
		function:        function,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.error
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
func (c *Cache) SetValue(ctx context.Context, function, address string, value interface{}, cpuLimit resource.Quantity) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     setValue,
		function:        function,
		address:         address,
		value:           value,
		cpuUsage:        cpuLimit,
		responseChannel: respChannel,
	}
}

// SetCPUUtilization updates/sets the CPU utilization limit for the pod
func (c *Cache) SetCPUUtilization(function, address string, cpuUsage resource.Quantity) {
	c.requestChannel <- &request{
		requestType:     setCPUUtilization,
		function:        function,
		address:         address,
		cpuUsage:        cpuUsage,
		responseChannel: make(chan *response),
	}
}

// MarkAvailable marks the value at key [function][address] as available
func (c *Cache) MarkAvailable(function, address string) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     markAvailable,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
}

// DeleteValue deletes the value at key composed of [function][address]
func (c *Cache) DeleteValue(ctx context.Context, function, address string) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     deleteValue,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}
