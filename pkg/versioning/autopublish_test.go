// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// newAutopublishReconciler builds an AutoPublishReconciler over a fake
// controller-runtime client (seeded with ctrlObjs, read by Reconcile's
// primary Function Get and by the map-function's mgr-independent List path
// where applicable) and cs, the generated fake clientset (read/written by
// the Publish engine and newestVersion, mirroring how buildermgr actually
// threads its ClientGenerator-sourced clientset into the reconciler).
func newAutopublishReconciler(t *testing.T, cs versioned.Interface, ctrlObjs ...client.Object) *AutoPublishReconciler {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(ctrlObjs...).
		Build()
	return &AutoPublishReconciler{logger: logr.Discard(), client: c, clientset: cs}
}

func reconcileFn(t *testing.T, r *AutoPublishReconciler, ns, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

func listVersions(t *testing.T, cs versioned.Interface, ns string) []fv1.FunctionVersion {
	t.Helper()
	list, err := cs.CoreV1().FunctionVersions(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	return list.Items
}

func listVersionsFor(t *testing.T, cs versioned.Interface, ns, fnName string) []fv1.FunctionVersion {
	t.Helper()
	var out []fv1.FunctionVersion
	for _, v := range listVersions(t, cs, ns) {
		if v.Spec.FunctionName == fnName {
			out = append(out, v)
		}
	}
	return out
}

func readyFn(name, ns, pkgName string, mode fv1.VersioningMode) *fv1.Function {
	fn := makeFunction(name, ns, pkgName)
	fn.Spec.Versioning = &fv1.VersioningConfig{Mode: mode}
	return fn
}

// TestAutoPublishReconcile_NilVersioningNoOp: a Function that never opted
// into RFC-0025 versioning (Spec.Versioning == nil, the zero value) must
// never mint a version, however runtime-affecting-looking its state.
func TestAutoPublishReconcile_NilVersioningNoOp(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg") // Versioning left nil

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	r := newAutopublishReconciler(t, cs, fn)

	reconcileFn(t, r, ns, "fn")

	assert.Empty(t, listVersions(t, cs, ns))
}

// TestAutoPublishReconcile_ManualModeNoOp: Mode "manual" opts a function
// OUT of the auto-publish loop -- only `fission fn publish` mints for it.
func TestAutoPublishReconcile_ManualModeNoOp(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeManual)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	r := newAutopublishReconciler(t, cs, fn)

	reconcileFn(t, r, ns, "fn")

	assert.Empty(t, listVersions(t, cs, ns))
}

// TestAutoPublishReconcile_EmptyModeProceedsAsAuto is the load-bearing gate
// case: the CRD defaults Mode to "auto" server-side, but a fake client never
// applies that default (and neither does a real apiserver until its
// defaulting round-trip completes). Mode == "" must be treated exactly like
// "auto", not skipped.
func TestAutoPublishReconcile_EmptyModeProceedsAsAuto(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")
	fn.Spec.Versioning = &fv1.VersioningConfig{} // Mode == ""

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	r := newAutopublishReconciler(t, cs, fn)

	reconcileFn(t, r, ns, "fn")

	versions := listVersions(t, cs, ns)
	require.Len(t, versions, 1)
	assert.Equal(t, "fn-v1", versions[0].Name)
}

// TestAutoPublishReconcile_FirstPublishMintsV1: no existing FunctionVersion
// means there is nothing to classify against -- always proceed straight to
// Publish (an explicit Mode: auto function's very first reconcile).
func TestAutoPublishReconcile_FirstPublishMintsV1(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	r := newAutopublishReconciler(t, cs, fn)

	reconcileFn(t, r, ns, "fn")

	versions := listVersions(t, cs, ns)
	require.Len(t, versions, 1)
	assert.Equal(t, "fn-v1", versions[0].Name)
	assert.Equal(t, int64(1), versions[0].Spec.Sequence)
}

// TestAutoPublishReconcile_AffectingChangeMintsNextVersion: a v1 already
// exists; the live spec has since gained a Secret reference (an AFFECTING
// field per RuntimeAffecting) and the package is build-ready -- Reconcile
// must mint v2.
func TestAutoPublishReconcile_AffectingChangeMintsNextVersion(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	first, err := Publish(t.Context(), cs, fn, "v1")
	require.NoError(t, err)
	require.True(t, first.Created)

	fn2 := fn.DeepCopy()
	fn2.Spec.Secrets = []fv1.SecretReference{{Name: "s1", Namespace: ns}}

	r := newAutopublishReconciler(t, cs, fn2)
	reconcileFn(t, r, ns, "fn")

	versions := listVersions(t, cs, ns)
	require.Len(t, versions, 2)
	names := []string{versions[0].Name, versions[1].Name}
	assert.ElementsMatch(t, []string{"fn-v1", "fn-v2"}, names)
}

// TestAutoPublishReconcile_PackagePendingDefersThenMintsAfterReady covers
// the non-blocking requeue path: while the referenced package is still
// building, Reconcile must return RequeueAfter (not an error, not a mint),
// and once the package transitions to succeeded a subsequent reconcile
// mints.
func TestAutoPublishReconcile_PackagePendingDefersThenMintsAfterReady(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusPending
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	r := newAutopublishReconciler(t, cs, fn)

	res, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "fn"}})
	require.NoError(t, err)
	assert.Equal(t, packageNotReadyRequeueInterval, res.RequeueAfter, "must defer via RequeueAfter, not block or error")
	assert.Empty(t, listVersions(t, cs, ns), "no version while the package is not build-ready")

	livePkg, err := cs.CoreV1().Packages(ns).Get(t.Context(), "pkg", metav1.GetOptions{})
	require.NoError(t, err)
	livePkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	_, err = cs.CoreV1().Packages(ns).Update(t.Context(), livePkg, metav1.UpdateOptions{})
	require.NoError(t, err)

	reconcileFn(t, r, ns, "fn")
	versions := listVersions(t, cs, ns)
	require.Len(t, versions, 1)
	assert.Equal(t, "fn-v1", versions[0].Name)
}

