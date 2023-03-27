package throttler

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

var (
	cntErrNotFound        = atomic.Int32{}
	cntErrTooManyRequests = atomic.Int32{}
	cntFirstRun           = atomic.Int32{}
)

const fn = "test-fn"
const (
	numWorkers           = 10
	numRequestsPerWorker = 5
	maxActiveRequests    = 10
	maxConcurrency       = 4
)

func TestThrottler_RunOnce(t *testing.T) {
	throttler := MakeThrottler(time.Minute)

	run := func(workerID int) {
		traceId := uuid.New().String()
		pod, err := throttler.RunOnceStrict(fn, func(isFirstRun bool) (interface{}, error) {
			if isFirstRun {
				cntFirstRun.Add(1)
				pod, err := _getPodFromCache(fn)
				fmt.Printf("isFirstRun, traceId: %s, err: %s\n", traceId, err)
				if err != notFoundErr {
					return pod, err
				}

				v, err := _newPod(fn)
				fmt.Println("newPod, traceId", traceId)
				return v, err
			}
			return _getPodFromCache(fn)
		})
		log.Printf("worker %d: get pod %s err %v", workerID, pod, err)
		switch err {
		case tooManyRequestErr:
			cntErrTooManyRequests.Add(1)
		case notFoundErr:
			cntErrNotFound.Add(1)
		}
	}

	wg := sync.WaitGroup{}
	workerFn := func(workerID int, n int) {
		for i := 0; i < n; i++ {
			run(workerID)
		}
		wg.Done()
	}

	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go workerFn(i, numRequestsPerWorker)
	}

	wg.Wait()
	log.Println(cache[fn])
	log.Println("#too many requests: ", cntErrTooManyRequests.Load())
	log.Println("#not found: ", cntErrNotFound.Load())
	log.Println("#first run: ", cntFirstRun.Load())
}

var tooManyRequestErr = errors.New("too many requests")
var notFoundErr = errors.New("not found")

var cache = make(map[string]map[string]int)
var cacheMutex = sync.Mutex{}

func _getPodFromCache(fn string) (string, error) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	pods, ok := cache[fn]
	if !ok {
		return "", notFoundErr
	}

	for pod, ac := range pods {
		if ac < maxActiveRequests {
			pods[pod] += 1
			return pod, nil
		}
	}

	if len(pods) >= maxConcurrency {
		return "", tooManyRequestErr
	}
	return "", notFoundErr
}

func _newPod(fn string) (string, error) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	pods, ok := cache[fn]
	if !ok {
		cache[fn] = make(map[string]int)
		pods = cache[fn]
	}

	// if len(pods) >= maxConcurrency {
	//	return "", tooManyRequestErr
	// }
	p := _pod()
	pods[p] = 1
	return p, nil
}

var cnt atomic.Int32

func _pod() string {
	time.Sleep(100 * time.Millisecond)
	cnt.Add(1)
	return fmt.Sprintf("pod-%d", cnt.Load())
}
