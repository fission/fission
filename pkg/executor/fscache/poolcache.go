// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
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
		deleted    bool
	}

	// PoolCache implements a simple cache implementation having values mapped by two keys [function][address].
	// As of now PoolCache is only used by poolmanager executor.
	//
	// All operations are serialized by lock: it replaces an earlier
	// single-goroutine "actor" (an unbuffered requestChannel drained by one
	// service() loop), so the mutual exclusion is identical — every method runs
	// to completion before another can start — without the per-cache goroutine
	// or the channel round-trip on the tap/specialization hot path.
	PoolCache struct {
		lock   sync.Mutex
		cache  map[crd.CacheKeyUG]*funcSvcGroup
		logger logr.Logger
	}

	svcWait struct {
		svcChannel chan *FuncSvc
		ctx        context.Context
	}
)

// NewPoolCache create a Cache object
func NewPoolCache(logger logr.Logger) *PoolCache {
	return &PoolCache{
		cache:  make(map[crd.CacheKeyUG]*funcSvcGroup),
		logger: logger,
	}
}

func NewFuncSvcGroup() *funcSvcGroup {
	return &funcSvcGroup{
		svcs:  make(map[string]*funcSvcInfo),
		queue: NewQueue(),
	}
}

func (c *PoolCache) MarkFuncDeleted(function crd.CacheKeyUG) {
	c.lock.Lock()
	defer c.lock.Unlock()

	for key := range c.cache {
		if key.UID == function.UID {
			c.cache[key].deleted = true
			break
		}
	}
}

// getSvcValue runs the cache-side of GetSvcValue under the lock. When the
// request is parked for capacity it returns a non-nil svcWait; the caller waits
// on its channel *after* the lock is released (the matching setValue send must
// be able to acquire the lock).
func (c *PoolCache) getSvcValue(ctx context.Context, function crd.CacheKeyUG, requestsPerPod int, concurrency int) (*FuncSvc, *svcWait, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	funcSvcGroup, ok := c.cache[function]
	if !ok {
		// first request for this function, create a new group
		c.cache[function] = NewFuncSvcGroup()
		c.cache[function].svcWaiting++
		return nil, nil, ferror.MakeError(ferror.ErrorNotFound,
			fmt.Sprintf("function Name '%s' not found", function))
	}
	totalActiveRequests := 0
	// check if any specialized pod is available
	for addr := range funcSvcGroup.svcs {
		totalActiveRequests += funcSvcGroup.svcs[addr].activeRequests
		if funcSvcGroup.svcs[addr].activeRequests < requestsPerPod &&
			funcSvcGroup.svcs[addr].currentCPUUsage.Cmp(funcSvcGroup.svcs[addr].cpuLimit) < 1 {
			// mark active
			funcSvcGroup.svcs[addr].activeRequests++
			if c.logger.V(1).Enabled() {
				otelUtils.LoggerWithTraceID(ctx, c.logger).V(1).Info("Increase active requests with getValue", "function", function.String(), "address", addr, "activeRequests", funcSvcGroup.svcs[addr].activeRequests)
			}
			return funcSvcGroup.svcs[addr].val, nil, nil
		}
	}
	concurrencyUsed := len(funcSvcGroup.svcs) + (funcSvcGroup.svcWaiting - funcSvcGroup.queue.Len())
	// if concurrency is available then be aggressive and use it as we are not sure if specialization will complete for other requests
	if concurrency > 0 && concurrencyUsed < concurrency {
		funcSvcGroup.svcWaiting++
		return nil, nil, ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%s' not found", function))
	}
	// if no concurrency is available then check if there is any virtual capacity in the existing pods to serve the request in future
	// if specialization doesnt complete within request then request will be timeout
	capacity := (concurrencyUsed * requestsPerPod) - (totalActiveRequests + funcSvcGroup.svcWaiting)
	if capacity > 0 {
		funcSvcGroup.svcWaiting++
		svcWait := &svcWait{
			svcChannel: make(chan *FuncSvc),
			ctx:        ctx,
		}
		funcSvcGroup.queue.Push(svcWait)
		return nil, svcWait, nil
	}

	// concurrency should not be set to zero and
	// sum of specialization in progress and specialized pods should be less then req.concurrency
	if concurrency > 0 && concurrencyUsed >= concurrency {
		return nil, nil, ferror.MakeError(ferror.ErrorTooManyRequests, fmt.Sprintf("function '%s' concurrency '%d' limit reached.", function, concurrency))
	}
	funcSvcGroup.svcWaiting++
	return nil, nil, ferror.MakeError(ferror.ErrorNotFound, fmt.Sprintf("function '%s' all functions are busy", function))
}

