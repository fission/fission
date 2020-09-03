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
	COPY
	UNSET
)

type (
	Value struct {
		ctime    time.Time
		atime    time.Time
		value    interface{}
		isActive bool
	}
	Cache struct {
		cache          map[interface{}]map[interface{}]*Value
		ctimeExpiry    time.Duration
		atimeExpiry    time.Duration
		requestChannel chan *request
	}

	request struct {
		requestType
		functionName    interface{}
		address         interface{}
		value           interface{}
		responseChannel chan *response
	}
	response struct {
		error
		existingValue interface{}
		mapCopy       map[interface{}]interface{}
		value         interface{}
	}
)

func (c *Cache) IsOld(v *Value) bool {
	if (c.ctimeExpiry != time.Duration(0)) && (time.Since(v.ctime) > c.ctimeExpiry) {
		return true
	}

	if (c.atimeExpiry != time.Duration(0)) && (time.Since(v.atime) > c.atimeExpiry) {
		return true
	}

	return false
}

func MakeCache(ctimeExpiry, atimeExpiry time.Duration) *Cache {
	c := &Cache{
		cache:          make(map[interface{}]map[interface{}]*Value),
		ctimeExpiry:    ctimeExpiry,
		atimeExpiry:    atimeExpiry,
		requestChannel: make(chan *request),
	}
	go c.service()
	if ctimeExpiry != time.Duration(0) || atimeExpiry != time.Duration(0) {
		go c.expiryService()
	}
	return c
}

func (c *Cache) service() {
	for {
		req := <-c.requestChannel
		resp := &response{}
		switch req.requestType {
		case GET:
			values, ok := c.cache[req.functionName]
			found := false
			if !ok {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("function Name '%v' not found", req.functionName))

			} else {
				for addr := range values {
					if c.IsOld(values[addr]) {
						resp.error = ferror.MakeError(ferror.ErrorNotFound,
							fmt.Sprintf("function '%v' expired (atime %v)", req.functionName, values[addr].atime))
						delete(values, addr)
					} else if !values[addr].isActive {
						// update atime
						// mark active
						values[addr].atime = time.Now()
						values[addr].isActive = true
						resp.value = values[addr]
						found = true
						break
					}
				}
			}
			if !found {
				resp.error = ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("funtion '%v' No active function found", req.functionName))
			}
			req.responseChannel <- resp
		case SET:
			now := time.Now()
			if values, ok := c.cache[req.functionName]; ok {
				if val, ok := values[req.address]; ok {
					val.atime = time.Now()
					resp.existingValue = val.value
					resp.error = ferror.MakeError(ferror.ErrorNameExists, "key already exists")
				} else {
					c.cache[req.functionName][req.address] = &Value{
						value:    req.value,
						ctime:    now,
						atime:    now,
						isActive: true,
					}
				}
			} else {
				c.cache[req.functionName] = make(map[interface{}]*Value)
				c.cache[req.functionName][req.address] = &Value{
					value:    req.value,
					ctime:    now,
					atime:    now,
					isActive: true,
				}
			}
			req.responseChannel <- resp
		case DELETE:
			delete(c.cache[req.functionName], req.address)
			req.responseChannel <- resp
		case EXPIRE:
			for funcName, values := range c.cache {
				for addr, val := range values {
					if c.IsOld(val) {
						delete(c.cache[funcName], addr)
					}
				}
			}
			// no response
		case COPY:
			resp.mapCopy = make(map[interface{}]interface{})
			for funcName, values := range c.cache {
				resp.mapCopy[funcName] = values
			}
			req.responseChannel <- resp
		case UNSET:
			if _, ok := c.cache[req.functionName]; ok {
				c.cache[req.functionName][req.address].isActive = false
			}
		default:
			resp.error = ferror.MakeError(ferror.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

func (c *Cache) Get(functionName interface{}) (interface{}, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     GET,
		functionName:    functionName,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.error
}

// if key exists in the cache, the new value is NOT set; instead an
// error and the old value are returned
func (c *Cache) Set(functionName, address, value interface{}) (interface{}, error) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     SET,
		functionName:    functionName,
		address:         address,
		value:           value,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.existingValue, resp.error
}

func (c *Cache) UnSet(functionName, address interface{}) {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     UNSET,
		functionName:    functionName,
		address:         address,
		responseChannel: respChannel,
	}
}

func (c *Cache) Delete(functionName, address interface{}) error {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     DELETE,
		functionName:    functionName,
		address:         address,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}

func (c *Cache) Copy() map[interface{}]interface{} {
	respChannel := make(chan *response)
	c.requestChannel <- &request{
		requestType:     COPY,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.mapCopy
}

func (c *Cache) expiryService() {
	for {
		time.Sleep(time.Minute)
		c.requestChannel <- &request{
			requestType: EXPIRE,
		}
	}
}
