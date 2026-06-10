// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// Executor defines a fission function executor.
	Executor struct {
		logger logr.Logger

		executorTypes map[fv1.ExecutorType]executortype.ExecutorType

		fissionClient versioned.Interface

		requestChan chan *createFuncServiceRequest
		fsCreateWg  sync.Map

		// Readiness state. /readyz reports ready only when this process is the
		// leader (or leader election is disabled) AND informer caches have
		// synced, so non-leaders are kept out of the Service endpoints and a
		// just-elected leader is not advertised before its caches are warm.
		// isLeader is set by the leader-only controllers runnable under the
		// controller-runtime Manager.
		leaderElection bool
		isLeader       atomic.Bool
		cachesSynced   atomic.Bool
	}
	createFuncServiceRequest struct {
		context  context.Context
		function *fv1.Function
		respChan chan *createFuncServiceResponse
	}

	createFuncServiceResponse struct {
		funcSvc *fscache.FuncSvc
		err     error
	}
)

// MakeExecutor returns an Executor for the given ExecutorType(s). It only builds
// the object; the mutating controllers are started by executorControllers (a
// leader-only runnable) under the controller-runtime Manager.
func MakeExecutor(logger logr.Logger,
	fissionClient versioned.Interface, types map[fv1.ExecutorType]executortype.ExecutorType) *Executor {
	return &Executor{
		logger:        logger.WithName("executor"),
		fissionClient: fissionClient,
		executorTypes: types,

		requestChan: make(chan *createFuncServiceRequest),
	}
}

// withSpecializationTimeout returns a context bounded by the function's
// specialization timeout plus a small buffer. The reason not to use the
// router request's deadline directly is that a request may be canceled for
// unknown reasons, and the executor would keep spawning pods that never finish
// the specialization process; also, even when a request fails, a specialized
// function pod can still serve other subsequent requests.
func withSpecializationTimeout(ctx context.Context, fn *fv1.Function) (context.Context, context.CancelFunc) {
	buffer := 10 // add some buffer time for specialization
	specializationTimeout := max(
		// set minimum specialization timeout to avoid illegal input and
		// compatibility problem when applying old spec file that doesn't
		// have specialization timeout field.
		fn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout, fv1.DefaultSpecializationTimeOut)

	return context.WithTimeoutCause(ctx,
		time.Duration(specializationTimeout+buffer)*time.Second,
		fmt.Errorf("function specialization timeout (%d)s exceeded", specializationTimeout+buffer))
}

// All non-cached function service requests go through this goroutine
// serially. It parallelizes requests for different functions, and
// ensures that for a given function, only one request causes a pod to
// get specialized. In other words, it ensures that when there's an
// ongoing request for a certain function, all other requests wait for
// that request to complete.
func (executor *Executor) serveCreateFuncServices(ctx context.Context) {
	for {
		var req *createFuncServiceRequest
		select {
		case <-ctx.Done():
			return
		case req = <-executor.requestChan:
		}
		function := req.function
		fnName := k8sCache.MetaObjectToName(function)
		fnkeyUR := crd.CacheKeyURFromObject(function)

		if req.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
			go func() {
				fnSpecializationTimeoutContext, cancel := withSpecializationTimeout(req.context, req.function)
				defer cancel()

				fsvc, err := executor.createServiceForFunction(fnSpecializationTimeoutContext, req.function)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
			}()
			continue
		}

		// Cache miss -- is this first one to request the func?
		wg, found := executor.fsCreateWg.Load(fnkeyUR)
		if !found {
			// create a waitgroup for other requests for
			// the same function to wait on
			wg := &sync.WaitGroup{}
			executor.fsCreateWg.Store(fnkeyUR, wg)

			// launch a goroutine for each request, to parallelize
			// the specialization of different functions
			wg.Go(func() {
				// Control overall specialization time by setting function
				// specialization time to context (see withSpecializationTimeout).
				fnSpecializationTimeoutContext, cancel := withSpecializationTimeout(req.context, req.function)
				defer cancel()

				fsvc, err := executor.createServiceForFunction(fnSpecializationTimeoutContext, req.function)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
				executor.fsCreateWg.Delete(fnkeyUR)
			})
		} else {
			// There's an existing request for this function, wait for it to finish
			go func() {
				executor.logger.V(1).Info("waiting for concurrent request for the same function",
					"function", fnName.String())
				wg, ok := wg.(*sync.WaitGroup)
				if !ok {
					err := fmt.Errorf("could not convert value to workgroup for function %s", fnName)
					req.respChan <- &createFuncServiceResponse{
						funcSvc: nil,
						err:     err,
					}
				}
				wg.Wait()

				// get the function service from the cache
				fsvc, err := executor.getFunctionServiceFromCache(req.context, req.function)

				// fsCache return error when the entry does not exist/expire.
				// It normally happened if there are multiple requests are
				// waiting for the same function and executor failed to cre-
				// ate service for function.
				err = fmt.Errorf("error getting service for function %s: %w", fnName, err)
				req.respChan <- &createFuncServiceResponse{
					funcSvc: fsvc,
					err:     err,
				}
			}()
		}
	}
}

func (executor *Executor) createServiceForFunction(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, executor.logger)
	otelUtils.SpanTrackEvent(ctx, "createServiceForFunction", otelUtils.GetAttributesForFunction(fn)...)
	logger.V(1).Info("no cached function service found, creating one",
		"function_name", fn.Name,
		"function_namespace", fn.Namespace)

	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, fmt.Errorf("unknown executor type '%s'", t)
	}

	fsvc, fsvcErr := e.GetFuncSvc(ctx, fn)
	if fsvcErr != nil {
		e := "error creating service for function"
		logger.Error(fsvcErr, e, "function_name", fn.Name,
			"function_namespace", fn.Namespace)
		fsvcErr = fmt.Errorf("[%s] %s: %w", fn.Name, e, fsvcErr)
	}

	return fsvc, fsvcErr
}

func (executor *Executor) getFunctionServiceFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "getFunctionServiceFromCache", otelUtils.GetAttributesForFunction(fn)...)
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	e, ok := executor.executorTypes[t]
	if !ok {
		return nil, fmt.Errorf("unknown executor type '%s'", t)
	}
	return e.GetFuncSvcFromCache(ctx, fn)
}
