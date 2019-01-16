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
	"sync"
	"time"

	"github.com/pkg/errors"
)

type throttlerOperationType int

const (
	GET throttlerOperationType = iota
	DELETE
	EXPIRE
)

type (
	// actionLock is a lock that indicates whether a resource with
	// certain key is being updated or not.
	actionLock struct {
		wg         *sync.WaitGroup
		ctimestamp time.Time // creation time of lock
		timeExpiry time.Duration
	}

	// Throttler is a simple throttling mechanism that provides the abil-
	// ity to limit the total amount of requests to do the same thing at
	// the same time.
	//
	// In router, for example, multiple goroutines may try to get the la-
	// test service URL from executor when there is no service URL entry
	// in the cache and caused executor overloaded because of receiving
	// massive requests. With throttler, we can easily limit there is at
	// most one requests being sent to executor.
	Throttler struct {
		requestChan    chan *request
		locks          map[string]*actionLock
		lockTimeExpiry time.Duration
	}

	request struct {
		requestType  throttlerOperationType
		responseChan chan *response
		resourceKey  string
	}

	response struct {
		lock           *actionLock
		firstGoroutine bool // denote this goroutine is the first goroutine
	}
)

func (l *actionLock) isOld() bool {
	return time.Since(l.ctimestamp) > l.timeExpiry
}

func (l *actionLock) wait() error {
	ch := make(chan struct{})

	go func(wg *sync.WaitGroup, ch chan struct{}) {
		wg.Wait()
		close(ch)
	}(l.wg, ch)

	select {
	case <-ch:
		return nil
	case <-time.After(l.timeExpiry):
		return errors.New("Error waiting for actionLock to be released: Exceeded timeout")
	}
}

// MakeThrottler returns a throttler that able to limit total amounts of goroutines from
// doing the same thing at the same time.
func MakeThrottler(timeExpiry time.Duration) *Throttler {
	tr := &Throttler{
		requestChan:    make(chan *request),
		locks:          make(map[string]*actionLock),
		lockTimeExpiry: timeExpiry,
	}
	go tr.service()
	go tr.expiryService()
	return tr
}

func (tr *Throttler) service() {
	for {
		req := <-tr.requestChan

		switch req.requestType {
		case GET:
			lock, ok := tr.locks[req.resourceKey]
			if ok && !lock.isOld() {
				req.responseChan <- &response{
					lock: lock, firstGoroutine: false,
				}
				continue
			} else if ok && lock.isOld() {
				// in case that one goroutine occupy the update lock for long time
				lock.wg.Done()
			}

			lock = &actionLock{
				wg:         &sync.WaitGroup{},
				ctimestamp: time.Now(),
				timeExpiry: tr.lockTimeExpiry,
			}

			lock.wg.Add(1)

			tr.locks[req.resourceKey] = lock

			req.responseChan <- &response{
				lock: lock, firstGoroutine: true,
			}

		case DELETE:
			lock, ok := tr.locks[req.resourceKey]
			if ok {
				lock.wg.Done()
				delete(tr.locks, req.resourceKey)
			}

		case EXPIRE:
			for k, v := range tr.locks {
				if v.isOld() {
					delete(tr.locks, k)
					v.wg.Done()
				}
			}
		}
	}
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
// 	   var u *url.URL
//
//	   if firstToTheLock { // first to the service url
//		   // Call to backend services then do something else.
//		   // For example, get service url from executor then update router cache.
//	   } else {
//		   // Do something here.
//		   // For example, get service url from cache
//	   }
//
//	   return AnythingYouWant{}, error
//   }

func (tr *Throttler) RunOnce(resourceKey string,
	callbackFunc func(bool) (interface{}, error)) (interface{}, error) {

	ch := make(chan *response)
	tr.requestChan <- &request{
		requestType:  GET,
		responseChan: ch,
		resourceKey:  resourceKey,
	}
	resp := <-ch

	// if we are not the first one, wait for the first goroutine to finish its task
	if !resp.firstGoroutine {
		err := resp.lock.wait()
		if err != nil {
			return nil, err
		}
	}

	// release actionLock so that other goroutines can take over the responsibility if failed.
	defer func() {
		go func() {
			tr.requestChan <- &request{
				requestType: DELETE,
				resourceKey: resourceKey,
			}
		}()
	}()

	return callbackFunc(resp.firstGoroutine)
}

// expiryService periodically expires time-out locks.
// Normally, we don't need to do this just in case any of goroutine didn't release lock.
func (tr *Throttler) expiryService() {
	for {
		time.Sleep(time.Minute)
		tr.requestChan <- &request{
			requestType: EXPIRE,
		}
	}
}
