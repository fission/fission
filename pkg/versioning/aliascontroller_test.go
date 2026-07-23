// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func newAliasReconciler(t *testing.T, objs ...client.Object) (*AliasReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.FunctionAlias{}).
		Build()
	r := &AliasReconciler{logger: logr.Discard(), client: c}
	return r, c
}

func testFunction(name string, uid types.UID) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid, Generation: 1},
	}
}

func testVersion(fnName string, seq int64, digest string) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fnName + "-v" + strconv.FormatInt(seq, 10),
			Namespace: "default",
			Labels:    map[string]string{fv1.VersionFunctionNameLabel: fnName},
		},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:  fnName,
			Sequence:      seq,
			PackageDigest: digest,
		},
	}
}

func testAliasNamePinned(name, fnName, version string) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec:       fv1.FunctionAliasSpec{FunctionName: fnName, Version: version},
	}
}

func testAliasDigestPinned(name, fnName, digest string) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec:       fv1.FunctionAliasSpec{FunctionName: fnName, PackageDigest: digest},
	}
}

func reconcileAlias(t *testing.T, r *AliasReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
	require.NoError(t, err)
}

func getAlias(t *testing.T, c client.Client, name string) *fv1.FunctionAlias {
	t.Helper()
	a := &fv1.FunctionAlias{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: name}, a))
	return a
}

func TestAliasReconcileNamePinnedResolves(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:aaa")
	alias := testAliasNamePinned("prod", "hello", v.Name)
	r, c := newAliasReconciler(t, fn, v, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Equal(t, v.Name, got.Status.ResolvedVersion)
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))
	cond := conditions.Find(got.Status.Conditions, fv1.FunctionAliasConditionResolved)
	require.NotNil(t, cond)
	assert.Equal(t, fv1.FunctionAliasReasonResolved, cond.Reason)
	assert.Empty(t, got.Status.History, "no history on first resolution")
}

func TestAliasReconcileNamePinnedVersionMissing(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	alias := testAliasNamePinned("prod", "hello", "hello-v9")
	r, c := newAliasReconciler(t, fn, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Empty(t, got.Status.ResolvedVersion)
	assert.False(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))
	cond := conditions.Find(got.Status.Conditions, fv1.FunctionAliasConditionResolved)
	require.NotNil(t, cond)
	assert.Equal(t, fv1.FunctionAliasReasonVersionNotFound, cond.Reason)
}

// TestAliasReconcileNamePinnedVersionDeletedKeepsLastResolved covers the
// defense-in-depth path the webhook's create/update-time existence check
// cannot: a name-pinned alias that already resolved successfully, whose
// target FunctionVersion is later deleted out from under it. Resolution
// must degrade to Resolved=False/VersionNotFound while leaving
// ResolvedVersion at its last value -- the same "keep serving the last
// resolved target" contract the digest-pinned unmatched path already
// guarantees.
func TestAliasReconcileNamePinnedVersionDeletedKeepsLastResolved(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:aaa")
	alias := testAliasNamePinned("prod", "hello", v.Name)
	r, c := newAliasReconciler(t, fn, v, alias)

	reconcileAlias(t, r, "prod")
	got := getAlias(t, c, "prod")
	require.Equal(t, v.Name, got.Status.ResolvedVersion)
	require.True(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))

	require.NoError(t, c.Delete(t.Context(), v))

	reconcileAlias(t, r, "prod")
	final := getAlias(t, c, "prod")
	assert.Equal(t, v.Name, final.Status.ResolvedVersion, "ResolvedVersion must stay at the last resolved target")
	assert.False(t, conditions.IsTrue(final.Status.Conditions, fv1.FunctionAliasConditionResolved))
	cond := conditions.Find(final.Status.Conditions, fv1.FunctionAliasConditionResolved)
	require.NotNil(t, cond)
	assert.Equal(t, fv1.FunctionAliasReasonVersionNotFound, cond.Reason)
}

func TestAliasReconcileDigestPinnedPicksHighestSequence(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v1 := testVersion("hello", 1, "sha256:bbb")
	v2 := testVersion("hello", 2, "sha256:bbb")
	v3 := testVersion("hello", 3, "sha256:ccc") // different digest, must not be picked
	alias := testAliasDigestPinned("prod", "hello", "sha256:bbb")
	r, c := newAliasReconciler(t, fn, v1, v2, v3, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Equal(t, v2.Name, got.Status.ResolvedVersion, "must pick the highest-Sequence version matching the digest")
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))
}

