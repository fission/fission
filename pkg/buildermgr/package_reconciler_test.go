// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	versioned "github.com/fission/fission/pkg/generated/clientset/versioned"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// newFissionFake builds a fission fake clientset that supports UpdateStatus on
// our CRDs. NewClientset's field-managed tracker can't apply a status update to
// the Package type — no structured-merge schema is registered for fission types
// in unit tests — so these tests use the simple object tracker instead.
func newFissionFake(objs ...runtime.Object) versioned.Interface {
	return fClient.NewSimpleClientset(objs...) //nolint:staticcheck // see doc comment above
}

func sourcePkg(name string, status fv1.BuildStatus) *fv1.Package {
	p := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "go", Namespace: "default"},
			Source:      fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://example/src.zip"},
		},
	}
	p.Status.BuildStatus = status
	return p
}

// TestBuildTriggerPredicate pins the predicate that keeps the package
// reconciler from re-triggering itself: only a Create or a BuildStatus → pending
// transition enqueues a reconcile, so the reconciler's own running/succeeded/
// failed/none status writes are dropped.
func TestBuildTriggerPredicate(t *testing.T) {
	p := buildTriggerPredicate()

	assert.True(t, p.Create(event.CreateEvent{Object: sourcePkg("p", "")}),
		"Create must always enqueue (initial status / resume)")
	assert.False(t, p.Delete(event.DeleteEvent{Object: sourcePkg("p", fv1.BuildStatusSucceeded)}),
		"Delete must be dropped (no builder state to tear down)")
	assert.False(t, p.Generic(event.GenericEvent{Object: sourcePkg("p", fv1.BuildStatusPending)}),
		"Generic must be dropped")

	cases := []struct {
		name     string
		old, new fv1.BuildStatus
		want     bool
	}{
		{"into pending from empty", "", fv1.BuildStatusPending, true},
		{"into pending from succeeded (rebuild trigger)", fv1.BuildStatusSucceeded, fv1.BuildStatusPending, true},
		{"into pending from failed (retrigger)", fv1.BuildStatusFailed, fv1.BuildStatusPending, true},
		{"self write pending->running", fv1.BuildStatusPending, fv1.BuildStatusRunning, false},
		{"self write running->succeeded", fv1.BuildStatusRunning, fv1.BuildStatusSucceeded, false},
		{"self write running->failed", fv1.BuildStatusRunning, fv1.BuildStatusFailed, false},
		{"self write empty->none", "", fv1.BuildStatusNone, false},
		{"already pending, no transition", fv1.BuildStatusPending, fv1.BuildStatusPending, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Update(event.UpdateEvent{
				ObjectOld: sourcePkg("p", tc.old),
				ObjectNew: sourcePkg("p", tc.new),
			})
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSetInitialBuildStatus covers the derivation of a package's initial status
// from its spec archives: a source archive needs a build (pending), a
// deployment archive does not (none), and an empty spec is unbuildable (failed).
func TestSetInitialBuildStatus(t *testing.T) {
	deployPkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "default"},
		Spec: fv1.PackageSpec{
			Deployment: fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://example/deploy.zip"},
		},
	}
	emptyPkg := &fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"}}
	// An OCI-only deployment archive must behave exactly like a tarball
	// deployment archive: nothing to build (RFC-0001; Archive.IsEmpty is the
	// load-bearing check).
	ociPkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "oci", Namespace: "default"},
		Spec: fv1.PackageSpec{
			Deployment: fv1.Archive{Type: fv1.ArchiveTypeOCI, OCI: &fv1.OCIArchive{Image: "ghcr.io/example/hello-code:v1"}},
		},
	}

	cases := []struct {
		name string
		pkg  *fv1.Package
		want fv1.BuildStatus
	}{
		{"source archive -> pending", sourcePkg("src", ""), fv1.BuildStatusPending},
		{"deployment archive -> none", deployPkg, fv1.BuildStatusNone},
		{"oci deployment archive -> none", ociPkg, fv1.BuildStatusNone},
		{"empty spec -> failed", emptyPkg, fv1.BuildStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFissionFake(tc.pkg)
			out, err := setInitialBuildStatus(t.Context(), fc, tc.pkg)
			require.NoError(t, err)
			assert.Equal(t, tc.want, out.Status.BuildStatus)
		})
	}
}

func newTestPackageReconciler(t *testing.T, fc versioned.Interface, crObjs ...client.Object) *PackageReconciler {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(crObjs...).
		WithStatusSubresource(&fv1.Package{}).
		Build()
	return &PackageReconciler{
		logger:          loggerfactory.GetLogger(),
		client:          c,
		fissionClient:   fc,
		nsResolver:      utils.DefaultNSResolver(),
		podPollInterval: builderPodPollInterval,
	}
}

