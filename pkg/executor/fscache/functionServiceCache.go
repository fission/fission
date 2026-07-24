// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/executor/util"
)

type (
	// FuncSvc represents a function service
	FuncSvc struct {
		Name              string                  // Name of object
		Function          *metav1.ObjectMeta      // function this pod/service is for
		Environment       *fv1.Environment        // function's environment
		Address           string                  // Host:Port or IP:Port that the function's service can be reached at.
		KubernetesObjects []apiv1.ObjectReference // Kubernetes Objects (within the function namespace)
		Executor          fv1.ExecutorType
		CPULimit          resource.Quantity

		Ctime time.Time
		Atime time.Time
	}

	// FunctionServiceCache represents the function service cache
	FunctionServiceCache struct {
		logger            logr.Logger
		byFunction        *cache.Cache[crd.CacheKeyUG, *FuncSvc]
		byAddress         *cache.Cache[string, metav1.ObjectMeta]
		byFunctionUID     *cache.Cache[types.UID, metav1.ObjectMeta]
		connFunctionCache *PoolCache // function-key -> funcSvc : map[string]*funcSvc
		PodToFsvc         sync.Map   // pod-name -> funcSvc: map[string]*FuncSvc
		WebsocketFsvc     sync.Map   // funcSvc-name -> bool: map[string]bool
		// lock serializes the composite read/modify operations that the
		// removed single-goroutine actor used to mutually exclude: the
		// _touchByAddress Atime write against the ListOld/ListOldForPool/Log
		// scans. The per-key stores (byFunction/byAddress/byFunctionUID and
		// connFunctionCache) are independently synchronized.
		//
		// Boundary note: GetByFunction/GetFuncSvc/GetByFunctionUID/AddFunc
		// also refresh fsvc.Atime, but WITHOUT this lock. That race predates
		// the actor's removal (the actor never covered those paths either) and
		// is intentionally left out of scope here; a new Atime writer that
		// needs to be serialized against the scans must take this lock.
		lock sync.Mutex
	}
)

// IsNotFoundError checks if err is ErrorNotFound.
func IsNotFoundError(err error) bool {
	if fe, ok := err.(ferror.Error); ok {
		return fe.Code == ferror.ErrorNotFound
	}
	return false
}

// IsNameExistError checks if err is ErrorNameExists.
func IsNameExistError(err error) bool {
	if fe, ok := err.(ferror.Error); ok {
		return fe.Code == ferror.ErrorNameExists
	}
	return false
}

// MakeFunctionServiceCache starts and returns an instance of FunctionServiceCache.
func MakeFunctionServiceCache(logger logr.Logger) *FunctionServiceCache {
	return &FunctionServiceCache{
		logger:            logger.WithName("function_service_cache"),
		byFunction:        cache.MakeCache[crd.CacheKeyUG, *FuncSvc](0, 0),
		byAddress:         cache.MakeCache[string, metav1.ObjectMeta](0, 0),
		byFunctionUID:     cache.MakeCache[types.UID, metav1.ObjectMeta](0, 0),
		connFunctionCache: NewPoolCache(logger.WithName("conn_function_cache")),
	}
}

// DumpDebugInfo => dump function service cache data to temporary directory of executor pod.
func (fsc *FunctionServiceCache) DumpDebugInfo(ctx context.Context) error {
	fsc.logger.Info("dumping function service")

	file, err := util.CreateDumpFile(fsc.logger)
	if err != nil {
		fsc.logger.Error(err, "error while creating file/dir", "error", err.Error())
		return err
	}
	defer file.Close()

	err = fsc.connFunctionCache.LogFnSvcGroup(ctx, file)
	if err != nil {
		fsc.logger.Error(nil, "error while logging function service group", "error", err.Error())
		return err
	}

	fsc.logger.Info("dumped function service")
	return nil
}

