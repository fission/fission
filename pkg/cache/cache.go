// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"fmt"
	"sync"
	"time"

	ferror "github.com/fission/fission/pkg/error"
)

type (
	Value[V any] struct {
		ctime time.Time
		atime time.Time
		value V
	}
	Cache[K comparable, V any] struct {
		lock                sync.RWMutex
		cache               map[K]*Value[V]
		ctimeExpiry         time.Duration
		atimeExpiry         time.Duration
		expiryCheckInterval time.Duration
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
	interval := time.Minute
	if ctimeExpiry > 0 && ctimeExpiry < interval {
		interval = ctimeExpiry
	}
	if atimeExpiry > 0 && atimeExpiry < interval {
		interval = atimeExpiry
	}
	// Don't check too often to avoid high CPU usage
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}

	c := &Cache[K, V]{
		cache:               make(map[K]*Value[V]),
		ctimeExpiry:         ctimeExpiry,
		atimeExpiry:         atimeExpiry,
		expiryCheckInterval: interval,
	}
	if ctimeExpiry != time.Duration(0) || atimeExpiry != time.Duration(0) {
		go c.expiryService()
	}
	return c
}

func (c *Cache[K, V]) Get(key K) (V, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var zero V
	val, ok := c.cache[key]
	if !ok {
		return zero, ferror.MakeError(ferror.ErrorNotFound,
			fmt.Sprintf("key '%v' not found", key))
	}
	if c.IsOld(val) {
		delete(c.cache, key)
		return zero, ferror.MakeError(ferror.ErrorNotFound,
			fmt.Sprintf("key '%v' expired (atime %v)", key, val.atime))
	}
	// update atime
	val.atime = time.Now()
	return val.value, nil
}

// if key exists in the cache, the new value is NOT set; instead an
// error and the old value are returned
func (c *Cache[K, V]) Set(key K, value V) (V, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	now := time.Now()
	if val, ok := c.cache[key]; ok {
		val.atime = now
		return val.value, ferror.MakeError(ferror.ErrorNameExists, "key already exists")
	}
	c.cache[key] = &Value[V]{
		value: value,
		ctime: now,
		atime: now,
	}
	var zero V
	return zero, nil
}

func (c *Cache[K, V]) Upsert(key K, value V) {
	c.lock.Lock()
	defer c.lock.Unlock()

	now := time.Now()
	c.cache[key] = &Value[V]{
		value: value,
		ctime: now,
		atime: now,
	}
}

func (c *Cache[K, V]) Delete(key K) {
	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.cache, key)
}

func (c *Cache[K, V]) Copy() map[K]V {
	c.lock.RLock()
	defer c.lock.RUnlock()

	mapCopy := make(map[K]V, len(c.cache))
	for k, v := range c.cache {
		mapCopy[k] = v.value
	}
	return mapCopy
}

func (c *Cache[K, V]) expiryService() {
	ticker := time.NewTicker(c.expiryCheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		c.lock.Lock()
		for k, v := range c.cache {
			if c.IsOld(v) {
				delete(c.cache, k)
			}
		}
		c.lock.Unlock()
	}
}
