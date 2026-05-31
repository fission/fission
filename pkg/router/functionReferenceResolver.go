// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
)

type (
	// functionReferenceResolver provides a resolver to turn a function
	// reference into a resolveResult
	functionReferenceResolver struct {
		// FunctionReference -> function metadata
		refCache *cache.Cache[namespacedTriggerReference, resolveResult]
		// reader is the Manager's cache-backed client. Function lookups go
		// through it (in-memory cache reads), replacing the per-namespace
		// SharedIndexInformer stores the resolver used before the
		// controller-runtime migration.
		reader client.Reader
		logger logr.Logger
	}

	resolveResultType int

	functionWeightDistribution struct {
		name      string
		weight    int
		sumPrefix int
	}

	// resolveResult is the result of resolving a function reference;
	// it could be the metadata of one function or
	// a distribution of requests across two functions.
	resolveResult struct {
		resolveResultType
		functionMap                map[string]*fv1.Function
		functionWtDistributionList []functionWeightDistribution
	}

	// namespacedTriggerReference is just a trigger reference plus a
	// namespace.
	namespacedTriggerReference struct {
		namespace              string
		triggerName            string
		triggerResourceVersion string
	}
)

const (
	resolveResultSingleFunction = iota
	resolveResultMultipleFunctions
)

func makeFunctionReferenceResolver(logger logr.Logger, reader client.Reader) *functionReferenceResolver {
	frr := &functionReferenceResolver{
		refCache: cache.MakeCache[namespacedTriggerReference, resolveResult](time.Minute, 0),
		reader:   reader,
		logger:   logger.WithName("function_ref_resolver"),
	}
	return frr
}

// resolve translates a trigger's function reference to a resolveResult.
func (frr *functionReferenceResolver) resolve(ctx context.Context, trigger fv1.HTTPTrigger) (*resolveResult, error) {
	nfr := namespacedTriggerReference{
		namespace:              trigger.Namespace,
		triggerName:            trigger.Name,
		triggerResourceVersion: trigger.ResourceVersion,
	}

	// check cache
	result, err := frr.refCache.Get(nfr)
	if err == nil {
		return &result, nil
	}

	// resolve on cache miss
	var rr *resolveResult

	switch trigger.Spec.FunctionReference.Type {
	case fv1.FunctionReferenceTypeFunctionName:
		rr, err = frr.resolveByName(ctx, nfr.namespace, trigger.Spec.FunctionReference.Name)
		if err != nil {
			return nil, err
		}

	case fv1.FunctionReferenceTypeFunctionWeights:
		rr, err = frr.resolveByFunctionWeights(ctx, nfr.namespace, &trigger.Spec.FunctionReference)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unrecognized function reference type %v", trigger.Spec.FunctionReference.Type)
	}

	// cache resolve result
	frr.refCache.Upsert(nfr, *rr)

	return rr, nil
}

// getFunction reads a Function from the Manager's cache.
func (frr *functionReferenceResolver) getFunction(ctx context.Context, namespace, name string) (*fv1.Function, error) {
	f := &fv1.Function{}
	err := frr.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, f)
	if apierrors.IsNotFound(err) {
		frr.logger.Error(nil, "function does not exists", "name", name, "namespace", namespace)
		return nil, fmt.Errorf("function %s/%s does not exist", namespace, name)
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// resolveByName simply looks up function by name in a namespace.
func (frr *functionReferenceResolver) resolveByName(ctx context.Context, namespace, name string) (*resolveResult, error) {
	f, err := frr.getFunction(ctx, namespace, name)
	if err != nil {
		return nil, err
	}

	rr := resolveResult{
		resolveResultType: resolveResultSingleFunction,
		functionMap: map[string]*fv1.Function{
			f.Name: f,
		},
	}

	return &rr, nil
}

func (frr *functionReferenceResolver) resolveByFunctionWeights(ctx context.Context, namespace string, fr *fv1.FunctionReference) (*resolveResult, error) {
	functionMap := make(map[string]*fv1.Function)
	fnWtDistrList := make([]functionWeightDistribution, 0)
	sumPrefix := 0

	for functionName, functionWeight := range fr.FunctionWeights {
		f, err := frr.getFunction(ctx, namespace, functionName)
		if err != nil {
			return nil, err
		}
		functionMap[f.Name] = f
		sumPrefix = sumPrefix + functionWeight
		fnWtDistrList = append(fnWtDistrList, functionWeightDistribution{
			name:      functionName,
			weight:    functionWeight,
			sumPrefix: sumPrefix,
		})
	}

	rr := resolveResult{
		resolveResultType:          resolveResultMultipleFunctions,
		functionMap:                functionMap,
		functionWtDistributionList: fnWtDistrList,
	}

	return &rr, nil
}

func (frr *functionReferenceResolver) delete(namespace string, triggerName, triggerRV string) {
	nfr := namespacedTriggerReference{
		namespace:              namespace,
		triggerName:            triggerName,
		triggerResourceVersion: triggerRV,
	}
	frr.refCache.Delete(nfr)
}

// invalidateForFunction drops any cached resolve result that references the
// named function in the given namespace, so the next resolve re-reads the
// function from the cache. Used by the function reconciler when a Function's
// spec changes. Returns true if an entry was invalidated.
func (frr *functionReferenceResolver) invalidateForFunction(namespace, name, resourceVersion string) bool {
	invalidated := false
	for key, rr := range frr.refCache.Copy() {
		if key.namespace == namespace &&
			rr.functionMap[name] != nil &&
			rr.functionMap[name].ResourceVersion != resourceVersion {
			frr.logger.V(1).Info("invalidating resolver cache", "function", name, "namespace", namespace, "trigger", key.triggerName)
			frr.delete(key.namespace, key.triggerName, key.triggerResourceVersion)
			invalidated = true
			// Don't stop at the first match: the same function can back multiple
			// triggers (and multiple cached trigger-RV entries), all now stale.
		}
	}
	return invalidated
}