// GetByFunction gets a function service from cache using function key.
//
// The key is UID+Generation, not ResourceVersion (see #3596): RV moves on
// status-only writes (not just spec changes), and a caller's view of the
// function can lag the writer's (informer cache lag across components) —
// an RV-keyed lookup would miss the entry Add()/DeleteEntry() operate on,
// splitting one function's cache entry across RV churn.
func (fsc *FunctionServiceCache) GetByFunction(m *metav1.ObjectMeta) (*FuncSvc, error) {
	key := crd.CacheKeyUGFromMeta(m)

	fsvc, err := fsc.byFunction.Get(key)
	if err != nil {
		return nil, err
	}

	// update atime
	fsvc.Atime = time.Now()

	fsvcCopy := *fsvc
	return &fsvcCopy, nil
}

// ReserveCapacity atomically checks the function's concurrency cap and
// reserves one in-flight specialization in the pool cache (RFC-0002
// ensureCapacity); returns a TooManyRequests ferror at the cap.
func (fsc *FunctionServiceCache) ReserveCapacity(key crd.CacheKeyUG, concurrency, maxPending int) error {
	return fsc.connFunctionCache.ReserveCapacity(key, concurrency, maxPending)
}

// GetFuncSvc gets a function service from pool cache using function key and returns number of active instances of function pod
func (fsc *FunctionServiceCache) GetFuncSvc(ctx context.Context, m *metav1.ObjectMeta, requestsPerPod int, concurrency int) (*FuncSvc, error) {
	key := crd.CacheKeyUGFromMeta(m)

	fsvc, err := fsc.connFunctionCache.GetSvcValue(ctx, key, requestsPerPod, concurrency)
	if err != nil {
		fsc.logger.Info("Not found in Cache")
		return nil, err
	}

	// update atime
	fsvc.Atime = time.Now()

	fsvcCopy := *fsvc
	return &fsvcCopy, nil
}

// GetByFunctionUID gets a function service from cache using function UUID.
func (fsc *FunctionServiceCache) GetByFunctionUID(uid types.UID) (*FuncSvc, error) {
	m, err := fsc.byFunctionUID.Get(uid)
	if err != nil {
		return nil, err
	}

	fsvc, err := fsc.byFunction.Get(crd.CacheKeyUGFromMeta(&m))
	if err != nil {
		return nil, err
	}

	// update atime
	fsvc.Atime = time.Now()

	fsvcCopy := *fsvc
	return &fsvcCopy, nil
}

// AddFunc adds a function service to pool cache.
func (fsc *FunctionServiceCache) AddFunc(ctx context.Context, fsvc FuncSvc, requestsPerPod, svcsRetain int) {
	fsc.connFunctionCache.SetSvcValue(ctx, crd.CacheKeyUGFromMeta(fsvc.Function), fsvc.Address, &fsvc, fsvc.CPULimit, requestsPerPod, svcsRetain)
	now := time.Now()
	fsvc.Ctime = now
	fsvc.Atime = now
}

func (fsc *FunctionServiceCache) MarkFuncDeleted(key crd.CacheKeyUG) {
	fsc.connFunctionCache.MarkFuncDeleted(key)
}

// SetCPUUtilizaton updates/sets CPUutilization in the pool cache
func (fsc *FunctionServiceCache) SetCPUUtilizaton(key crd.CacheKeyUG, svcHost string, cpuUsage resource.Quantity) {
	fsc.connFunctionCache.SetCPUUtilization(key, svcHost, cpuUsage)
}

// MarkAvailable marks the value at key [function][address] as available.
func (fsc *FunctionServiceCache) MarkAvailable(key crd.CacheKeyUG, svcHost string) {
	fsc.connFunctionCache.MarkAvailable(key, svcHost)
}

func (fsc *FunctionServiceCache) MarkSpecializationFailure(key crd.CacheKeyUG) {
	fsc.connFunctionCache.MarkSpecializationFailure(key)
}

