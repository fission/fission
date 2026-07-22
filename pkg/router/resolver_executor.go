// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/throttler"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// executorResolver is the executor-RPC AddressResolver — today's address
// source, retained as the cold-start path, the strict-mode path, and the
// universal fallback. Poolmgr lookups always RPC the executor (its PoolCache
// does per-request concurrency-aware dispatch); newdeploy/container lookups
// hit the router-side address cache first, coalescing misses through the
// throttler.
type executorResolver struct {
	logger logr.Logger
	fmap   *functionServiceMap
	// reader is the Manager's cache-backed client, used to re-read the current
	// Function before asking the executor to specialize it (the resolved
	// function snapshot can be stale — see fromExecutor).
	reader    client.Reader
	executor  eclient.ClientInterface
	throttler *throttler.Throttler
}

type svcEntryRecord struct {
	svcURL   *url.URL
	cacheHit bool
}

// Resolve implements AddressResolver with the historical getServiceEntry
// semantics. The sticky key is ignored: the executor-RPC data plane has no
// endpoint choice to make (documented — legacy mode does not support
// stickiness).
func (r *executorResolver) Resolve(ctx context.Context, fn *fv1.Function, _ string) (ResolvedEntry, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		svcURL, err := r.fromExecutor(ctx, fn)
		return ResolvedEntry{SvcURL: svcURL}, err
	}
	// Check if service URL present in cache
	svcURL, err := r.fromCache(fn)
	if err == nil && svcURL != nil {
		return ResolvedEntry{SvcURL: svcURL, FromCache: true}, nil
	} else if err != nil {
		return ResolvedEntry{}, err
	}

	fnMeta := &fn.ObjectMeta
	recordObj, err := r.throttler.RunOnce(
		crd.CacheKeyURFromMeta(fnMeta).String(),
		func(firstToTheLock bool) (any, error) {
			if !firstToTheLock {
				svcURL, err := r.fromCache(fn)
				if err != nil {
					return nil, err
				}
				return svcEntryRecord{svcURL: svcURL, cacheHit: true}, err
			}
			svcURL, err = r.fromExecutor(ctx, fn)
			if err != nil {
				return nil, err
			}
			r.fmap.assign(&fn.ObjectMeta, svcURL)
			return svcEntryRecord{
				svcURL:   svcURL,
				cacheHit: false,
			}, nil
		},
	)

	if recordObj == nil {
		return ResolvedEntry{}, fmt.Errorf("empty service entry: %w", err)
	}

	record, ok := recordObj.(svcEntryRecord)
	if !ok {
		return ResolvedEntry{}, fmt.Errorf("unexpected type of recordObj %T: %w", recordObj, err)
	}
	return ResolvedEntry{SvcURL: record.svcURL, FromCache: record.cacheHit}, err
}

// Invalidate removes the function's service url entry from the cache.
func (r *executorResolver) Invalidate(fn *fv1.Function, _ *url.URL, _ InvalidateReason) {
	r.fmap.remove(&fn.ObjectMeta)
}

// fromCache returns the function's service url entry from the cache.
func (r *executorResolver) fromCache(fn *fv1.Function) (serviceUrl *url.URL, err error) {
	// cache lookup to get serviceUrl
	serviceUrl, err = r.fmap.lookup(&fn.ObjectMeta)
	if err != nil {
		var errMsg string

		e, ok := err.(ferror.Error)
		if !ok {
			errMsg = fmt.Sprintf("Unknown error when looking up service entry: %v", err)
		} else {
			// Ignore ErrorNotFound error here, it's an expected error,
			// roundTripper will try to get service url later.
			if e.Code == ferror.ErrorNotFound {
				return nil, nil
			}
			errMsg = fmt.Sprintf("Error getting function %v;s service entry from cache: %v", fn.Name, err)
		}
		return nil, ferror.MakeError(http.StatusInternalServerError, errMsg)
	}
	return serviceUrl, nil
}

