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

package fscache

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/fission/fission/pkg/crd"
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
	markSpecializationFailure
	logFuncSvc
)

type (
	funcSvcInfo struct {
		val             *FuncSvc
		activeRequests  int               // number of requests served by function pod
		currentCPUUsage resource.Quantity // current cpu usage of the specialized function pod
		cpuLimit        resource.Quantity // if currentCPUUsage is more than cpuLimit cache miss occurs in getValue request
	}

	funcSvcGroup struct {
		svcWaiting int
		svcRetain  int
		svcs       map[string]*funcSvcInfo
		queue      *Queue
	}

	// PoolCache implements a simple cache implementation having values mapped by two keys [function][address].
	// As of now PoolCache is only used by poolmanager executor
	PoolCache struct {
		cache          map[crd.CacheKeyWithGen]*funcSvcGroup
		requestChannel chan *request
		logger         *zap.Logger
	}

	request struct {
		requestType
		ctx             context.Context
		function        crd.CacheKeyWithGen
		address         string
		dumpWriter      io.Writer
		value           *FuncSvc
		requestsPerPod  int
		cpuUsage        resource.Quantity
		responseChannel chan *response
		concurrency     int
		svcsRetain      int
	}
	response struct {
		error
		allValues    []*FuncSvc
		value        *FuncSvc
		svcWaitValue *svcWait
	}
	svcWait struct {
		svcChannel chan *FuncSvc
		ctx        context.Context
	}
)

// NewPoolCache create a Cache object

func NewPoolCache(logger *zap.Logger) *PoolCache {
	c := &PoolCache{
		cache:          make(map[crd.CacheKeyWithGen]*funcSvcGroup),
		requestChannel: make(chan *request),
		logger:         logger,
	}
	go c.service()
	return c
}

func NewFuncSvcGroup() *funcSvcGroup {
	return &funcSvcGroup{
		svcs:  make(map[string]*funcSvcInfo),
		queue: NewQueue(),
	}
}

