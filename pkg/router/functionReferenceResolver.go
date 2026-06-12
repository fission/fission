// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// errFunctionNotFound marks a resolve failure caused by the referenced
// function not existing (as opposed to a transient cache/reader error).
// The incremental route path uses it to distinguish "remove the route and
// mark the trigger FunctionNotFound" from "requeue and keep the
// last-known-good route".
var errFunctionNotFound = errors.New("function not found")

type (
	// functionReferenceResolver turns a trigger's function reference into a
	// resolveResult. Resolution reads straight from the Manager's informer
	// cache (in-memory map reads): the trigger-RV-keyed result cache that used
	// to sit in front of it was a cache of a cache — it cost two goroutines
	// (pkg/cache actor + expiry), a full Copy() walk on every Function
	// reconcile, and a stale-snapshot class (trigger-RV keying misses function
	// updates) — and was removed in RFC-0014 phase 3.
	functionReferenceResolver struct {
		// reader is the Manager's cache-backed client. Function lookups go
		// through it (in-memory cache reads), replacing the per-namespace
		// SharedIndexInformer stores the resolver used before the
		// controller-runtime migration.
		reader client.Reader
		logger logr.Logger
	}

	resolveResultType int

	// resolveResult is the result of resolving a function reference;
	// it could be the metadata of one function or
	// a distribution of requests across two functions.
	resolveResult struct {
		resolveResultType
		functionMap                map[string]*fv1.Function
		functionWtDistributionList []functionWeightDistribution
	}
)

const (
	resolveResultSingleFunction = iota
	resolveResultMultipleFunctions
)

func makeFunctionReferenceResolver(logger logr.Logger, reader client.Reader) *functionReferenceResolver {
	return &functionReferenceResolver{
		reader: reader,
		logger: logger.WithName("function_ref_resolver"),
	}
}

// resolve translates a trigger's function reference to a resolveResult,
// reading current Function objects from the Manager cache. Uncached on
// purpose: it runs only during mux rebuilds (never on the request path — the
// result is closed into the route's handler at build time), and the informer
// reads it fans out to are in-memory.
func (frr *functionReferenceResolver) resolve(ctx context.Context, trigger fv1.HTTPTrigger) (*resolveResult, error) {
	switch trigger.Spec.FunctionReference.Type {
	case fv1.FunctionReferenceTypeFunctionName:
		return frr.resolveByName(ctx, trigger.Namespace, trigger.Spec.FunctionReference.Name)
	case fv1.FunctionReferenceTypeFunctionWeights:
		return frr.resolveByFunctionWeights(ctx, trigger.Namespace, &trigger.Spec.FunctionReference)
	default:
		return nil, fmt.Errorf("unrecognized function reference type %v", trigger.Spec.FunctionReference.Type)
	}
}

// getFunction reads a Function from the Manager's cache.
func (frr *functionReferenceResolver) getFunction(ctx context.Context, namespace, name string) (*fv1.Function, error) {
	f := &fv1.Function{}
	err := frr.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, f)
	if apierrors.IsNotFound(err) {
		frr.logger.Error(nil, "function does not exists", "name", name, "namespace", namespace)
		return nil, fmt.Errorf("function %s/%s does not exist: %w", namespace, name, errFunctionNotFound)
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
