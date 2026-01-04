/*
Copyright 2018 The Fission Authors.

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

package throttler

import (
	"errors"
	"sync"
	"time"
)

type (
	// Throttler is a simple throttling mechanism that provides the abil-
	// ity to limit the total amount of requests to do the same thing at
	// the same time.
	Throttler struct {
		mu    sync.Mutex
		locks map[string]*entry
		ttl   time.Duration
	}

	entry struct {
		done      chan struct{}
		createdAt time.Time
	}
)

// MakeThrottler returns a throttler that able to limit total amounts of goroutines from
// doing the same thing at the same time.
func MakeThrottler(timeExpiry time.Duration) *Throttler {
	t := &Throttler{
		locks: make(map[string]*entry),
		ttl:   timeExpiry,
	}
	go t.expiryService()
	return t
}

// RunOnce accepts two arguments:
// 1. function metadata:
//   It's used to check whether the actionLock of a function exists.
//
//   If not exists, an actionLock will be inserted into the map with
//   passing key. Then, throttler pass true to callbackFunc to indi-
//   cate this goroutine is the first goroutine and is responsible for
//   calling backend service, like updating service URL entry in router.
//
//   If exists, the goroutines have to wait until the first goroutine
//   finishes the update process.
//
// 2. callback function:
//   The callback function is a function accepts one bool argument which
//   indicates whether a goroutine is responsible for getting/updating a
//   resource from backend service or waiting for the first goroutine to
//   be finished.
//
//   Example callback function:
//
//   func(firstToTheLock bool) (interface{}, error) {
//    var u *url.URL
//
//   if firstToTheLock { // first to the service url
//   // Call to backend services then do something else.
//   // For example, get service url from executor then update router cache.
//   } else {
//   // Do something here.
//   // For example, get service url from cache
//   }
//
//   return AnythingYouWant{}, error
//   }

func (t *Throttler) RunOnce(resourceKey string, callbackFunc func(bool) (any, error)) (any, error) {
	t.mu.Lock()
	e, ok := t.locks[resourceKey]

	if ok {
		if time.Since(e.createdAt) < t.ttl {
			t.mu.Unlock()
			// Wait for the first goroutine to finish
			select {
			case <-e.done:
				return callbackFunc(false)
			case <-time.After(t.ttl):
				return nil, errors.New("error waiting for actionLock to be released: Exceeded timeout")
			}
		}
		// Expired, we take over.
		// We don't need to delete explicitly, we just overwrite.
	}

	// Create new entry
	myEntry := &entry{
		done:      make(chan struct{}),
		createdAt: time.Now(),
	}
	t.locks[resourceKey] = myEntry
	t.mu.Unlock()

	// Ensure cleanup
	defer func() {
		t.mu.Lock()
		// Only remove if it is still OUR entry
		if t.locks[resourceKey] == myEntry {
			delete(t.locks, resourceKey)
		}
		t.mu.Unlock()
		close(myEntry.done)
	}()

	return callbackFunc(true)
}

// expiryService periodically expires time-out locks.
func (t *Throttler) expiryService() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		t.mu.Lock()
		for k, v := range t.locks {
			if time.Since(v.createdAt) > t.ttl {
				delete(t.locks, k)
			}
		}
		t.mu.Unlock()
	}
}
