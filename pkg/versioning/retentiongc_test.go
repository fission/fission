// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// seedVersions creates count FunctionVersions for fnName in ns via cl,
// sequences 1..count, and returns their names in that order.
func seedVersions(t *testing.T, cl *fakeversioned.Clientset, ns, fnName string, count int) []string {
	t.Helper()
	names := make([]string, count)
	for i := 1; i <= count; i++ {
		v := testVersion(fnName, int64(i), fmt.Sprintf("sha256:%d", i))
		v.Namespace = ns
		_, err := cl.CoreV1().FunctionVersions(ns).Create(t.Context(), v, metav1.CreateOptions{})
		require.NoError(t, err)
		names[i-1] = v.Name
	}
	return names
}

func listVersionNames(t *testing.T, cl *fakeversioned.Clientset, ns string) []string {
	t.Helper()
	list, err := cl.CoreV1().FunctionVersions(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	names := make([]string, 0, len(list.Items))
	for _, v := range list.Items {
		names = append(names, v.Name)
	}
	return names
}

// TestSweepVersions_RetainNDeletesOldest: 12 versions, retain 10 -- the
// oldest 2 (v1, v2) are deleted, v3..v12 survive.
func TestSweepVersions_RetainNDeletesOldest(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	seedVersions(t, cl, ns, "fn", 12)

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 10)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"fn-v1", "fn-v2"}, result.Deleted)
	assert.Empty(t, result.SkippedReferenced)
	assert.Empty(t, result.SkippedForbidden)
	assert.Len(t, result.Retained, 10)

	remaining := listVersionNames(t, cl, ns)
	assert.ElementsMatch(t, []string{"fn-v3", "fn-v4", "fn-v5", "fn-v6", "fn-v7", "fn-v8", "fn-v9", "fn-v10", "fn-v11", "fn-v12"}, remaining)
}

// TestSweepVersions_AliasedOldVersionSurvives covers invariant V3's three ref
// fields individually: an old, otherwise-GC-eligible version referenced by
// spec.Version, spec.SecondaryVersion, or status.ResolvedVersion must never
// be deleted.
func TestSweepVersions_AliasedOldVersionSurvives(t *testing.T) {
	t.Parallel()

	newAlias := func(refField, fnName, target string) *fv1.FunctionAlias {
		a := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod-" + refField, Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: fnName},
		}
		switch refField {
		case "version":
			a.Spec.Version = target
		case "secondaryVersion":
			// XOR rule (webhook, not enforced by the fake client): pin a
			// primary target too so this object shape is realistic.
			a.Spec.Version = "fn-v99"
			a.Spec.SecondaryVersion = target
		case "resolvedVersion":
			a.Spec.Version = "fn-v99"
			a.Status.ResolvedVersion = target
		}
		return a
	}

	for _, refField := range []string{"version", "secondaryVersion", "resolvedVersion"} {
		t.Run(refField, func(t *testing.T) {
			t.Parallel()
			ns := "default"
			cl := fakeversioned.NewSimpleClientset()
			// 3 versions, retain 1: v1 and v2 would ordinarily both be
			// GC-eligible candidates. v1 is alias-referenced via refField.
			names := seedVersions(t, cl, ns, "fn", 3)
			alias := newAlias(refField, "fn", names[0])
			_, err := cl.CoreV1().FunctionAliases(ns).Create(t.Context(), alias, metav1.CreateOptions{})
			require.NoError(t, err)

			result, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
			require.NoError(t, err)

			assert.NotContains(t, result.Deleted, names[0], "alias-referenced version must survive")
			assert.Contains(t, result.Deleted, names[1], "unaliased older version must still be swept")
			assert.Contains(t, result.Retained, names[0])

			remaining := listVersionNames(t, cl, ns)
			assert.Contains(t, remaining, names[0])
			assert.Contains(t, remaining, names[2], "newest is always retained")
		})
	}
}

// TestSweepVersions_NewestNeverDeleted: even with retain=1 and zero alias
// references, the newest version is never a candidate.
func TestSweepVersions_NewestNeverDeleted(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	names := seedVersions(t, cl, ns, "fn", 3)

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
	require.NoError(t, err)

	assert.NotContains(t, result.Deleted, names[2])
	assert.Contains(t, result.Retained, names[2])
	assert.ElementsMatch(t, []string{names[0], names[1]}, result.Deleted)
}

// TestSweepVersions_OnlyVersionNeverDeleted: a single version is always
// retained, regardless of retain -- there is nothing else to keep it company
// above the floor.
func TestSweepVersions_OnlyVersionNeverDeleted(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	names := seedVersions(t, cl, ns, "fn", 1)

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
	require.NoError(t, err)

	assert.Empty(t, result.Deleted)
	assert.Equal(t, names, result.Retained)
}

