// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// resolverFn builds a live Function for resolver tests.
func resolverFn(name, ns string, uid types.UID, gen int64, timeout int) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid, Generation: gen},
		Spec:       fv1.FunctionSpec{FunctionTimeout: timeout},
	}
}

// resolverVersion builds a FunctionVersion snapshot pointing at (uid, gen) of
// fnName, carrying a distinctive FunctionTimeout in its Snapshot so tests can
// tell "resolved the version's snapshot" apart from "resolved the live spec".
func resolverVersion(name, ns, fnName string, uid types.UID, gen, seq int64, snapshotTimeout int) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:       fnName,
			FunctionUID:        uid,
			FunctionGeneration: gen,
			Sequence:           seq,
			Snapshot:           fv1.FunctionSpec{FunctionTimeout: snapshotTimeout},
			PackageDigest:      "sha256:0000000000000000000000000000000000000000000000000000000000000",
			PublishedAt:        metav1.Now(),
		},
	}
}

func resolverAlias(name, ns, fnName string, mutate func(*fv1.FunctionAlias)) *fv1.FunctionAlias {
	a := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       fv1.FunctionAliasSpec{FunctionName: fnName},
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func newResolver(t *testing.T, objs ...client.Object) *functionReferenceResolver {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	return makeFunctionReferenceResolver(loggerfactory.GetLogger(), cl)
}

// TestResolveByName_Unversioned pins byte-identical behavior for a plain
// "name"-type reference (no Alias/Version): functionMap is keyed by the
// plain function name (BackendKey(name, "") == name), exactly as before
// RFC-0025.
func TestResolveByName_Unversioned(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	frr := newResolver(t, fn)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, resolveResultType(resolveResultSingleFunction), rr.resolveResultType)
	require.Contains(t, rr.functionMap, "hello")
	assert.Equal(t, fn.UID, rr.functionMap["hello"].UID)
	assert.Empty(t, rr.functionMap["hello"].Labels[fv1.FUNCTION_VERSION])
}

// TestResolveByName_Unversioned_NotFound pins the errFunctionNotFound
// wrapping the incremental apply path branches on.
func TestResolveByName_Unversioned_NotFound(t *testing.T) {
	frr := newResolver(t)
	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "ghost",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_VersionPin resolves a direct Version pin to the
// FunctionVersion's snapshot, projected via versioning.VersionedFunction:
// Generation comes from the version, Spec from its Snapshot, identity (UID)
// from the live Function, and the version label is stamped.
func TestResolveByName_VersionPin(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 3, 60)
	v := resolverVersion("hello-v1", "default", "hello", "fn-uid", 3, 1, 77)
	frr := newResolver(t, fn, v)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Version: "hello-v1",
	})
	require.NoError(t, err)
	assert.Equal(t, resolveResultType(resolveResultSingleFunction), rr.resolveResultType)

	key := routetable.BackendKey("hello", "hello-v1")
	require.Contains(t, rr.functionMap, key)
	resolved := rr.functionMap[key]
	assert.Equal(t, types.UID("fn-uid"), resolved.UID, "identity (UID) comes from the live Function")
	assert.Equal(t, int64(3), resolved.Generation, "Generation is pinned from the FunctionVersion")
	assert.Equal(t, 77, resolved.Spec.FunctionTimeout, "Spec comes from the version's Snapshot, not the live spec")
	assert.Equal(t, "hello-v1", resolved.Labels[fv1.FUNCTION_VERSION])
}

