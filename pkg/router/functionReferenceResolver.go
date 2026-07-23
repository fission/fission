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
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/versioning"
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
		// Aliases is the FunctionAlias names this resolution consumed
		// (RFC-0025): only resolveByAlias populates it (a plain name,
		// version-pinned, or FunctionWeights reference never references an
		// alias). applyTriggerIncremental copies it onto the route's
		// RouteSpec.Aliases so a FunctionAlias event can find and re-apply
		// exactly the triggers resolving through it (TriggersForAlias).
		Aliases []string
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
		return frr.resolveByName(ctx, trigger.Namespace, trigger.Spec.FunctionReference)
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

// resolveByName looks up a "name"-type function reference. Plain references
// (no Alias/Version) resolve straight to the live Function, unchanged from
// pre-RFC-0025 behavior — functionMap is keyed by BackendKey(name, ""),
// which is just name, so the result is byte-identical for unversioned
// triggers. A Version pin resolves to that one immutable FunctionVersion
// snapshot; an Alias resolves through the FunctionAlias's currently
// effective target(s) — one target normally, two during a weighted rollout.
func (frr *functionReferenceResolver) resolveByName(ctx context.Context, namespace string, ref fv1.FunctionReference) (*resolveResult, error) {
	switch {
	case ref.Version != "":
		fn, err := frr.resolveVersion(ctx, namespace, ref.Name, ref.Version)
		if err != nil {
			return nil, err
		}
		return singleFunctionResult(routetable.BackendKey(ref.Name, ref.Version), fn), nil

	case ref.Alias != "":
		return frr.resolveByAlias(ctx, namespace, ref)

	default:
		f, err := frr.getFunction(ctx, namespace, ref.Name)
		if err != nil {
			return nil, err
		}
		return singleFunctionResult(routetable.BackendKey(f.Name, ""), f), nil
	}
}

// singleFunctionResult wraps one resolved backend, keyed by BackendKey (or
// plain name for unversioned backends, since BackendKey(name, "") == name).
func singleFunctionResult(key string, fn *fv1.Function) *resolveResult {
	return &resolveResult{
		resolveResultType: resolveResultSingleFunction,
		functionMap:       map[string]*fv1.Function{key: fn},
	}
}

// resolveVersion resolves a FunctionVersion pin (by its CR name, "version")
// into a versioned Function projection: it Gets the FunctionVersion,
// validates it actually belongs to "name" (defense against a stale/
// mismatched reference — a version name recycled under a different
// function, or a hand-crafted trigger), Gets the live Function so the
// projection carries its identity (UID), and hands both to
// versioning.VersionedFunction. Every failure mode (missing version,
// mismatched owner, missing live function) rides errFunctionNotFound so the
// incremental apply path drops the route and marks the trigger unresolved
// rather than treating it as a transient error.
func (frr *functionReferenceResolver) resolveVersion(ctx context.Context, namespace, name, version string) (*fv1.Function, error) {
	v := &fv1.FunctionVersion{}
	err := frr.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: version}, v)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("function version %s/%s does not exist: %w", namespace, version, errFunctionNotFound)
	}
	if err != nil {
		return nil, err
	}
	if v.Spec.FunctionName != name {
		return nil, fmt.Errorf("function version %s/%s belongs to function %q, not %q: %w",
			namespace, version, v.Spec.FunctionName, name, errFunctionNotFound)
	}

	live, err := frr.getFunction(ctx, namespace, name)
	if err != nil {
		return nil, err
	}

	return versioning.VersionedFunction(live, v), nil
}

// resolveByAlias resolves a FunctionReference.Alias pin: Gets the named
// FunctionAlias, computes its effective target — Spec.Version when the
// alias is name-pinned (known immediately, no need to wait on the alias
// reconciler), else Status.ResolvedVersion (the reconciler's async
// resolution of a digest-pinned alias) — and resolves that target's
// FunctionVersion the same way a direct Version pin does. An alias that has
// never resolved (empty target) rides errFunctionNotFound: the router keeps
// the route unresolved rather than erroring the reconcile, exactly like a
// missing function — a future alias-resolution event (the FunctionAlias
// ROUTER reconciler, a later RFC-0025 task) or the periodic resync re-admits
// it once resolution completes.
//
// A weighted alias (Spec.Weight != nil) resolves BOTH targets — the primary
// at Weight, SecondaryVersion at 100-Weight — into a two-backend
// resolveResultMultipleFunctions, functionMap and functionWeightDistribution
// both keyed by each target's BackendKey.
func (frr *functionReferenceResolver) resolveByAlias(ctx context.Context, namespace string, ref fv1.FunctionReference) (*resolveResult, error) {
	alias := &fv1.FunctionAlias{}
	err := frr.reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Alias}, alias)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("function alias %s/%s does not exist: %w", namespace, ref.Alias, errFunctionNotFound)
	}
	if err != nil {
		return nil, err
	}

	target := alias.Spec.Version
	if target == "" {
		target = alias.Status.ResolvedVersion
	}
	if target == "" {
		return nil, fmt.Errorf("function alias %s/%s has not resolved to a version yet: %w", namespace, ref.Alias, errFunctionNotFound)
	}

	primary, err := frr.resolveVersion(ctx, namespace, ref.Name, target)
	if err != nil {
		return nil, err
	}
	primaryKey := routetable.BackendKey(ref.Name, target)

	if alias.Spec.Weight == nil {
		rr := singleFunctionResult(primaryKey, primary)
		rr.Aliases = []string{ref.Alias}
		return rr, nil
	}

	secondary, err := frr.resolveVersion(ctx, namespace, ref.Name, alias.Spec.SecondaryVersion)
	if err != nil {
		return nil, err
	}
	secondaryKey := routetable.BackendKey(ref.Name, alias.Spec.SecondaryVersion)

	weight := *alias.Spec.Weight
	rr := resolveResult{
		resolveResultType: resolveResultMultipleFunctions,
		functionMap: map[string]*fv1.Function{
			primaryKey:   primary,
			secondaryKey: secondary,
		},
		functionWtDistributionList: []functionWeightDistribution{
			{name: primaryKey, weight: weight, sumPrefix: weight},
			{name: secondaryKey, weight: 100 - weight, sumPrefix: 100},
		},
		Aliases: []string{ref.Alias},
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