// TestSweepVersions_InterleavedAliasCreateSkipsCandidate is the aliasgc.tla
// trace: an alias-create for the oldest candidate lands AFTER SweepVersions's
// initial retained-set scan but BEFORE that candidate's delete-time recheck.
// The recheck must catch it and skip the delete -- the exact race
// RecheckGuard=TRUE closes and RecheckGuard=FALSE (the rejected design)
// does not (docs/rfc/specs/aliasgc.tla's GCCommit / NoDanglingAlias).
func TestSweepVersions_InterleavedAliasCreateSkipsCandidate(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	// retain 1: v1 (oldest) is the sole candidate, v2 is retained by count.
	names := seedVersions(t, cl, ns, "fn", 2)

	tracker := cl.Tracker()
	listCalls := 0
	// PrependReactor on "list functionaliases": the FIRST call is
	// SweepVersions's initial scan (must see zero aliases -- v1 starts as an
	// eligible candidate). The SECOND call is v1's delete-time recheck: seed
	// a racing alias-create directly into the tracker (never call the typed
	// client from inside a reactor -- Fake.Invokes holds an RWMutex for the
	// whole dispatch and a nested call self-deadlocks on it) and let the
	// list fall through to the default tracker-backed reactor, which then
	// observes the just-added alias.
	cl.PrependReactor("list", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listCalls++
		if listCalls == 2 {
			racer := &fv1.FunctionAlias{
				ObjectMeta: metav1.ObjectMeta{Name: "racer", Namespace: ns},
				Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: names[0]},
			}
			if err := tracker.Add(racer); err != nil {
				return true, nil, fmt.Errorf("test setup: seeding racing alias: %w", err)
			}
		}
		return false, nil, nil // let the default tracker-backed reactor actually list
	})

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
	require.NoError(t, err)

	assert.Empty(t, result.Deleted, "the recheck must catch the interleaved alias-create")
	assert.Equal(t, []string{names[0]}, result.SkippedReferenced)
	assert.GreaterOrEqual(t, listCalls, 2, "test premise: the recheck issued a second aliases list")

	remaining := listVersionNames(t, cl, ns)
	assert.Contains(t, remaining, names[0], "invariant V3: the now-aliased version must not have been deleted")
}

// TestSweepVersions_ForbiddenDeleteSkippedNotTerminal: a Delete denied with
// 403 Forbidden (RBAC or the webhook -- byte-indistinguishable, never
// substring-matched) is recorded as SkippedForbidden and the sweep continues
// rather than aborting with an error.
func TestSweepVersions_ForbiddenDeleteSkippedNotTerminal(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	names := seedVersions(t, cl, ns, "fn", 3)

	cl.PrependReactor("delete", "functionversions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		del := action.(k8stesting.DeleteAction)
		if del.GetName() == names[0] {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "fission.io", Resource: "functionversions"}, names[0], fmt.Errorf("denied"))
		}
		return false, nil, nil
	})

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
	require.NoError(t, err, "a Forbidden delete must never be terminal")

	assert.Equal(t, []string{names[0]}, result.SkippedForbidden)
	assert.Equal(t, []string{names[1]}, result.Deleted)

	remaining := listVersionNames(t, cl, ns)
	assert.Contains(t, remaining, names[0], "the Forbidden delete must not have removed the version")
}

// TestSweepVersions_OtherDeleteErrorPropagates: a non-Forbidden delete error
// aborts the sweep rather than being swallowed like Forbidden is.
func TestSweepVersions_OtherDeleteErrorPropagates(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	names := seedVersions(t, cl, ns, "fn", 3)

	cl.PrependReactor("delete", "functionversions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd unavailable"))
	})

	_, err := SweepVersions(t.Context(), cl, ns, "fn", 1)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrPackageNotReady) // sanity: not accidentally reusing an unrelated sentinel
	_ = names
}

// TestSweepVersions_NoVersionsIsNoOp: an unpublished function has nothing to
// sweep.
func TestSweepVersions_NoVersionsIsNoOp(t *testing.T) {
	t.Parallel()
	cl := fakeversioned.NewSimpleClientset()

	result, err := SweepVersions(t.Context(), cl, "default", "fn", 10)
	require.NoError(t, err)
	assert.Empty(t, result.Deleted)
	assert.Empty(t, result.Retained)
	assert.Empty(t, result.SkippedReferenced)
	assert.Empty(t, result.SkippedForbidden)
}

// TestSweepVersions_RetainFlooredAtOne: retain=0 (or negative) is floored to
// 1, never zero -- SweepVersions must not delete every version.
func TestSweepVersions_RetainFlooredAtOne(t *testing.T) {
	t.Parallel()
	ns := "default"
	cl := fakeversioned.NewSimpleClientset()
	names := seedVersions(t, cl, ns, "fn", 3)

	result, err := SweepVersions(t.Context(), cl, ns, "fn", 0)
	require.NoError(t, err)

	assert.Contains(t, result.Retained, names[2])
	assert.NotContains(t, result.Deleted, names[2])
}

// --- RetentionGCReconciler ---

func newRetentionGCReconciler(t *testing.T, cs *fakeversioned.Clientset, ctrlObjs ...client.Object) *RetentionGCReconciler {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(ctrlObjs...).
		Build()
	return &RetentionGCReconciler{logger: logr.Discard(), client: c, clientset: cs}
}

