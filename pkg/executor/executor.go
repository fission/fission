// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/dispatch"
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

		// dispatcher runs specializations: per-function dedup with ctx-aware
		// waiters for newdeploy/container, independent (optionally bounded)
		// runs for poolmgr. Replaces the request-channel multiplexer.
		dispatcher *dispatch.Dispatcher[*fscache.FuncSvc]

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
)

// MakeExecutor returns an Executor for the given ExecutorType(s). It only builds
// the object; the mutating controllers are started by executorControllers (a
// leader-only runnable) under the controller-runtime Manager.
// specializationConcurrency bounds concurrently running specializations
// (EXECUTOR_SPECIALIZATION_CONCURRENCY); 0 keeps the historical unbounded
// behavior.
func MakeExecutor(logger logr.Logger,
	fissionClient versioned.Interface, types map[fv1.ExecutorType]executortype.ExecutorType,
	specializationConcurrency int) *Executor {
	l := logger.WithName("executor")
	return &Executor{
		logger:        l,
		fissionClient: fissionClient,
		executorTypes: types,

		dispatcher: dispatch.New[*fscache.FuncSvc](l, specializationConcurrency),
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

// dispatchCreateFuncService runs createServiceForFunction through the
// dispatcher: poolmgr requests each specialize their own pod (concurrent
// cache misses scale out by design), every other type deduplicates per
// function so concurrent requests share one specialization — with waiters
// honoring their own context (replacing the sync.WaitGroup wait that could
// not be canceled).
//
// Cancellation semantics differ deliberately per path:
//   - Deduplicated types (newdeploy/container) run the creation on a context
//     detached from the creator's cancellation (values kept, bounded by the
//     specialization timeout): one creation serves every coalesced waiter, so
//     the dedup bounds the detached work to one in-flight per function, and
//     without the detach a single canceled creator poisons all waiters.
//   - Poolmgr keeps the caller's context as the parent (main's behavior):
//     DoEach has no dedup, so a detached creation per request would let a
//     retry storm against a slow/cold function pile up zombie specializations
//     that keep claiming warm pods long after their callers gave up — caller
//     cancellation IS poolmgr's load shedding.
func (executor *Executor) dispatchCreateFuncService(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	createFrom := func(parent context.Context) (*fscache.FuncSvc, error) {
		fnSpecializationTimeoutContext, cancel := withSpecializationTimeout(parent, fn)
		defer cancel()
		return executor.createServiceForFunction(fnSpecializationTimeoutContext, fn)
	}
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		return executor.dispatcher.DoEach(ctx, func(cctx context.Context) (*fscache.FuncSvc, error) {
			return createFrom(cctx)
		})
	}
	// Dedup key is UID+Generation, not ResourceVersion (see #3596): keying
	// on RV here (which the router-side migration alone would not fix)
	// would reintroduce status-churn duplication on the executor side —
	// two concurrent createServiceForFunction callers that observed
	// different RVs of the same spec would each get their own dedup
	// bucket instead of coalescing onto one specialization.
	return executor.dispatcher.Do(ctx, crd.CacheKeyUGFromMeta(&fn.ObjectMeta).String(), func(cctx context.Context) (*fscache.FuncSvc, error) {
		return createFrom(context.WithoutCancel(cctx))
	})
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