func TestAliasReconcileDigestAppearsLaterFlipsFalseToTrue(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	alias := testAliasDigestPinned("prod", "hello", "sha256:ddd")
	r, c := newAliasReconciler(t, fn, alias)

	// First reconcile: no matching FunctionVersion exists yet.
	reconcileAlias(t, r, "prod")
	got := getAlias(t, c, "prod")
	assert.False(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))
	cond := conditions.Find(got.Status.Conditions, fv1.FunctionAliasConditionResolved)
	require.NotNil(t, cond)
	assert.Equal(t, fv1.FunctionAliasReasonDigestUnmatched, cond.Reason)
	assert.Empty(t, got.Status.ResolvedVersion)

	// The matching version now appears (e.g. a publish landed after the alias
	// was created).
	v := testVersion("hello", 1, "sha256:ddd")
	require.NoError(t, c.Create(t.Context(), v))

	reconcileAlias(t, r, "prod")
	got = getAlias(t, c, "prod")
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionAliasConditionResolved))
	assert.Equal(t, v.Name, got.Status.ResolvedVersion)
}

func TestAliasReconcileUnmatchedKeepsLastResolvedVersion(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:eee")
	alias := testAliasDigestPinned("prod", "hello", "sha256:eee")
	r, c := newAliasReconciler(t, fn, v, alias)

	reconcileAlias(t, r, "prod")
	got := getAlias(t, c, "prod")
	require.Equal(t, v.Name, got.Status.ResolvedVersion)

	// Re-point the alias at a digest nothing records (e.g. the version
	// carrying it was garbage collected, or the spec was updated ahead of
	// the publish).
	got.Spec.PackageDigest = "sha256:doesnotexist"
	got.Generation = 2
	require.NoError(t, c.Update(t.Context(), got))

	reconcileAlias(t, r, "prod")
	final := getAlias(t, c, "prod")
	assert.Equal(t, v.Name, final.Status.ResolvedVersion, "ResolvedVersion must stay at the last resolved target")
	assert.False(t, conditions.IsTrue(final.Status.Conditions, fv1.FunctionAliasConditionResolved))
	cond := conditions.Find(final.Status.Conditions, fv1.FunctionAliasConditionResolved)
	require.NotNil(t, cond)
	assert.Equal(t, fv1.FunctionAliasReasonDigestUnmatched, cond.Reason)
}

// TestAliasReconcileHistoryAppendedOnlyOnChange drives two reconciles at the
// same target and asserts no history entry is added by the second (a no-op
// re-resolution), then a third reconcile after retargeting appends exactly
// one entry for the outgoing version.
func TestAliasReconcileHistoryAppendedOnlyOnChange(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v1 := testVersion("hello", 1, "sha256:v1")
	v2 := testVersion("hello", 2, "sha256:v2")
	alias := testAliasNamePinned("prod", "hello", v1.Name)
	r, c := newAliasReconciler(t, fn, v1, v2, alias)

	reconcileAlias(t, r, "prod")
	reconcileAlias(t, r, "prod") // no-op: still pinned at v1
	got := getAlias(t, c, "prod")
	assert.Empty(t, got.Status.History, "no history until the target actually changes")

	got.Spec.Version = v2.Name
	got.Generation = 2
	require.NoError(t, c.Update(t.Context(), got))

	reconcileAlias(t, r, "prod")
	final := getAlias(t, c, "prod")
	require.Len(t, final.Status.History, 1)
	assert.Equal(t, v1.Name, final.Status.History[0].Version, "the OUTGOING target is recorded")
	assert.Equal(t, "sha256:v1", final.Status.History[0].PackageDigest)
	assert.Equal(t, v2.Name, final.Status.ResolvedVersion)
}