func reconcileGC(t *testing.T, r *RetentionGCReconciler, ns, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

// TestRetentionGCReconcile_NilVersioningNoOp: a function that never opted
// into RFC-0025 versioning is never swept -- its FunctionVersions (however
// they got there, e.g. explicit `fission fn publish`) are left alone.
func TestRetentionGCReconcile_NilVersioningNoOp(t *testing.T) {
	t.Parallel()
	ns := "default"
	fn := makeFunction("fn", ns, "pkg") // Versioning left nil
	cs := fakeversioned.NewSimpleClientset(fn)
	seedVersions(t, cs, ns, "fn", 12)

	r := newRetentionGCReconciler(t, cs, fn)
	res := reconcileGC(t, r, ns, "fn")

	assert.Zero(t, res.RequeueAfter)
	assert.Len(t, listVersionNames(t, cs, ns), 12, "no opt-in means no sweep")
}

// TestRetentionGCReconcile_FunctionGoneNoOp: a Function deleted between the
// event firing and Reconcile running is not an error -- the CRD ownerRef
// cascade already deletes its FunctionVersions.
func TestRetentionGCReconcile_FunctionGoneNoOp(t *testing.T) {
	t.Parallel()
	cs := fakeversioned.NewSimpleClientset()
	r := newRetentionGCReconciler(t, cs)

	res := reconcileGC(t, r, "default", "missing")
	assert.Zero(t, res.RequeueAfter)
}

// TestRetentionGCReconcile_DefaultRetainApplies: Spec.Versioning set with a
// nil Retain sweeps down to DefaultRetain.
func TestRetentionGCReconcile_DefaultRetainApplies(t *testing.T) {
	t.Parallel()
	ns := "default"
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto) // Retain left nil
	cs := fakeversioned.NewSimpleClientset(fn)
	seedVersions(t, cs, ns, "fn", 12)

	r := newRetentionGCReconciler(t, cs, fn)
	reconcileGC(t, r, ns, "fn")

	assert.Len(t, listVersionNames(t, cs, ns), DefaultRetain)
}

// TestRetentionGCReconcile_ExplicitRetainOverridesDefault: a
// Spec.Versioning.Retain lower than DefaultRetain is honored -- this is also
// the "retain lowering" case the GenerationChangedPredicate on the .For()
// watch exists to catch.
func TestRetentionGCReconcile_ExplicitRetainOverridesDefault(t *testing.T) {
	t.Parallel()
	ns := "default"
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)
	retain := 3
	fn.Spec.Versioning.Retain = &retain
	cs := fakeversioned.NewSimpleClientset(fn)
	seedVersions(t, cs, ns, "fn", 12)

	r := newRetentionGCReconciler(t, cs, fn)
	reconcileGC(t, r, ns, "fn")

	assert.Len(t, listVersionNames(t, cs, ns), 3)
}

// TestRetentionGCReconcile_ForbiddenRequeues: any SkippedForbidden in the
// sweep result signals the reconciler to RequeueAfter rather than treating
// the sweep as fully converged.
func TestRetentionGCReconcile_ForbiddenRequeues(t *testing.T) {
	t.Parallel()
	ns := "default"
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)
	retain := 1
	fn.Spec.Versioning.Retain = &retain
	cs := fakeversioned.NewSimpleClientset(fn)
	names := seedVersions(t, cs, ns, "fn", 2)

	cs.PrependReactor("delete", "functionversions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		del := action.(k8stesting.DeleteAction)
		if del.GetName() == names[0] {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "fission.io", Resource: "functionversions"}, names[0], fmt.Errorf("denied"))
		}
		return false, nil, nil
	})

	r := newRetentionGCReconciler(t, cs, fn)
	res := reconcileGC(t, r, ns, "fn")

	assert.Equal(t, retentionGCRequeueInterval, res.RequeueAfter)
}

// TestRetentionGCReconcile_AliasWatchMapsToFunction covers mapAliasToFunction
// directly -- an alias event (including delete, which is what RELEASES a
// version for GC) must enqueue the function it names.
func TestRetentionGCReconcile_AliasWatchMapsToFunction(t *testing.T) {
	t.Parallel()
	cs := fakeversioned.NewSimpleClientset()
	r := newRetentionGCReconciler(t, cs)

	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "fn"},
	}
	reqs := r.mapAliasToFunction(t.Context(), alias)
	require.Len(t, reqs, 1)
	assert.Equal(t, "fn", reqs[0].Name)
	assert.Equal(t, "default", reqs[0].Namespace)
}

// TestRetentionGCReconcile_VersionWatchMapsToFunction covers
// mapVersionToFunction directly.
func TestRetentionGCReconcile_VersionWatchMapsToFunction(t *testing.T) {
	t.Parallel()
	cs := fakeversioned.NewSimpleClientset()
	r := newRetentionGCReconciler(t, cs)

	v := testVersion("fn", 1, "sha256:abc")
	reqs := r.mapVersionToFunction(t.Context(), v)
	require.Len(t, reqs, 1)
	assert.Equal(t, "fn", reqs[0].Name)
	assert.Equal(t, "default", reqs[0].Namespace)
}