// currentFunction re-reads the Function from the Manager cache, falling back
// to the given snapshot. The resolved snapshot can be stale: the reference
// resolver caches the resolved function keyed by the *trigger's*
// ResourceVersion, so a `fission fn update --pkg` (which changes the function
// but not the trigger) doesn't invalidate it. Every path that makes the
// executor specialize — fromExecutor AND the fallback resolver's
// ensureCapacity — must use the current spec, or updated functions keep
// getting pods specialized from the old package.
func (r *executorResolver) currentFunction(ctx context.Context, fn *fv1.Function) *fv1.Function {
	if r.reader == nil {
		return fn
	}
	fresh := &fv1.Function{}
	if gerr := r.reader.Get(ctx, k8stypes.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}, fresh); gerr != nil {
		otelUtils.LoggerWithTraceID(ctx, r.logger).V(1).Info("could not re-read current function; using resolved snapshot",
			"function", fn.Name, "namespace", fn.Namespace, "error", gerr)
		return fn
	}
	return fresh
}

// resolveUncached RPCs the executor for a fresh address — bypassing the
// address cache but still coalescing concurrent callers through the throttler
// (one RPC per function; followers reuse the leader's result) — and updates
// the cache. The fallback resolver's scale-from-zero path uses it: the cached
// Service DNS would dial into a backendless Service, but a thundering herd of
// uncoalesced RPCs is the very thing the throttler exists to prevent.
func (r *executorResolver) resolveUncached(ctx context.Context, fn *fv1.Function) (*url.URL, error) {
	recordObj, err := r.throttler.RunOnce(
		crd.CacheKeyURFromMeta(&fn.ObjectMeta).String(),
		func(firstToTheLock bool) (any, error) {
			if !firstToTheLock {
				svcURL, err := r.fromCache(fn)
				if err != nil {
					return nil, err
				}
				return svcEntryRecord{svcURL: svcURL, cacheHit: true}, err
			}
			svcURL, err := r.fromExecutor(ctx, fn)
			if err != nil {
				return nil, err
			}
			r.fmap.assign(&fn.ObjectMeta, svcURL)
			return svcEntryRecord{svcURL: svcURL, cacheHit: false}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	record, ok := recordObj.(svcEntryRecord)
	if !ok {
		return nil, fmt.Errorf("unexpected type of recordObj %T", recordObj)
	}
	return record.svcURL, nil
}

// fromExecutor asks the executor for the function's service address
// (specializing a pod / scaling up as needed).
func (r *executorResolver) fromExecutor(ctx context.Context, fn *fv1.Function) (serviceUrl *url.URL, err error) {
	logger := otelUtils.LoggerWithTraceID(ctx, r.logger)

	fn = r.currentFunction(ctx, fn)

	// send a request to executor to specialize a new pod
	r.logger.V(1).Info("function timeout specified", "timeout", fn.Spec.FunctionTimeout)

	var fContext context.Context
	if fn.Spec.FunctionTimeout > 0 {
		timeout := time.Second * time.Duration(fn.Spec.FunctionTimeout)
		f, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("function service entry timeout (%f)s exceeded", timeout.Seconds()))
		fContext = f
		defer cancel()
	} else {
		fContext = ctx
	}

	service, err := r.executor.GetServiceForFunction(fContext, fn)
	if err != nil {
		// A canceled context is the caller giving up mid-cold-start (client
		// disconnect / its deadline elsewhere), not an executor failure — log
		// it quietly so client churn during specialization doesn't read as a
		// server error. Genuine failures (executor down, 5xx, the cold-start
		// deadline itself) still surface at Error.
		if errors.Is(err, context.Canceled) {
			logger.V(1).Info("GetServiceForFunction canceled by caller", "function", fn.Name, "namespace", fn.Namespace)
			return nil, err
		}
		statusCode, errMsg := ferror.GetHTTPError(err)
		logger.Error(err, "error from GetServiceForFunction", "error_message", errMsg,
			"function", fn,
			"status_code", statusCode)
		return nil, err
	}
	// parse the address into url
	rawURL := fmt.Sprintf("http://%v", service)
	svcURL, err := url.Parse(rawURL)
	if err != nil {
		// svcURL is nil on a parse error — log the raw string, not svcURL.String().
		logger.Error(err, "error parsing service url", "service_url", rawURL)
		return nil, err
	}
	return svcURL, err
}