func (c *PoolCache) service() {
	for {
		req := <-c.requestChannel
		resp := &response{}
		switch req.requestType {
		case getValue:
			funcSvcGroup, ok := c.cache[req.function]
			if !ok {
				c.cache[req.function] = NewFuncSvcGroup()
				c.cache[req.function].svcWaiting++
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("function Name '%v' not found", req.function))
				req.responseChannel <- resp
				continue
			}
			found := false
			totalActiveRequests := 0
			for addr := range funcSvcGroup.svcs {
				totalActiveRequests += funcSvcGroup.svcs[addr].activeRequests
				if funcSvcGroup.svcs[addr].activeRequests < req.requestsPerPod &&
					funcSvcGroup.svcs[addr].currentCPUUsage.Cmp(funcSvcGroup.svcs[addr].cpuLimit) < 1 {
					// mark active
					funcSvcGroup.svcs[addr].activeRequests++
					if c.logger.Core().Enabled(zap.DebugLevel) {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Increase active requests with getValue", zap.Any("function", req.function), zap.String("address", addr), zap.Int("activeRequests", funcSvcGroup.svcs[addr].activeRequests))
					}
					resp.value = funcSvcGroup.svcs[addr].val
					found = true
					break
				}
			}
			if found {
				req.responseChannel <- resp
				continue
			}
			specializationInProgress := funcSvcGroup.svcWaiting - funcSvcGroup.queue.Len()
			capacity := ((specializationInProgress + len(funcSvcGroup.svcs)) * req.requestsPerPod) - (totalActiveRequests + funcSvcGroup.svcWaiting)
			if capacity > 0 {
				funcSvcGroup.svcWaiting++
				svcWait := &svcWait{
					svcChannel: make(chan *FuncSvc),
					ctx:        req.ctx,
				}
				resp.svcWaitValue = svcWait
				funcSvcGroup.queue.Push(svcWait)
				req.responseChannel <- resp
				continue
			}

			// concurrency should not be set to zero and
			//sum of specialization in progress and specialized pods should be less then req.concurrency
			if req.concurrency > 0 && (specializationInProgress+len(funcSvcGroup.svcs)) >= req.concurrency {
				resp.error = ferror.MakeError(ferror.ErrorTooManyRequests, fmt.Sprintf("function '%s' concurrency '%d' limit reached.", req.function, req.concurrency))
			} else {
				funcSvcGroup.svcWaiting++
				resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%s' all functions are busy", req.function))
			}
			req.responseChannel <- resp
		case setValue:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = NewFuncSvcGroup()
			}
			if _, ok := c.cache[req.function].svcs[req.address]; !ok {
				c.cache[req.function].svcs[req.address] = &funcSvcInfo{}
			}
			c.cache[req.function].svcRetain = req.svcsRetain
			c.cache[req.function].svcs[req.address].val = req.value
			c.cache[req.function].svcs[req.address].activeRequests++
			if c.cache[req.function].svcWaiting > 0 {
				c.cache[req.function].svcWaiting--
				svcCapacity := req.requestsPerPod - c.cache[req.function].svcs[req.address].activeRequests
				queueLen := c.cache[req.function].queue.Len()
				if svcCapacity > queueLen {
					svcCapacity = queueLen
				}
				for i := 0; i <= svcCapacity; {
					popped := c.cache[req.function].queue.Pop()
					if popped == nil {
						break
					}
					if popped.ctx.Err() == nil {
						popped.svcChannel <- req.value
						c.cache[req.function].svcs[req.address].activeRequests++
						i++
					}
					close(popped.svcChannel)
					c.cache[req.function].svcWaiting--
				}
			}
			if c.logger.Core().Enabled(zap.DebugLevel) {
				otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Increase active requests with setValue", zap.Any("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function].svcs[req.address].activeRequests))
			}
			c.cache[req.function].svcs[req.address].cpuLimit = req.cpuUsage
		case listAvailableValue:
			vals := make([]*FuncSvc, 0)
			for key1, values := range c.cache {
				svcCleanQuota := len(values.svcs) - values.svcRetain
				if svcCleanQuota <= 0 {
					continue
				}
				for key2, value := range values.svcs {
					debugLevel := c.logger.Core().Enabled(zap.DebugLevel)
					if debugLevel {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Reading active requests", zap.Any("function", key1), zap.String("address", key2), zap.Int("activeRequests", value.activeRequests))
					}
					if value.activeRequests == 0 && svcCleanQuota > 0 {
						if debugLevel {
							otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Function service with no active requests", zap.Any("function", key1), zap.String("address", key2), zap.Int("activeRequests", value.activeRequests))
						}
						vals = append(vals, value.val)
						svcCleanQuota--
					}
				}
			}
			resp.allValues = vals
			req.responseChannel <- resp
		case setCPUUtilization:
			if _, ok := c.cache[req.function]; !ok {
				c.cache[req.function] = NewFuncSvcGroup()
			}
			if _, ok := c.cache[req.function].svcs[req.address]; ok {
				c.cache[req.function].svcs[req.address].currentCPUUsage = req.cpuUsage
			}
		case markAvailable:
			if _, ok := c.cache[req.function]; ok {
				if _, ok = c.cache[req.function].svcs[req.address]; ok {
					if c.cache[req.function].svcs[req.address].activeRequests > 0 {
						c.cache[req.function].svcs[req.address].activeRequests--
						if c.logger.Core().Enabled(zap.DebugLevel) {
							otelUtils.LoggerWithTraceID(req.ctx, c.logger).Debug("Decrease active requests", zap.Any("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function].svcs[req.address].activeRequests))
						}
					} else {
						otelUtils.LoggerWithTraceID(req.ctx, c.logger).Error("Invalid request to decrease active requests", zap.Any("function", req.function), zap.String("address", req.address), zap.Int("activeRequests", c.cache[req.function].svcs[req.address].activeRequests))
					}
				}
			}
		case markSpecializationFailure:
			if c.cache[req.function].svcWaiting > c.cache[req.function].queue.Len() {
				c.cache[req.function].svcWaiting--
				if c.cache[req.function].svcWaiting == c.cache[req.function].queue.Len() {
					expiredRequests := c.cache[req.function].queue.Expired()
					c.cache[req.function].svcWaiting = c.cache[req.function].svcWaiting - expiredRequests
				}
			}
		case deleteValue:
			delete(c.cache[req.function].svcs, req.address)
			req.responseChannel <- resp
		case logFuncSvc:
			datawriter := bufio.NewWriter(req.dumpWriter)

			writefnSvcGrp := func(svcGrp *funcSvcGroup) error {
				_, err := datawriter.WriteString(fmt.Sprintf("svc_waiting:%d\tqueue_len:%d", svcGrp.svcWaiting, svcGrp.queue.Len()))
				if err != nil {
					return err
				}

				if len(svcGrp.svcs) == 0 {
					_, err := datawriter.WriteString("\n")
					if err != nil {
						return err
					}
				}

				for addr, fnSvc := range svcGrp.svcs {
					_, err := datawriter.WriteString(fmt.Sprintf("\tfunction_name:%s\tfn_svc_address:%s\tactive_req:%d\tcurrent_cpu_usage:%v\tcpu_limit:%v\n",
						fnSvc.val.Function.Name, addr, fnSvc.activeRequests, fnSvc.currentCPUUsage, fnSvc.cpuLimit))
					if err != nil {
						return err
					}
				}
				return nil
			}

			for _, fnSvcGrp := range c.cache {
				err := writefnSvcGrp(fnSvcGrp)
				if err != nil {
					resp.error = err
					break
				}
			}
			err := datawriter.Flush()
			if err != nil {
				if resp.error == nil {
					resp.error = err
				} else {
					resp.error = fmt.Errorf("%v, %v", resp.error, err)
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

// GetValue returns a function service with status in Active else return error
func (c *PoolCache) GetSvcValue(ctx context.Context, function crd.CacheKeyWithGen, requestsPerPod int, concurrency int) (*FuncSvc, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     getValue,
		function:        function,
		concurrency:     concurrency,
		requestsPerPod:  requestsPerPod,
		responseChannel: respChannel,
	}
	resp := <-respChannel

	if resp.svcWaitValue != nil {
		select {
		case <-ctx.Done():
			return resp.value, ctx.Err()
		case funcSvc := <-resp.svcWaitValue.svcChannel:
			return funcSvc, nil
		}
	}
	return resp.value, resp.error
}

// ListAvailableValue returns a list of the available function services stored in the Cache
func (c *PoolCache) ListAvailableValue() []*FuncSvc {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     listAvailableValue,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.allValues
}

// SetValue marks the value at key [function][address] as active(begin used)
func (c *PoolCache) SetSvcValue(ctx context.Context, function crd.CacheKeyWithGen, address string, value *FuncSvc, cpuLimit resource.Quantity, requestsPerPod, svcsRetain int) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		ctx:             ctx,
		requestType:     setValue,
		function:        function,
		address:         address,
		value:           value,
		cpuUsage:        cpuLimit,
		requestsPerPod:  requestsPerPod,
		svcsRetain:      svcsRetain,
		responseChannel: respChannel,
	}
}

// SetCPUUtilization updates/sets the CPU utilization limit for the pod
func (c *PoolCache) SetCPUUtilization(function crd.CacheKeyWithGen, address string, cpuUsage resource.Quantity) {
	c.requestChannel <- &request{
		requestType:     setCPUUtilization,
		function:        function,
		address:         address,
		cpuUsage:        cpuUsage,
		responseChannel: make(chan *response),
	}
}

// MarkAvailable marks the value at key [function][address] as available
func (c *PoolCache) MarkAvailable(function crd.CacheKeyWithGen, address string) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     markAvailable,
		function:        function,
		address:         address,
		responseChannel: respChannel,
	}
}

// DeleteValue deletes the value at key composed of [function][address]
func (c *PoolCache) DeleteValue(ctx context.Context, function crd.CacheKeyWithGen, address string) error {
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

// ReduceSpecializationInProgress reduces the svcWaiting count
func (c *PoolCache) MarkSpecializationFailure(function crd.CacheKeyWithGen) {
	c.requestChannel <- &request{
		requestType:     markSpecializationFailure,
		function:        function,
		responseChannel: make(chan *response),
	}
}

func (c *PoolCache) LogFnSvcGroup(ctx context.Context, file io.Writer) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     logFuncSvc,
		dumpWriter:      file,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}
