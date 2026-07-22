// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

const digest64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func makeEnv(name, ns, image string) *fv1.Environment {
	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.EnvironmentSpec{
			Runtime: fv1.Runtime{Image: image},
		},
	}
	env.Generation = 3
	return env
}

// makeDeployPackage returns a deploy-archive package with BuildStatus none
// and a recorded sha256 checksum on the deployment archive — the ordinary
// "legacy" (non-OCI) case.
func makeDeployPackage(name, ns string) *fv1.Package {
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "env", Namespace: ns},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeUrl,
				URL:  "http://storagesvc/v1/archive?id=abc",
				Checksum: fv1.Checksum{
					Type: fv1.ChecksumTypeSHA256,
					Sum:  digest64,
				},
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusNone},
	}
}

// makeOCIPackage returns an OCI-digest-pinned deploy package — the non-legacy
// case, which never needs a snapshot Package copy.
func makeOCIPackage(name, ns string) *fv1.Package {
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "env", Namespace: ns},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI: &fv1.OCIArchive{
					Image:  "registry/example:v1",
					Digest: "sha256:" + digest64,
				},
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusSucceeded},
	}
}

// makeLiteralPackage returns a package whose deployment archive is a literal
// byte payload with no checksum recorded (util.go:38-40's case).
func makeLiteralPackage(name, ns string, literal []byte) *fv1.Package {
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "env", Namespace: ns},
			Deployment: fv1.Archive{
				Type:    fv1.ArchiveTypeLiteral,
				Literal: literal,
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusNone},
	}
}

func makeFunction(name, ns, pkgName string) *fv1.Function {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "env", Namespace: ns},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{Name: pkgName, Namespace: ns},
			},
		},
	}
	fn.Generation = 1
	return fn
}

func TestPublish_FirstPublishCreatesV1(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "first publish")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Created)
	require.NotNil(t, res.Version)
	assert.Equal(t, "fn-v1", res.Version.Name)
	assert.Equal(t, int64(1), res.Version.Spec.Sequence)
	assert.Equal(t, fn.UID, res.Version.Spec.FunctionUID)
	assert.Equal(t, fn.Generation, res.Version.Spec.FunctionGeneration)
	assert.Equal(t, "sha256:"+digest64, res.Version.Spec.PackageDigest)
	assert.Equal(t, env.Generation, res.Version.Spec.EnvObservedGeneration)
	assert.Equal(t, "example.com/env:v1", res.Version.Spec.EnvRuntimeImage)
	assert.False(t, res.Version.Spec.PublishedAt.IsZero())
	assert.Equal(t, "first publish", res.Version.Annotations[DescriptionAnnotation])
	assert.Equal(t, "fn", res.Version.Labels[fv1.VersionFunctionNameLabel])
	assert.Equal(t, string(fn.UID), res.Version.Labels[fv1.VersionFunctionUIDLabel])
	require.Len(t, res.Version.OwnerReferences, 1)
	assert.Equal(t, "Function", res.Version.OwnerReferences[0].Kind)
	assert.Equal(t, fn.Name, res.Version.OwnerReferences[0].Name)
	assert.Equal(t, fn.UID, res.Version.OwnerReferences[0].UID)

	// Legacy (non-OCI) path: the snapshot repoints at the version-owned copy
	// and records the original name for idempotence normalization.
	assert.Equal(t, "fn-v1-pkg", res.Version.Spec.Snapshot.Package.PackageRef.Name)
	assert.Equal(t, "pkg", res.Version.Annotations[SourcePackageAnnotation])

	snapPkg, err := cl.CoreV1().Packages(ns).Get(t.Context(), "fn-v1-pkg", metav1.GetOptions{})
	require.NoError(t, err, "legacy path must create the snapshot package")
	assert.Equal(t, pkg.Spec, snapPkg.Spec)
	require.Len(t, snapPkg.OwnerReferences, 1)
	assert.Equal(t, "FunctionVersion", snapPkg.OwnerReferences[0].Kind)
	assert.Equal(t, "fn-v1", snapPkg.OwnerReferences[0].Name)
}

func TestPublish_SecondPublishUnchangedIsIdempotent(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	first, err := Publish(t.Context(), cl, fn, "first")
	require.NoError(t, err)
	require.True(t, first.Created)

	second, err := Publish(t.Context(), cl, fn, "second call, same spec")
	require.NoError(t, err)
	assert.False(t, second.Created)
	assert.Equal(t, first.Version.Name, second.Version.Name)
	assert.Equal(t, first.Version.Spec.Sequence, second.Version.Spec.Sequence)

	list, err := cl.CoreV1().FunctionVersions(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "idempotent publish must not create a second version")
}