// TestResolveByName_VersionPin_NotFound: the pinned FunctionVersion CR does
// not exist -- rides errFunctionNotFound so the route is dropped, not
// treated as a transient resolve error.
func TestResolveByName_VersionPin_NotFound(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	frr := newResolver(t, fn)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Version: "hello-vGHOST",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_VersionPin_WrongOwner: the FunctionVersion exists but
// belongs to a different function -- defense against a mismatched/stale
// reference, also rides errFunctionNotFound.
func TestResolveByName_VersionPin_WrongOwner(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	other := resolverVersion("other-v1", "default", "someone-else", "other-uid", 1, 1, 60)
	frr := newResolver(t, fn, other)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Version: "other-v1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_VersionPin_LiveFunctionMissing: the FunctionVersion
// exists and matches, but the live Function it claims to belong to has been
// deleted -- also rides errFunctionNotFound (via getFunction).
func TestResolveByName_VersionPin_LiveFunctionMissing(t *testing.T) {
	v := resolverVersion("hello-v1", "default", "hello", "fn-uid", 1, 1, 60)
	frr := newResolver(t, v)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Version: "hello-v1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_Alias_NamePinned resolves through a FunctionAlias whose
// Spec.Version is set directly (the imperative path) -- the effective target
// is known immediately, no need to wait on Status.ResolvedVersion.
func TestResolveByName_Alias_NamePinned(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 5, 60)
	v := resolverVersion("hello-v1", "default", "hello", "fn-uid", 5, 1, 88)
	alias := resolverAlias("prod", "default", "hello", func(a *fv1.FunctionAlias) {
		a.Spec.Version = "hello-v1"
	})
	frr := newResolver(t, fn, v, alias)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod",
	})
	require.NoError(t, err)
	assert.Equal(t, resolveResultType(resolveResultSingleFunction), rr.resolveResultType)

	key := routetable.BackendKey("hello", "hello-v1")
	require.Contains(t, rr.functionMap, key)
	assert.Equal(t, 88, rr.functionMap[key].Spec.FunctionTimeout)
}

// TestResolveByName_Alias_DigestPinned_UsesResolvedVersion resolves through
// a digest-pinned alias (Spec.Version empty, Spec.PackageDigest set): the
// effective target falls back to Status.ResolvedVersion, the AliasReconciler's
// async resolution of the digest.
func TestResolveByName_Alias_DigestPinned_UsesResolvedVersion(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 2, 60)
	v := resolverVersion("hello-v2", "default", "hello", "fn-uid", 2, 2, 99)
	alias := resolverAlias("prod", "default", "hello", func(a *fv1.FunctionAlias) {
		a.Spec.PackageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111"
		a.Status.ResolvedVersion = "hello-v2"
	})
	frr := newResolver(t, fn, v, alias)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod",
	})
	require.NoError(t, err)
	key := routetable.BackendKey("hello", "hello-v2")
	require.Contains(t, rr.functionMap, key)
	assert.Equal(t, 99, rr.functionMap[key].Spec.FunctionTimeout)
}