// Add adds a function service to cache if it does not exist already.
func (fsc *FunctionServiceCache) Add(fsvc FuncSvc) (*FuncSvc, error) {
	existing, err := fsc.byFunction.Set(crd.CacheKeyUGFromMeta(fsvc.Function), &fsvc)
	if err != nil {
		if IsNameExistError(err) {
			err2 := fsc.TouchByAddress(existing.Address)
			if err2 != nil {
				return nil, err2
			}
			fCopy := *existing
			return &fCopy, nil
		}
		return nil, err
	}
	now := time.Now()
	fsvc.Ctime = now
	fsvc.Atime = now

	// Add to byAddress cache. Ignore NameExists errors
	// because of multiple-specialization. See issue #331.
	fsc.byAddress.Upsert(fsvc.Address, *fsvc.Function)

	// Add to byFunctionUID cache. Ignore NameExists errors
	// because of multiple-specialization. See issue #331.
	fsc.byFunctionUID.Upsert(fsvc.Function.UID, *fsvc.Function)

	return nil, nil
}

// TouchByAddress makes a TOUCH request to given address. Addresses unknown to
// the byAddress cache fall back to the pool cache: poolmgr registers its
// specialized pods only there (AddFunc), and with the RFC-0002 warm path the
// router's batched taps are those pods' only liveness signal — without this
// fallback every tap 404s and the idle reaper ages serving pods on their
// specialization time.
func (fsc *FunctionServiceCache) TouchByAddress(address string) error {
	fsc.lock.Lock()
	err := fsc._touchByAddress(address)
	fsc.lock.Unlock()
	if err != nil {
		// Only an unknown address falls through to the pool cache; any other
		// error class must surface rather than be masked by the fallback's
		// own not-found. (The pool cache does its own locking, so the fallback
		// runs without fsc.lock held.)
		if !IsNotFoundError(err) {
			return err
		}
		return fsc.connFunctionCache.TouchByAddress(address)
	}
	return nil
}

func (fsc *FunctionServiceCache) _touchByAddress(address string) error {
	m, err := fsc.byAddress.Get(address)
	if err != nil {
		return err
	}
	fsvc, err := fsc.byFunction.Get(crd.CacheKeyUGFromMeta(&m))
	if err != nil {
		return err
	}
	fsvc.Atime = time.Now()
	return nil
}

// DeleteEntry deletes a function service from cache.
func (fsc *FunctionServiceCache) DeleteEntry(fsvc *FuncSvc) {
	fsc.byFunction.Delete(crd.CacheKeyUGFromMeta(fsvc.Function))
	fsc.byAddress.Delete(fsvc.Address)
	fsc.byFunctionUID.Delete(fsvc.Function.UID)
	metrics.ObserveFunctionRunningSeconds(context.Background(), fsvc.Function.Name, fsvc.Function.Namespace, fsvc.Atime.Sub(fsvc.Ctime).Seconds())
}

// DeleteFunctionSvc deletes a function service at key composed of [function][address].
func (fsc *FunctionServiceCache) DeleteFunctionSvc(ctx context.Context, fsvc *FuncSvc) {
	err := fsc.connFunctionCache.DeleteValue(ctx, crd.CacheKeyUGFromMeta(fsvc.Function), fsvc.Address)
	if err != nil {
		fsc.logger.Error(err,
			"error deleting function service", "function", fsvc.Function.Name,
			"address", fsvc.Address,
		)
	}
	// PodToFsvc and WebsocketFsvc are keyed by pod name (== fsvc.Name) and were
	// never cleaned up, leaking one entry per specialized pod as pods churn.
	// Both poolmgr cleanup paths (idle reaper and specialized-pod cleanup) route
	// through here, so removing them once covers both. Delete is a no-op for
	// executors that never populate these maps.
	fsc.PodToFsvc.Delete(fsvc.Name)
	fsc.WebsocketFsvc.Delete(fsvc.Name)
}