// TestAliasReconcileHistoryBoundedAtTen drives 12 distinct retargets and
// asserts the history caps at 10 entries, most recent last, oldest dropped
// from the front.
func TestAliasReconcileHistoryBoundedAtTen(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	objs := []client.Object{fn}
	versions := make([]*fv1.FunctionVersion, 0, 13)
	for i := int64(1); i <= 13; i++ {
		v := testVersion("hello", i, "sha256:v"+strconv.FormatInt(i, 10))
		versions = append(versions, v)
		objs = append(objs, v)
	}
	alias := testAliasNamePinned("prod", "hello", versions[0].Name)
	objs = append(objs, alias)
	r, c := newAliasReconciler(t, objs...)

	reconcileAlias(t, r, "prod") // resolves to v1, no history yet

	gen := int64(1)
	for i := 1; i < 13; i++ { // 12 repoints: v2..v13
		gen++
		got := getAlias(t, c, "prod")
		got.Spec.Version = versions[i].Name
		got.Generation = gen
		require.NoError(t, c.Update(t.Context(), got))
		reconcileAlias(t, r, "prod")
	}

	final := getAlias(t, c, "prod")
	require.Len(t, final.Status.History, aliasHistoryLimit)
	// 12 transitions occurred (v1->v2, v2->v3, ..., v12->v13); the oldest two
	// (outgoing v1, outgoing v2) were dropped, leaving outgoing v3..v12 with
	// v12 last (most recent).
	assert.Equal(t, "hello-v3", final.Status.History[0].Version)
	assert.Equal(t, "hello-v12", final.Status.History[len(final.Status.History)-1].Version)
	assert.Equal(t, versions[12].Name, final.Status.ResolvedVersion)
}

func TestAliasReconcileOwnerRefRepairMissing(t *testing.T) {
	fn := testFunction("hello", "fn-uid-123")
	alias := testAliasNamePinned("prod", "hello", "")
	alias.Spec.Version = ""
	alias.Spec.PackageDigest = "sha256:whatever"
	r, c := newAliasReconciler(t, fn, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "Function", got.OwnerReferences[0].Kind)
	assert.Equal(t, "hello", got.OwnerReferences[0].Name)
	assert.Equal(t, fn.UID, got.OwnerReferences[0].UID)
}

func TestAliasReconcileOwnerRefRepairStaleUID(t *testing.T) {
	fn := testFunction("hello", "fn-uid-current")
	alias := testAliasNamePinned("prod", "hello", "")
	alias.Spec.PackageDigest = "sha256:whatever"
	alias.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: fv1.SchemeGroupVersion.String(), Kind: "Function", Name: "hello", UID: "stale-uid"},
	}
	r, c := newAliasReconciler(t, fn, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	require.Len(t, got.OwnerReferences, 1, "stale ref is replaced, not duplicated")
	assert.Equal(t, fn.UID, got.OwnerReferences[0].UID)
}

