// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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

// RunOnce coalesces concurrent callers on resourceKey.
//
// The first goroutine to arrive for a key becomes the leader: it inserts an
// entry into the map and runs callbackFunc(true), which is responsible for
// talking to the backend (for example asking the executor for a service URL
// and updating the router cache). Every goroutine that arrives while the
// leader's entry is live waits for it to finish and then runs
// callbackFunc(false), which should read the leader's result (for example
// from the cache). A follower that waits longer than the throttler TTL gives
// up with an error and the zero T. An entry older than the TTL is treated as
// expired and the next caller takes over as leader.
//
// The result type is the callback's own, so callers never round-trip through
// an untyped value:
//
//	rec, err := t.RunOnce(key, func(leader bool) (svcRecord, error) {
//		if leader {
//			return fetchFromBackend()
//		}
//		return readFromCache()
//	})
func (t *Throttler) RunOnce[T any](resourceKey string, callbackFunc func(bool) (T, error)) (T, error) {
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
				var zero T
				return zero, errors.New("error waiting for throttler entry to be released: exceeded timeout")
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