// GetSvcValue returns a function service with status in Active else return error
func (c *PoolCache) GetSvcValue(ctx context.Context, function crd.CacheKeyUG, requestsPerPod int, concurrency int) (*FuncSvc, error) {
	value, svcWaitValue, err := c.getSvcValue(ctx, function, requestsPerPod, concurrency)
	if svcWaitValue != nil {
		select {
		case <-ctx.Done():
			return value, ctx.Err()
		case funcSvc := <-svcWaitValue.svcChannel:
			return funcSvc, nil
		}
	}
	return value, err
}

// ReserveCapacity atomically checks the function's concurrency cap against
// its specialized pod count plus in-flight specializations and reserves one
// in-flight specialization slot, or returns a TooManyRequests ferror at the
// cap (RFC-0002 ensureCapacity). The reservation must be released exactly
// once: by setValue on a successful specialization, or
// MarkSpecializationFailure on a failed one.
//
// The check-and-reserve runs under the lock exactly like the GetSvcValue arm,
// so concurrent capacity requests cannot race past the function's concurrency
// cap. The svcWaiting reservation is symmetric with GetSvcValue's: a
// successful specialization decrements it in SetSvcValue, a failed one in
// MarkSpecializationFailure.
// maxPending additionally bounds specializations in flight at once,
// independently of the concurrency cap (a total-pods budget); 0 disables.
// Exceeding either bound returns ErrorTooManyRequests, which the router
// relays to the client as a fast 429. Sizing rationale lives on
// poolmgr.defaultMaxPendingSpecializations.
func (c *PoolCache) ReserveCapacity(function crd.CacheKeyUG, concurrency, maxPending int) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	grp, ok := c.cache[function]
	if !ok {
		grp = NewFuncSvcGroup()
		c.cache[function] = grp
	}
	pending := grp.svcWaiting - grp.queue.Len()
	concurrencyUsed := len(grp.svcs) + pending
	if concurrency > 0 && concurrencyUsed >= concurrency {
		return ferror.MakeError(ferror.ErrorTooManyRequests, fmt.Sprintf("function '%s' concurrency '%d' limit reached.", function, concurrency))
	}
	if maxPending > 0 && pending >= maxPending {
		return ferror.MakeError(ferror.ErrorTooManyRequests, fmt.Sprintf("function '%s' already has %d specializations in flight (cap %d).", function, pending, maxPending))
	}
	grp.svcWaiting++
	return nil
}

// TouchByAddress refreshes the Atime of pool-cache entries serving the given
// address (the router's batched tap path for poolmgr; RFC-0002). The router's
// batched taps are poolmgr's only liveness signal once the warm path stops
// calling the executor per request, and the idle reaper ages on exactly this
// Atime (ListAvailableValue).
func (c *PoolCache) TouchByAddress(address string) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	found := false
	for _, grp := range c.cache {
		if info, ok := grp.svcs[address]; ok {
			info.val.Atime = time.Now()
			found = true
		}
	}
	if !found {
		return ferror.MakeError(ferror.ErrorNotFound,
			fmt.Sprintf("address %s not found in pool cache", address))
	}
	return nil
}