func TestPublish_SpecChangeCreatesV2(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	first, err := Publish(t.Context(), cl, fn, "v1")
	require.NoError(t, err)
	require.True(t, first.Created)

	// Mutate the live function spec (a runtime-affecting change) and publish
	// again.
	fn = fn.DeepCopy()
	fn.Spec.Secrets = []fv1.SecretReference{{Name: "s1", Namespace: ns}}

	second, err := Publish(t.Context(), cl, fn, "v2")
	require.NoError(t, err)
	require.True(t, second.Created)
	assert.Equal(t, "fn-v2", second.Version.Name)
	assert.Equal(t, int64(2), second.Version.Spec.Sequence)
	assert.NotEqual(t, first.Version.Name, second.Version.Name)

	list, err := cl.CoreV1().FunctionVersions(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
}

func TestPublish_PackagePendingReturnsErrPackageNotReady(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	pkg.Status.BuildStatus = fv1.BuildStatusPending
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, ErrPackageNotReady), "error must wrap ErrPackageNotReady: %v", err)
}

func TestPublish_PackageBuildStatusNoneIsReady(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns) // BuildStatusNone
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "")
	require.NoError(t, err)
	assert.True(t, res.Created)
}

func TestPublish_OCIPackageSkipsSnapshotCopy(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeOCIPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "")
	require.NoError(t, err)
	require.True(t, res.Created)

	assert.Equal(t, "sha256:"+digest64, res.Version.Spec.PackageDigest)
	assert.Equal(t, "pkg", res.Version.Spec.Snapshot.Package.PackageRef.Name, "OCI-digest-backed publish must not repoint the PackageRef")
	assert.NotContains(t, res.Version.Annotations, SourcePackageAnnotation)

	pkgs, err := cl.CoreV1().Packages(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, pkgs.Items, 1, "OCI path must not create a snapshot package copy")
}

func TestPublish_LiteralArchiveDigestOverBytes(t *testing.T) {
	t.Parallel()
	ns := "default"
	literal := []byte("package contents with no recorded checksum")
	pkg := makeLiteralPackage("pkg", ns, literal)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "")
	require.NoError(t, err)
	require.True(t, res.Created)

	wantDigest, err := PackageDigest(pkg)
	require.NoError(t, err)
	assert.Equal(t, wantDigest, res.Version.Spec.PackageDigest)

	// Legacy path still applies: a literal archive is not OCI-backed.
	assert.Equal(t, "fn-v1-pkg", res.Version.Spec.Snapshot.Package.PackageRef.Name)
	snapPkg, err := cl.CoreV1().Packages(ns).Get(t.Context(), "fn-v1-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, literal, snapPkg.Spec.Deployment.Literal)
}

func TestPublish_EnvObservationRecorded(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/runtime:v9")
	env.Generation = 7
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	res, err := Publish(t.Context(), cl, fn, "")
	require.NoError(t, err)
	require.True(t, res.Created)
	assert.Equal(t, int64(7), res.Version.Spec.EnvObservedGeneration)
	assert.Equal(t, "example.com/runtime:v9", res.Version.Spec.EnvRuntimeImage)
}

func TestPublish_LegacyPackageMissingSnapshotSelfHeals(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	first, err := Publish(t.Context(), cl, fn, "v1")
	require.NoError(t, err)
	require.True(t, first.Created)

	// Simulate the crash-between-steps trace: delete the snapshot package
	// out from under the version.
	require.NoError(t, cl.CoreV1().Packages(ns).Delete(t.Context(), "fn-v1-pkg", metav1.DeleteOptions{}))

	res, err := Publish(t.Context(), cl, fn, "v1 retry")
	require.NoError(t, err)
	assert.False(t, res.Created, "idempotence still matches; self-heal must not mint a new version")
	assert.Equal(t, "fn-v1", res.Version.Name)

	snapPkg, err := cl.CoreV1().Packages(ns).Get(t.Context(), "fn-v1-pkg", metav1.GetOptions{})
	require.NoError(t, err, "self-heal must re-create the missing snapshot package")
	assert.Equal(t, pkg.Spec, snapPkg.Spec)
	require.Len(t, snapPkg.OwnerReferences, 1)
	assert.Equal(t, "fn-v1", snapPkg.OwnerReferences[0].Name)
}