// TestPackageReconcileGate exercises the BuildStatus gate that decides whether a
// package is initialised, built, or left alone.
func TestPackageReconcileGate(t *testing.T) {
	req := func(name string) ctrl.Request {
		return ctrl.Request{NamespacedName: client.ObjectKey{Name: name, Namespace: "default"}}
	}

	t.Run("empty status writes initial pending", func(t *testing.T) {
		pkg := sourcePkg("init", "")
		fc := newFissionFake(pkg)
		r := newTestPackageReconciler(t, fc, sourcePkg("init", ""))

		res, err := r.Reconcile(t.Context(), req("init"))
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)

		got, err := fc.CoreV1().Packages("default").Get(t.Context(), "init", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, fv1.BuildStatusPending, string(got.Status.BuildStatus),
			"empty-status source package must be initialised to pending")
	})

	t.Run("succeeded is terminal noop", func(t *testing.T) {
		pkg := sourcePkg("done", fv1.BuildStatusSucceeded)
		fc := newFissionFake(pkg)
		r := newTestPackageReconciler(t, fc, sourcePkg("done", fv1.BuildStatusSucceeded))

		res, err := r.Reconcile(t.Context(), req("done"))
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)

		got, err := fc.CoreV1().Packages("default").Get(t.Context(), "done", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, fv1.BuildStatusSucceeded, string(got.Status.BuildStatus), "terminal status must be left untouched")
	})

	t.Run("pending with missing environment fails terminally", func(t *testing.T) {
		pkg := sourcePkg("noenv", fv1.BuildStatusPending)
		fc := newFissionFake(pkg)
		r := newTestPackageReconciler(t, fc, sourcePkg("noenv", fv1.BuildStatusPending))

		res, err := r.Reconcile(t.Context(), req("noenv"))
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res, "a missing environment is a terminal failure, not a requeue")

		got, err := fc.CoreV1().Packages("default").Get(t.Context(), "noenv", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, fv1.BuildStatusFailed, string(got.Status.BuildStatus))
		assert.Contains(t, got.Status.BuildLog, "environment does not exist")
	})

	t.Run("deleted package is a noop", func(t *testing.T) {
		fc := newFissionFake()
		r := newTestPackageReconciler(t, fc)
		res, err := r.Reconcile(t.Context(), req("ghost"))
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	})
}

// TestBuilderPodReady checks the readiness gate that the package build waits on.
func TestBuilderPodReady(t *testing.T) {
	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "go", Namespace: "default", ResourceVersion: "42"},
	}
	const builderNs = "default"
	podWith := func(name string, statuses ...apiv1.ContainerStatus) *apiv1.Pod {
		return &apiv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: builderNs,
				Labels: map[string]string{
					LABEL_ENV_NAME:            env.Name,
					LABEL_ENV_NAMESPACE:       builderNs,
					LABEL_ENV_RESOURCEVERSION: env.ResourceVersion,
				},
			},
			Status: apiv1.PodStatus{ContainerStatuses: statuses},
		}
	}

	cases := []struct {
		name string
		pods []*apiv1.Pod
		want bool
	}{
		{"no builder pod", nil, false},
		{"pod with no container status", []*apiv1.Pod{podWith("p")}, false},
		{"pod with unready container", []*apiv1.Pod{podWith("p", apiv1.ContainerStatus{Ready: false})}, false},
		{"pod with all containers ready", []*apiv1.Pod{podWith("p", apiv1.ContainerStatus{Ready: true})}, true},
		{"pod with one unready container", []*apiv1.Pod{podWith("p", apiv1.ContainerStatus{Ready: true}, apiv1.ContainerStatus{Ready: false})}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tc.pods))
			for _, p := range tc.pods {
				objs = append(objs, p)
			}
			r := &PackageReconciler{
				logger:           loggerfactory.GetLogger(),
				kubernetesClient: k8sfake.NewClientset(objs...),
			}
			pod, err := r.readyBuilderPod(t.Context(), env, builderNs)
			require.NoError(t, err)
			assert.Equal(t, tc.want, pod != nil)
		})
	}
}

// ociPkg builds a deploy-only OCI package — the shape that never builds, and
// so had no propagation at all before content-keyed re-stamping.
func ociPkg(name, digest, contentHash string) *fv1.Package {
	p := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", ResourceVersion: "100"},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "go", Namespace: "default"},
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  &fv1.OCIArchive{Image: "registry/app:v1", Digest: digest},
			},
		},
	}
	p.Status.BuildStatus = fv1.BuildStatusNone
	p.Status.ContentHash = contentHash
	return p
}

func fnForPkg(name, pkgName, pkgRV string, annotations map[string]string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
		Spec: fv1.FunctionSpec{Package: fv1.FunctionPackageRef{
			PackageRef: fv1.PackageRef{Name: pkgName, Namespace: "default", ResourceVersion: pkgRV},
		}},
	}
}