// ListAvailableValue returns a list of the available function services stored in the Cache
func (c *PoolCache) ListAvailableValue() []*FuncSvc {
	c.lock.Lock()
	defer c.lock.Unlock()

	vals := make([]*FuncSvc, 0)
	latestFuncGen := make(map[types.UID]int64, len(c.cache))

	// find the latest generation of each function
	for key := range c.cache {
		if currentFuncGen, ok := latestFuncGen[key.UID]; ok {
			if key.Generation > currentFuncGen {
				latestFuncGen[key.UID] = key.Generation
			}
		} else {
			latestFuncGen[key.UID] = key.Generation
		}
	}

	for key1, values := range c.cache {
		svcRetain := values.svcRetain
		// if the function is not latest generation, then we don't need to retain any pods
		if latestFuncGen[key1.UID] != key1.Generation || values.deleted {
			svcRetain = 0
		}
		svcCleanQuota := len(values.svcs) - svcRetain
		if svcCleanQuota <= 0 {
			continue
		}
		for key2, value := range values.svcs {
			debugLevel := c.logger.V(1).Enabled()
			if debugLevel {
				otelUtils.LoggerWithTraceID(context.Background(), c.logger).V(1).Info("Reading active requests", "function", key1.String(), "address", key2, "activeRequests", value.activeRequests)
			}
			if value.activeRequests == 0 && svcCleanQuota > 0 {
				if debugLevel {
					otelUtils.LoggerWithTraceID(context.Background(), c.logger).V(1).Info("Function service with no active requests", "function", key1.String(), "address", key2, "activeRequests", value.activeRequests)
				}
				vals = append(vals, value.val)
				svcCleanQuota--
			}
		}
	}
	return vals
}

// SetSvcValue marks the value at key [function][address] as active(begin used)
func (c *PoolCache) SetSvcValue(ctx context.Context, function crd.CacheKeyUG, address string, value *FuncSvc, cpuLimit resource.Quantity, requestsPerPod, svcsRetain int) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.cache[function]; !ok {
		c.cache[function] = NewFuncSvcGroup()
	}
	if _, ok := c.cache[function].svcs[address]; !ok {
		c.cache[function].svcs[address] = &funcSvcInfo{}
	}
	c.cache[function].svcRetain = svcsRetain
	c.cache[function].svcs[address].val = value
	c.cache[function].svcs[address].activeRequests++
	if c.cache[function].svcWaiting > 0 {
		c.cache[function].svcWaiting--
		svcCapacity := requestsPerPod - c.cache[function].svcs[address].activeRequests
		queueLen := c.cache[function].queue.Len()
		if svcCapacity > queueLen {
			svcCapacity = queueLen
		}
		for i := 0; i <= svcCapacity; {
			popped := c.cache[function].queue.Pop()
			if popped == nil {
				break
			}
			if popped.ctx.Err() == nil {
				// Safe to send under c.lock: every queued waiter was pushed by
				// a prior getSvcValue call that has already returned (and so
				// released the lock) before its caller reaches the receive in
				// GetSvcValue. No in-flight getSvcValue holds the lock when its
				// svcWait becomes poppable, so this unbuffered send cannot
				// deadlock against the receiver. Do not move it out of the
				// lock — that decouples activeRequests/svcWaiting accounting
				// from the hand-off and reintroduces the race serialization
				// prevents.
				popped.svcChannel <- value
				c.cache[function].svcs[address].activeRequests++
				i++
			}
			close(popped.svcChannel)
			c.cache[function].svcWaiting--
		}
	}
	if c.logger.V(1).Enabled() {
		otelUtils.LoggerWithTraceID(ctx, c.logger).V(1).Info("SetSvcValue",
			"function", function.String(),
			"address", address,
			"uid", function.UID,
			"resourceVersion", function.ResourceVersion,
			"generation", function.Generation,
			"activeRequests", c.cache[function].svcs[address].activeRequests)
	}
	c.cache[function].svcs[address].cpuLimit = cpuLimit
}