// TestAutoPublishReconcile_NonAffectingChangeNoMint: IdleTimeout is
// classified NOT-affecting (warm-capacity economics only) -- changing it
// alone must never mint a new version, regardless of package readiness.
func TestAutoPublishReconcile_NonAffectingChangeNoMint(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	first, err := Publish(t.Context(), cs, fn, "v1")
	require.NoError(t, err)
	require.True(t, first.Created)

	fn2 := fn.DeepCopy()
	fn2.Spec.IdleTimeout = new(42)

	r := newAutopublishReconciler(t, cs, fn2)
	reconcileFn(t, r, ns, "fn")

	assert.Len(t, listVersions(t, cs, ns), 1, "IdleTimeout-only change must not mint a new version")
}

// TestAutoPublishReconcile_PackageFanOutMapFunction drives the RFC-0025
// dependency axis: a Package shared by two Functions transitions into a
// build-ready state, the Watches map function must enqueue BOTH dependents
// (and skip a Function referencing a different package), and reconciling
// both mints one version each sharing the package's digest.
func TestAutoPublishReconcile_PackageFanOutMapFunction(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusSucceeded
	env := makeEnv("env", ns, "example.com/env:v1")
	fn1 := readyFn("fn1", ns, "pkg", fv1.VersioningModeAuto)
	fn2 := readyFn("fn2", ns, "pkg", fv1.VersioningModeAuto)
	other := readyFn("other", ns, "other-pkg", fv1.VersioningModeAuto) // different package

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn1, fn2, other)
	r := newAutopublishReconciler(t, cs, fn1, fn2, other)

	reqs := r.mapPackageToFunctions(t.Context(), pkg)
	names := make([]string, 0, len(reqs))
	for _, req := range reqs {
		names = append(names, req.Name)
	}
	assert.ElementsMatch(t, []string{"fn1", "fn2"}, names, "only functions referencing this package are enqueued")

	for _, req := range reqs {
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	v1 := listVersionsFor(t, cs, ns, "fn1")
	v2 := listVersionsFor(t, cs, ns, "fn2")
	require.Len(t, v1, 1)
	require.Len(t, v2, 1)
	assert.Equal(t, v1[0].Spec.PackageDigest, v2[0].Spec.PackageDigest, "both dependents mint sharing the package's digest")
	assert.NotEmpty(t, v1[0].Spec.PackageDigest)
}

// TestAutoPublishReconcile_LegacySnapshotRepointNoFalseRemint guards the
// idempotence normalization: the legacy (non-OCI) publish path repoints the
// snapshot's PackageRef at a version-owned copy ("fn-v1-pkg"), which would
// never DeepEqual the live spec's PackageRef ("pkg") without
// normalizedSnapshot restoring it first. A live spec that has NOT actually
// changed since that publish must not be misclassified as affecting.
func TestAutoPublishReconcile_LegacySnapshotRepointNoFalseRemint(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns) // legacy (non-OCI) path
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := readyFn("fn", ns, "pkg", fv1.VersioningModeAuto)

	cs := fakeversioned.NewSimpleClientset(pkg, env, fn)
	first, err := Publish(t.Context(), cs, fn, "v1")
	require.NoError(t, err)
	require.True(t, first.Created)
	require.Equal(t, "fn-v1-pkg", first.Version.Spec.Snapshot.Package.PackageRef.Name, "test premise: legacy publish repoints the snapshot")

	r := newAutopublishReconciler(t, cs, fn) // fn is unchanged from what was published
	reconcileFn(t, r, ns, "fn")

	assert.Len(t, listVersions(t, cs, ns), 1, "the legacy repoint alone must never re-mint")
}

// TestAutoPublishReconcile_NotFoundIsNotAnError: a Function deleted between
// the event firing and Reconcile running is not an error condition.
func TestAutoPublishReconcile_NotFoundIsNotAnError(t *testing.T) {
	t.Parallel()
	cs := fakeversioned.NewSimpleClientset()
	r := newAutopublishReconciler(t, cs)

	_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "missing"}})
	assert.NoError(t, err)
}