// TestReconcileContentChange covers the branch that makes a Git-applied
// package converge without the CLI (RFC-0029 §3). The seeding case is the one
// that protects the upgrade: an absent hash must never read as "changed".
func TestReconcileContentChange(t *testing.T) {
	t.Parallel()

	const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("unchanged content is a no-op", func(t *testing.T) {
		t.Parallel()
		pkg := ociPkg("p", digestA, "")
		pkg.Status.ContentHash = PackageContentHash(pkg.Spec)
		fn := fnForPkg("f", "p", "1", nil)
		fc := newFissionFake(pkg, fn)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		got, err := fc.CoreV1().Functions("default").Get(t.Context(), "f", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "1", got.Spec.Package.PackageRef.ResourceVersion, "no content change must not restamp")
	})

	t.Run("absent hash seeds without acting", func(t *testing.T) {
		t.Parallel()
		// Every package looks like this on the first reconcile after the
		// feature ships. Acting here would rebuild the whole cluster at once.
		pkg := ociPkg("p", digestA, "")
		fn := fnForPkg("f", "p", "1", nil)
		fc := newFissionFake(pkg, fn)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		got, err := fc.CoreV1().Functions("default").Get(t.Context(), "f", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "1", got.Spec.Package.PackageRef.ResourceVersion, "seeding must not restamp")

		gotPkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "p", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, PackageContentHash(pkg.Spec), gotPkg.Status.ContentHash, "seeding must record the hash")
	})

	t.Run("deploy-only content change restamps referencing functions", func(t *testing.T) {
		t.Parallel()
		// The digest-pinned-OCI-in-Git golden path: no build ever runs, so
		// this is the only thing that moves the pods.
		staleHash := PackageContentHash(ociPkg("p", digestA, "").Spec)
		pkg := ociPkg("p", digestB, staleHash)
		fn := fnForPkg("f", "p", "1", nil)
		other := fnForPkg("other", "different-pkg", "1", nil)
		fc := newFissionFake(pkg, fn, other)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		got, err := fc.CoreV1().Functions("default").Get(t.Context(), "f", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, pkg.ResourceVersion, got.Spec.Package.PackageRef.ResourceVersion,
			"a referencing function must be restamped onto the new package RV")

		untouched, err := fc.CoreV1().Functions("default").Get(t.Context(), "other", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "1", untouched.Spec.Package.PackageRef.ResourceVersion,
			"a function referencing a different package must not be touched")

		gotPkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "p", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, PackageContentHash(pkg.Spec), gotPkg.Status.ContentHash)
	})

	t.Run("no status write when the CLI already stamped the functions", func(t *testing.T) {
		t.Parallel()
		// This is the regression that broke RFC-0025 auto-publish in CI.
		// `fission fn update` writes the Package and THEN stamps referencing
		// Functions itself, so by the time this reconciles there is nothing to
		// do. Recording the hash anyway is a STATUS write, and a status write
		// bumps the Package's ResourceVersion — which the CLI copies into
		// every Function's PackageRef on the NEXT `fn update`, minting a
		// version for an update the classifier treats as non-affecting.
		staleHash := PackageContentHash(ociPkg("p", digestA, "").Spec)
		pkg := ociPkg("p", digestB, staleHash)
		// The function already points at this package's current RV, exactly as
		// the CLI leaves it.
		fn := fnForPkg("f", "p", pkg.ResourceVersion, nil)
		fc := newFissionFake(pkg, fn)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		gotPkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "p", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, staleHash, gotPkg.Status.ContentHash,
			"with nothing to re-stamp, the reconciler must not write status at all")
	})

	t.Run("opt-out annotation is honored on the content path", func(t *testing.T) {
		t.Parallel()
		staleHash := PackageContentHash(ociPkg("p", digestA, "").Spec)
		pkg := ociPkg("p", digestB, staleHash)
		fn := fnForPkg("f", "p", "1", map[string]string{fv1.PackageAutoFollowDisabledAnnotation: "true"})
		fc := newFissionFake(pkg, fn)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		got, err := fc.CoreV1().Functions("default").Get(t.Context(), "f", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "1", got.Spec.Package.PackageRef.ResourceVersion,
			"an opted-out function must keep its pinned package RV")
	})

	t.Run("source content change re-queues a build", func(t *testing.T) {
		t.Parallel()
		pkg := sourcePkg("s", fv1.BuildStatusSucceeded)
		pkg.Status.ContentHash = "sha256:stale"
		fc := newFissionFake(pkg)
		r := newTestPackageReconciler(t, fc, pkg)

		_, err := r.reconcileContentChange(t.Context(), pkg)
		require.NoError(t, err)

		got, err := fc.CoreV1().Packages("default").Get(t.Context(), "s", metav1.GetOptions{})
		require.NoError(t, err)
		assert.EqualValues(t, fv1.BuildStatusPending, got.Status.BuildStatus,
			"a source package whose content changed must re-enter the build queue")
	})
}