// SetCPUUtilization updates/sets the CPU utilization limit for the pod
func (c *PoolCache) SetCPUUtilization(function crd.CacheKeyUG, address string, cpuUsage resource.Quantity) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.cache[function]; !ok {
		c.cache[function] = NewFuncSvcGroup()
	}
	if _, ok := c.cache[function].svcs[address]; ok {
		c.cache[function].svcs[address].currentCPUUsage = cpuUsage
	}
}

// MarkAvailable marks the value at key [function][address] as available
func (c *PoolCache) MarkAvailable(function crd.CacheKeyUG, address string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	funcSvcGroup, ok := c.cache[function]
	if !ok {
		if c.logger.V(1).Enabled() {
			otelUtils.LoggerWithTraceID(context.Background(), c.logger).V(1).Info("MarkAvailable function miss",
				"function", function.String(),
				"address", address,
				"uid", function.UID,
				"resourceVersion", function.ResourceVersion,
				"generation", function.Generation)
		}
		return
	}
	svcInfo, ok := funcSvcGroup.svcs[address]
	if !ok {
		if c.logger.V(1).Enabled() {
			otelUtils.LoggerWithTraceID(context.Background(), c.logger).V(1).Info("MarkAvailable address miss",
				"function", function.String(),
				"address", address,
				"uid", function.UID,
				"resourceVersion", function.ResourceVersion,
				"generation", function.Generation)
		}
		return
	}
	if svcInfo.activeRequests > 0 {
		svcInfo.activeRequests--
		if c.logger.V(1).Enabled() {
			otelUtils.LoggerWithTraceID(context.Background(), c.logger).V(1).Info("Decrease active requests", "function", function.String(), "address", address, "activeRequests", svcInfo.activeRequests)
		}
	} else {
		otelUtils.LoggerWithTraceID(context.Background(), c.logger).Error(nil, "Invalid request to decrease active requests", "function", function.String(), "address", address, "activeRequests", svcInfo.activeRequests)
	}
}

// DeleteValue deletes the value at key composed of [function][address]
func (c *PoolCache) DeleteValue(ctx context.Context, function crd.CacheKeyUG, address string) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if funcSvcGroup, ok := c.cache[function]; ok {
		delete(c.cache[function].svcs, address)
		if funcSvcGroup.deleted && len(c.cache[function].svcs) == 0 {
			delete(c.cache, function)
		}
	}
	return nil
}

// MarkSpecializationFailure reduces the svcWaiting count
func (c *PoolCache) MarkSpecializationFailure(function crd.CacheKeyUG) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Guarded lookup: the ensureCapacity path can fail before the function ever
	// had a GetSvcValue (which is what historically created the group) — an
	// unguarded index here nil-panics and takes the whole executor down.
	if grp, ok := c.cache[function]; ok {
		if grp.svcWaiting > grp.queue.Len() {
			grp.svcWaiting--
			if grp.svcWaiting == grp.queue.Len() {
				expiredRequests := grp.queue.Expired()
				grp.svcWaiting = grp.svcWaiting - expiredRequests
			}
		}
	}
}

func (c *PoolCache) LogFnSvcGroup(ctx context.Context, file io.Writer) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	datawriter := bufio.NewWriter(file)

	writefnSvcGrp := func(svcGrp *funcSvcGroup) error {
		_, err := fmt.Fprintf(datawriter, "svc_waiting:%d\tqueue_len:%d", svcGrp.svcWaiting, svcGrp.queue.Len())
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
			_, err := fmt.Fprintf(datawriter, "\tfunction_name:%s\tfn_svc_address:%s\tactive_req:%d\tcurrent_cpu_usage:%v\tcpu_limit:%v\n",
				fnSvc.val.Function.Name, addr, fnSvc.activeRequests, fnSvc.currentCPUUsage, fnSvc.cpuLimit)
			if err != nil {
				return err
			}
		}
		return nil
	}

	var err error
	for _, fnSvcGrp := range c.cache {
		if e := writefnSvcGrp(fnSvcGrp); e != nil {
			err = e
			break
		}
	}
	if e := datawriter.Flush(); e != nil {
		err = errors.Join(err, e)
	}
	return err
}