// TestResolveByName_Alias_NeverResolved: an alias with neither Spec.Version
// nor Status.ResolvedVersion set (a digest-pinned alias still waiting on a
// matching FunctionVersion) rides errFunctionNotFound -- the router keeps
// the route unresolved rather than erroring the reconcile.
func TestResolveByName_Alias_NeverResolved(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	alias := resolverAlias("prod", "default", "hello", func(a *fv1.FunctionAlias) {
		a.Spec.PackageDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222"
		// Status.ResolvedVersion intentionally left empty.
	})
	frr := newResolver(t, fn, alias)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_Alias_NotFound: the referenced FunctionAlias CR does not
// exist -- also rides errFunctionNotFound.
func TestResolveByName_Alias_NotFound(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	frr := newResolver(t, fn)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "ghost",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_Alias_Weighted resolves a weighted alias
// (Spec.Weight != nil) into a two-backend resolveResultMultipleFunctions:
// functionMap and functionWeightDistribution are both keyed by each target's
// BackendKey, and the distribution's weights match the alias spec.
func TestResolveByName_Alias_Weighted(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 2, 60)
	v1 := resolverVersion("hello-v1", "default", "hello", "fn-uid", 1, 1, 10)
	v2 := resolverVersion("hello-v2", "default", "hello", "fn-uid", 2, 2, 20)
	weight := 70
	alias := resolverAlias("prod", "default", "hello", func(a *fv1.FunctionAlias) {
		a.Spec.Version = "hello-v1"
		a.Spec.Weight = &weight
		a.Spec.SecondaryVersion = "hello-v2"
	})
	frr := newResolver(t, fn, v1, v2, alias)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod",
	})
	require.NoError(t, err)
	require.Equal(t, resolveResultType(resolveResultMultipleFunctions), rr.resolveResultType)

	primaryKey := routetable.BackendKey("hello", "hello-v1")
	secondaryKey := routetable.BackendKey("hello", "hello-v2")
	require.Len(t, rr.functionMap, 2)
	assert.Equal(t, 10, rr.functionMap[primaryKey].Spec.FunctionTimeout)
	assert.Equal(t, 20, rr.functionMap[secondaryKey].Spec.FunctionTimeout)

	require.Len(t, rr.functionWtDistributionList, 2)
	var primaryWt, secondaryWt functionWeightDistribution
	for _, d := range rr.functionWtDistributionList {
		switch d.name {
		case primaryKey:
			primaryWt = d
		case secondaryKey:
			secondaryWt = d
		default:
			t.Fatalf("unexpected distribution entry name %q", d.name)
		}
	}
	assert.Equal(t, 70, primaryWt.weight)
	assert.Equal(t, 70, primaryWt.sumPrefix)
	assert.Equal(t, 30, secondaryWt.weight)
	assert.Equal(t, 100, secondaryWt.sumPrefix)

	// Statistical: getCanaryBackend must pick the primary roughly 70% of the
	// time over a large sample (RFC-0025 weighted rollout traffic split).
	primaryHits := 0
	const trials = 20000
	for range trials {
		picked := getCanaryBackend(rr.functionMap, rr.functionWtDistributionList)
		if picked.Spec.FunctionTimeout == 10 { // v1's distinctive snapshot marker
			primaryHits++
		}
	}
	ratio := float64(primaryHits) / float64(trials)
	assert.InDelta(t, 0.70, ratio, 0.03, "weighted alias split must land close to 70/30 over %d trials", trials)
}

// TestResolveByAlias_Weighted_SecondaryVersionNotFound: the primary target
// resolves but SecondaryVersion's FunctionVersion is missing -- the whole
// resolve fails (errFunctionNotFound), no partial single-backend result.
func TestResolveByAlias_Weighted_SecondaryVersionNotFound(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	v1 := resolverVersion("hello-v1", "default", "hello", "fn-uid", 1, 1, 10)
	weight := 50
	alias := resolverAlias("prod", "default", "hello", func(a *fv1.FunctionAlias) {
		a.Spec.Version = "hello-v1"
		a.Spec.Weight = &weight
		a.Spec.SecondaryVersion = "hello-vGHOST"
	})
	frr := newResolver(t, fn, v1, alias)

	_, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Alias: "prod",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFunctionNotFound)
}

// TestResolveByName_AliasAndVersionMutuallyExclusive documents that the
// resolver takes Version pins before Alias pins when (in violation of the
// CRD's CEL XOR rule) both happen to be set on a hand-crafted reference --
// defense in depth, not a supported configuration.
func TestResolveByName_AliasAndVersionMutuallyExclusive(t *testing.T) {
	fn := resolverFn("hello", "default", "fn-uid", 1, 60)
	v := resolverVersion("hello-v1", "default", "hello", "fn-uid", 1, 1, 42)
	frr := newResolver(t, fn, v)

	rr, err := frr.resolveByName(t.Context(), "default", fv1.FunctionReference{
		Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello", Version: "hello-v1", Alias: "unused-alias",
	})
	require.NoError(t, err)
	key := routetable.BackendKey("hello", "hello-v1")
	require.Contains(t, rr.functionMap, key)
}