func TestPublish_SnapshotPackageNameCollisionRejected(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	// A foreign Package with no owner reference already occupies the name
	// Publish will pick for fn's v1 snapshot copy ("fn-v1-pkg") — a plain
	// name collision, not a Fission-owned object left behind by a crash.
	foreign := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-v1-pkg", Namespace: ns},
		Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: "env", Namespace: ns}},
	}

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn, foreign)

	res, err := Publish(t.Context(), cl, fn, "v1")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "fn-v1-pkg", "error must name the colliding snapshot package")
}

func TestEnsureSnapshotPackage_AlreadyExistsWithMatchingOwnerAdopts(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	version := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-v1", Namespace: ns, UID: types.UID("fn-v1-uid")},
		Spec: fv1.FunctionVersionSpec{
			Snapshot: fv1.FunctionSpec{
				Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "fn-v1-pkg", Namespace: ns}},
			},
		},
	}

	first, err := ensureSnapshotPackage(t.Context(), cl, fn, version, pkg)
	require.NoError(t, err)
	require.NotNil(t, first)

	// Crash-recovery re-publish: the caller re-drives ensureSnapshotPackage
	// for the same version (e.g. the first Create's response was lost). The
	// object already exists and is already owned by this exact version, so
	// it must be adopted, not rejected as a collision.
	second, err := ensureSnapshotPackage(t.Context(), cl, fn, version, pkg)
	require.NoError(t, err, "a package already owned by this exact version must be adopted")
	assert.Equal(t, first.Name, second.Name)
	require.Len(t, second.OwnerReferences, 1)
	assert.Equal(t, "fn-v1", second.OwnerReferences[0].Name)
}

func TestPublish_ConcurrentAlreadyExistsRetriesOnce(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)
	tracker := cl.Tracker()

	// A concurrent publisher creates fn-v1 first, at the moment our create
	// call would land — force exactly one AlreadyExists by seeding the
	// tracker directly. Never call the typed client from inside a reactor:
	// Fake.Invokes holds an RWMutex for the whole dispatch, and a nested
	// typed-client call from the reactor it's running self-deadlocks on it.
	seeded := false
	cl.PrependReactor("create", "functionversions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if seeded {
			return false, nil, nil // let subsequent attempts go through the tracker normally
		}
		seeded = true

		create := action.(k8stesting.CreateAction)
		winner := create.GetObject().(*fv1.FunctionVersion).DeepCopy()
		// Keep the SourcePackageAnnotation Publish already computed (the
		// legacy-path repoint) — only override the description — so the
		// retry's idempotence normalization still lines up.
		if winner.Annotations == nil {
			winner.Annotations = map[string]string{}
		}
		winner.Annotations[DescriptionAnnotation] = "concurrent winner"
		if err := tracker.Add(winner); err != nil {
			return true, nil, fmt.Errorf("test setup: seeding concurrent winner: %w", err)
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "fission.io", Resource: "functionversions"}, winner.Name)
	})

	res, err := Publish(t.Context(), cl, fn, "our publish")
	require.NoError(t, err)
	require.NotNil(t, res)
	// Our own publish call recomputes idempotence after the re-list and
	// finds the concurrent winner's snapshot matches (same fn spec), so it
	// reports the existing version rather than erroring or duplicating.
	assert.Equal(t, "fn-v1", res.Version.Name)
	assert.Equal(t, "concurrent winner", res.Version.Annotations[DescriptionAnnotation])

	list, err := cl.CoreV1().FunctionVersions(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "exactly one version must exist after the race resolves")
}

func TestPublish_ConcurrentAlreadyExistsTwiceReturnsError(t *testing.T) {
	t.Parallel()
	ns := "default"
	pkg := makeDeployPackage("pkg", ns)
	env := makeEnv("env", ns, "example.com/env:v1")
	fn := makeFunction("fn", ns, "pkg")

	cl := fakeversioned.NewSimpleClientset(pkg, env, fn)

	// Every create attempt reports AlreadyExists, and nothing ever actually
	// lands, so retrying can never converge: the second attempt must
	// surface the error instead of looping.
	cl.PrependReactor("create", "functionversions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		name := create.GetObject().(*fv1.FunctionVersion).Name
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "fission.io", Resource: "functionversions"}, name)
	})

	res, err := Publish(t.Context(), cl, fn, "")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, apierrors.IsAlreadyExists(err), "error must surface the AlreadyExists failure: %v", err)
}