func TestAliasReconcileOwnerRefToleratesAbsentFunction(t *testing.T) {
	alias := testAliasNamePinned("prod", "does-not-exist", "")
	alias.Spec.PackageDigest = "sha256:whatever"
	r, c := newAliasReconciler(t, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Empty(t, got.OwnerReferences, "no Function to repair against; left as-is")
}

func TestAliasReconcileLabelBackfill(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:aaa")
	alias := testAliasNamePinned("prod", "hello", v.Name)
	alias.Labels = nil
	r, c := newAliasReconciler(t, fn, v, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Equal(t, "hello", got.Labels[fv1.VersionFunctionNameLabel])
}

func TestAliasReconcileLabelBackfillCorrectsStaleValue(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:aaa")
	alias := testAliasNamePinned("prod", "hello", v.Name)
	alias.Labels = map[string]string{fv1.VersionFunctionNameLabel: "stale-name"}
	r, c := newAliasReconciler(t, fn, v, alias)

	reconcileAlias(t, r, "prod")

	got := getAlias(t, c, "prod")
	assert.Equal(t, "hello", got.Labels[fv1.VersionFunctionNameLabel])
}

// TestAliasReconcileIdempotentNoWritesOnSecondReconcile is the anti-loop
// guarantee: once an alias's metadata is repaired and its status reflects
// the correct resolution, a second reconcile with nothing changed in the
// world must not issue a single Update or status Patch. Without this, the
// controller would fight its own writes forever (every status write is
// itself a new event on the FunctionAlias it watches).
func TestAliasReconcileIdempotentNoWritesOnSecondReconcile(t *testing.T) {
	fn := testFunction("hello", "fn-uid")
	v := testVersion("hello", 1, "sha256:aaa")
	alias := testAliasNamePinned("prod", "hello", v.Name)

	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(fn, v, alias).
		WithStatusSubresource(&fv1.FunctionAlias{}).
		Build()
	r := &AliasReconciler{logger: logr.Discard(), client: c}

	// First reconcile does the repair + resolve work.
	reconcileAlias(t, r, "prod")

	writes := 0
	counting := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(fn, v, getAlias(t, c, "prod")).
		WithStatusSubresource(&fv1.FunctionAlias{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes++
				return cl.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				writes++
				return cl.Patch(ctx, obj, patch, opts...)
			},
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				writes++
				return cl.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	r2 := &AliasReconciler{logger: logr.Discard(), client: counting}

	reconcileAlias(t, r2, "prod")

	assert.Zero(t, writes, "a fully-converged reconcile must perform zero writes")
}

func TestMapVersionToAliasesFiltersByFunctionAndRelevance(t *testing.T) {
	// resolvedAlias is already Resolved=True at a digest that does NOT match
	// the new version -- must not be enqueued.
	resolvedAlias := testAliasDigestPinned("resolved", "hello", "sha256:other")
	conditions.Set(&resolvedAlias.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})

	// unresolvedAlias is Resolved=False -- must be enqueued regardless of digest.
	unresolvedAlias := testAliasDigestPinned("unresolved", "hello", "sha256:unrelated")
	conditions.Set(&unresolvedAlias.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonDigestUnmatched,
	})

	// digestMatchAlias is Resolved=True already, but its digest matches the
	// new version exactly -- still enqueued (defensive; also covers a
	// same-digest republish).
	digestMatchAlias := testAliasDigestPinned("digest-match", "hello", "sha256:new")
	conditions.Set(&digestMatchAlias.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})

	// otherFunctionAlias is unresolved but for a different function -- must
	// never be enqueued by a version that isn't its own function's.
	otherFunctionAlias := testAliasDigestPinned("other-fn", "goodbye", "sha256:new")
	conditions.Set(&otherFunctionAlias.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonDigestUnmatched,
	})

	r, _ := newAliasReconciler(t, resolvedAlias, unresolvedAlias, digestMatchAlias, otherFunctionAlias)
	newVersion := testVersion("hello", 5, "sha256:new")

	reqs := r.mapVersionToAliases(t.Context(), newVersion)

	names := make([]string, 0, len(reqs))
	for _, req := range reqs {
		names = append(names, req.Name)
	}
	assert.ElementsMatch(t, []string{"unresolved", "digest-match"}, names)
}

// TestMapVersionToAliasesEnqueuesResolvedNamePinnedAliasOnVersionEvent is the
// regression test for the defense-in-depth gap: a name-pinned alias that is
// already Resolved=True (so the "unresolved" clause never fires) and has no
// PackageDigest (so "digestMatch" never fires either) must still be
// re-enqueued by an event on the FunctionVersion it is pinned to -- in
// particular its DELETE event, which is what lets Reconcile downgrade
// ResolvedVersion's Resolved condition once the target is gone. The map
// function receives the same last-known object on a Delete event as on
// Create/Update, so this is exercised the same way regardless of which
// event actually fired.
func TestMapVersionToAliasesEnqueuesResolvedNamePinnedAliasOnVersionEvent(t *testing.T) {
	v := testVersion("hello", 1, "sha256:aaa")

	primaryPinned := testAliasNamePinned("primary", "hello", v.Name)
	conditions.Set(&primaryPinned.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})
	primaryPinned.Status.ResolvedVersion = v.Name

	secondaryPinned := testAliasNamePinned("secondary", "hello", "hello-v9") // primary target unrelated
	secondaryPinned.Spec.SecondaryVersion = v.Name
	conditions.Set(&secondaryPinned.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})

	unrelatedPinned := testAliasNamePinned("unrelated", "hello", "hello-v2") // pinned at a different version
	conditions.Set(&unrelatedPinned.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})

	otherFunctionPinned := testAliasNamePinned("other-fn", "goodbye", v.Name) // same version name, different function
	conditions.Set(&otherFunctionPinned.Status.Conditions, metav1.Condition{
		Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved,
	})

	r, _ := newAliasReconciler(t, primaryPinned, secondaryPinned, unrelatedPinned, otherFunctionPinned)

	reqs := r.mapVersionToAliases(t.Context(), v)

	names := make([]string, 0, len(reqs))
	for _, req := range reqs {
		names = append(names, req.Name)
	}
	assert.ElementsMatch(t, []string{"primary", "secondary"}, names)
}

func TestAliasReconcileNotFoundIsNotAnError(t *testing.T) {
	r, _ := newAliasReconciler(t)
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "missing"}})
	assert.NoError(t, err)
}