// DeleteOld deletes aged function service entries from cache.
func (fsc *FunctionServiceCache) DeleteOld(fsvc *FuncSvc, minAge time.Duration) (bool, error) {
	if time.Since(fsvc.Atime) < minAge {
		return false, nil
	}

	fsc.DeleteEntry(fsvc)

	return true, nil
}

// DeleteOldPoolCache deletes aged function service entries from pool cache.
func (fsc *FunctionServiceCache) DeleteOldPoolCache(ctx context.Context, fsvc *FuncSvc, minAge time.Duration) (bool, error) {
	if time.Since(fsvc.Atime) < minAge {
		return false, nil
	}

	fsc.DeleteFunctionSvc(ctx, fsvc)

	return true, nil
}

// ListOld returns a list of aged function services in cache.
func (fsc *FunctionServiceCache) ListOld(age time.Duration) ([]*FuncSvc, error) {
	fsc.lock.Lock()
	defer fsc.lock.Unlock()

	fscs := fsc.byFunctionUID.Copy()
	funcObjects := make([]*FuncSvc, 0, len(fscs))
	for _, m := range fscs {
		fsvc, err := fsc.byFunction.Get(crd.CacheKeyUGFromMeta(&m))
		if err != nil {
			// A byFunctionUID entry without a byFunction entry is a transient
			// TOCTOU gap (concurrent delete). Skip it and return what we have;
			// the previous actor implementation aborted the whole service loop
			// here, which silently wedged every future cache request.
			fsc.logger.Error(err, "error while getting service")
			return funcObjects, nil
		}
		if time.Since(fsvc.Atime) > age {
			funcObjects = append(funcObjects, fsvc)
		}
	}
	return funcObjects, nil
}

// ListOldForPool returns a list of aged function services in cache for pooling.
func (fsc *FunctionServiceCache) ListOldForPool(age time.Duration) ([]*FuncSvc, error) {
	fsc.lock.Lock()
	defer fsc.lock.Unlock()

	fscs := fsc.connFunctionCache.ListAvailableValue()
	funcObjects := make([]*FuncSvc, 0, len(fscs))
	for _, fsvc := range fscs {
		if time.Since(fsvc.Atime) > age {
			funcObjects = append(funcObjects, fsvc)
		}
	}
	return funcObjects, nil
}

// Log dumps the function service cache contents.
func (fsc *FunctionServiceCache) Log() {
	fsc.logger.Info("--- FunctionService Cache Contents")
	fsc.logger.Info("dumping function service cache")
	fsc.lock.Lock()
	funcCopy := fsc.byFunction.Copy()
	info := []string{}
	for key, fsvc := range funcCopy {
		for _, kubeObj := range fsvc.KubernetesObjects {
			info = append(info, fmt.Sprintf("%v\t%v\t%v", key, kubeObj.Kind, kubeObj.Name))
		}
	}
	fsc.lock.Unlock()
	fsc.logger.Info("function service cache", "item_count", len(funcCopy), "cache", info)
	fsc.logger.Info("--- FunctionService Cache Contents End")
}

func GetAttributesForFuncSvc(fsvc *FuncSvc) []attribute.KeyValue {
	if fsvc == nil {
		return []attribute.KeyValue{}
	}
	var attrs []attribute.KeyValue
	if fsvc.Function != nil {
		attrs = append(attrs,
			attribute.KeyValue{Key: "function-name", Value: attribute.StringValue(fsvc.Function.Name)},
			attribute.KeyValue{Key: "function-namespace", Value: attribute.StringValue(fsvc.Function.Namespace)})
	}
	if fsvc.Environment != nil {
		attrs = append(attrs,
			attribute.KeyValue{Key: "environment-name", Value: attribute.StringValue(fsvc.Environment.Name)},
			attribute.KeyValue{Key: "environment-namespace", Value: attribute.StringValue(fsvc.Environment.Namespace)})
	}
	if fsvc.Address != "" {
		attrs = append(attrs, attribute.KeyValue{Key: "address", Value: attribute.StringValue(fsvc.Address)})
	}
	return attrs
}
