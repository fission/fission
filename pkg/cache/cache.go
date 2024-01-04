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
)

type (
	Value[V any] struct {
		ctime time.Time
		atime time.Time
		value V
	}
	Cache[K comparable, V any] struct {
		cache          map[K]*Value[V]
		ctimeExpiry    time.Duration
		atimeExpiry    time.Duration
		requestChannel chan *request[K, V]
	}

	request[K comparable, V any] struct {
		requestType
		key             K
		value           V
		responseChannel chan *response[K, V]
	}
	response[K comparable, V any] struct {
		error
		existingValue V
		mapCopy       map[K]V
		value         V
	}
)

func (c *Cache[K, V]) IsOld(v *Value[V]) bool {
	if (c.ctimeExpiry != time.Duration(0)) && (time.Since(v.ctime) > c.ctimeExpiry) {
		return true
	}

	if (c.atimeExpiry != time.Duration(0)) && (time.Since(v.atime) > c.atimeExpiry) {
		return true
	}

	return false
}

func MakeCache[K comparable, V any](ctimeExpiry, atimeExpiry time.Duration) *Cache[K, V] {
	c := &Cache[K, V]{
		cache:          make(map[K]*Value[V]),
		ctimeExpiry:    ctimeExpiry,
		atimeExpiry:    atimeExpiry,
		requestChannel: make(chan *request[K, V]),
	}
	go c.service()
	if ctimeExpiry != time.Duration(0) || atimeExpiry != time.Duration(0) {
		go c.expiryService()
	}
	return c
}

func (c *Cache[K, V]) service() {
	for {
		req := <-c.requestChannel
		resp := &response[K, V]{}
		switch req.requestType {
		case GET:
			val, ok := c.cache[req.key]
			if !ok {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("key '%v' not found", req.key))
			} else if c.IsOld(val) {
				resp.error = ferror.MakeError(ferror.ErrorNotFound,
					fmt.Sprintf("key '%v' expired (atime %v)", req.key, val.atime))
				delete(c.cache, req.key)
			} else {
				// update atime
				val.atime = time.Now()
				c.cache[req.key] = val
				resp.value = val.value
			}
			req.responseChannel <- resp
		case SET:
			now := time.Now()
			if _, ok := c.cache[req.key]; ok {
				val := c.cache[req.key]
				val.atime = time.Now()
				resp.existingValue = val.value
				resp.error = ferror.MakeError(ferror.ErrorNameExists, "key already exists")
			} else {
				c.cache[req.key] = &Value[V]{
					value: req.value,
					ctime: now,
					atime: now,
				}
			}
			req.responseChannel <- resp
		case DELETE:
			delete(c.cache, req.key)
			req.responseChannel <- resp
		case EXPIRE:
			for k, v := range c.cache {
				if c.IsOld(v) {
					delete(c.cache, k)
				}
			}
			// no response
		case COPY:
			resp.mapCopy = make(map[K]V)
			for k, v := range c.cache {
				resp.mapCopy[k] = v.value
			}
			req.responseChannel <- resp
		default:
			resp.error = ferror.MakeError(ferror.ErrorInvalidArgument,
				fmt.Sprintf("invalid request type: %v", req.requestType))
			req.responseChannel <- resp
		}
	}
}

func (c *Cache[K, V]) Get(key K) (V, error) {
	respChannel := make(chan *response[K, V])
	c.requestChannel <- &request[K, V]{
		requestType:     GET,
		key:             key,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.value, resp.error
}

// if key exists in the cache, the new value is NOT set; instead an
// error and the old value are returned
func (c *Cache[K, V]) Set(key K, value V) (V, error) {
	respChannel := make(chan *response[K, V])
	c.requestChannel <- &request[K, V]{
		requestType:     SET,
		key:             key,
		value:           value,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.existingValue, resp.error
}

func (c *Cache[K, V]) Delete(key K) error {
	respChannel := make(chan *response[K, V])
	c.requestChannel <- &request[K, V]{
		requestType:     DELETE,
		key:             key,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.error
}

func (c *Cache[K, V]) Copy() map[K]V {
	respChannel := make(chan *response[K, V])
	c.requestChannel <- &request[K, V]{
		requestType:     COPY,
		responseChannel: respChannel,
	}
	resp := <-respChannel
	return resp.mapCopy
}

func (c *Cache[K, V]) expiryService() {
	for {
		time.Sleep(time.Minute)
		c.requestChannel <- &request[K, V]{
			requestType: EXPIRE,
		}
	}
}
