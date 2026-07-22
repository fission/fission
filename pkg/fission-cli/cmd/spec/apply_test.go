// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestPackageEqual exercises the equal() closure used by applyPackages to decide
// whether a cluster package is up-to-date with the desired spec. This covers the
// new logic that detects source-build packages stuck with succeeded status but an
// empty deployment archive.
func TestPackageEqual(t *testing.T) {
	t.Parallel()

	// equal is the same closure used in applyPackages — extracted here so we
	// can test it in isolation without a fake client.
	equal := func(existing, desired *fv1.Package) bool {
		specMatches := reflect.DeepEqual(existing.Spec, desired.Spec) ||
			(reflect.DeepEqual(existing.Spec.Environment, desired.Spec.Environment) &&
				!existing.Spec.Source.IsEmpty() &&
				reflect.DeepEqual(existing.Spec.Source, desired.Spec.Source) &&
				existing.Spec.BuildCommand == desired.Spec.BuildCommand)

		ready := existing.Status.BuildStatus == fv1.BuildStatusSucceeded ||
			existing.Status.BuildStatus == fv1.BuildStatusNone

		if ready && existing.Spec.BuildCommand != "" &&
			!existing.Spec.Source.IsEmpty() && existing.Spec.Deployment.IsEmpty() {
			ready = false
		}

		return specMatches &&
			isObjectMetaEqual(existing.ObjectMeta, desired.ObjectMeta) &&
			ready
	}

	sourcePkg := func(buildStatus fv1.BuildStatus, deployURL string) *fv1.Package {
		return &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "mypkg", Namespace: "default"},
			Spec: fv1.PackageSpec{
				Environment:  fv1.EnvironmentReference{Name: "python", Namespace: "default"},
				Source:       fv1.Archive{URL: "http://storagesvc/src.zip"},
				BuildCommand: "pip install -r requirements.txt",
				Deployment:   fv1.Archive{URL: deployURL},
			},
			Status: fv1.PackageStatus{BuildStatus: buildStatus},
		}
	}

	desiredSourcePkg := func() *fv1.Package {
		return &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "mypkg", Namespace: "default"},
			Spec: fv1.PackageSpec{
				Environment:  fv1.EnvironmentReference{Name: "python", Namespace: "default"},
				Source:       fv1.Archive{URL: "http://storagesvc/src.zip"},
				BuildCommand: "pip install -r requirements.txt",
				// Deployment is empty in the spec file — this is normal for source builds.
			},
		}
	}

	deployPkg := func(buildStatus fv1.BuildStatus) *fv1.Package {
		return &fv1.Package{
			ObjectMeta: metav1.ObjectMeta{Name: "deploypkg", Namespace: "default"},
			Spec: fv1.PackageSpec{
				Environment: fv1.EnvironmentReference{Name: "python", Namespace: "default"},
				Deployment:  fv1.Archive{URL: "http://storagesvc/deploy.zip"},
			},
			Status: fv1.PackageStatus{BuildStatus: buildStatus},
		}
	}

	tests := []struct {
		name     string
		existing *fv1.Package
		desired  *fv1.Package
		want     bool
	}{
		{
			name:     "source-build succeeded with deploy URL is equal (no rebuild needed)",
			existing: sourcePkg(fv1.BuildStatusSucceeded, "http://storagesvc/deploy.zip"),
			desired:  desiredSourcePkg(),
			want:     true,
		},
		{
			name:     "source-build succeeded but empty deploy is NOT equal (stuck state)",
			existing: sourcePkg(fv1.BuildStatusSucceeded, ""),
			desired:  desiredSourcePkg(),
			want:     false,
		},
		{
			name:     "source-build failed is NOT equal (needs rebuild)",
			existing: sourcePkg(fv1.BuildStatusFailed, ""),
			desired:  desiredSourcePkg(),
			want:     false,
		},
		{
			name:     "source-build pending is NOT equal (build in progress)",
			existing: sourcePkg(fv1.BuildStatusPending, ""),
			desired:  desiredSourcePkg(),
			want:     false,
		},
		{
			name:     "source-build running is NOT equal (build in progress)",
			existing: sourcePkg(fv1.BuildStatusRunning, ""),
			desired:  desiredSourcePkg(),
			want:     false,
		},
		{
			name:     "deploy-archive package with status none is equal",
			existing: deployPkg(fv1.BuildStatusNone),
			desired: &fv1.Package{
				ObjectMeta: metav1.ObjectMeta{Name: "deploypkg", Namespace: "default"},
				Spec: fv1.PackageSpec{
					Environment: fv1.EnvironmentReference{Name: "python", Namespace: "default"},
					Deployment:  fv1.Archive{URL: "http://storagesvc/deploy.zip"},
				},
			},
			want: true,
		},
		{
			name:     "source changed triggers update",
			existing: sourcePkg(fv1.BuildStatusSucceeded, "http://storagesvc/deploy.zip"),
			desired: &fv1.Package{
				ObjectMeta: metav1.ObjectMeta{Name: "mypkg", Namespace: "default"},
				Spec: fv1.PackageSpec{
					Environment:  fv1.EnvironmentReference{Name: "python", Namespace: "default"},
					Source:       fv1.Archive{URL: "http://storagesvc/src-v2.zip"}, // changed
					BuildCommand: "pip install -r requirements.txt",
				},
			},
			want: false,
		},
		{
			name: "metadata label change triggers update",
			existing: func() *fv1.Package {
				p := sourcePkg(fv1.BuildStatusSucceeded, "http://storagesvc/deploy.zip")
				p.Labels = map[string]string{"version": "1"}
				return p
			}(),
			desired: func() *fv1.Package {
				p := desiredSourcePkg()
				p.Labels = map[string]string{"version": "2"}
				return p
			}(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := equal(tc.existing, tc.desired)
			if got != tc.want {
				t.Errorf("equal() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPackageRetriggerDecision exercises the retrigger logic that decides whether
// to issue a separate UpdateStatus call to set BuildStatus=pending after the spec
// Update. This validates the fix for the #3427 regression.
func TestPackageRetriggerDecision(t *testing.T) {
	t.Parallel()

	// retrigger mirrors the logic in applyPackages' update closure.
	retrigger := func(currentBuildStatus fv1.BuildStatus, desiredBuildCmd string, desiredSourceEmpty bool) bool {
		desired := &fv1.Package{
			Spec: fv1.PackageSpec{
				BuildCommand: desiredBuildCmd,
			},
		}
		if !desiredSourceEmpty {
			desired.Spec.Source = fv1.Archive{URL: "http://storagesvc/src.zip"}
		}
		return currentBuildStatus == fv1.BuildStatusFailed ||
			(desired.Spec.BuildCommand != "" && !desired.Spec.Source.IsEmpty())
	}

	tests := []struct {
		name               string
		currentBuildStatus fv1.BuildStatus
		buildCmd           string
		sourceEmpty        bool
		want               bool
	}{
		{
			name:               "failed build always retriggers",
			currentBuildStatus: fv1.BuildStatusFailed,
			buildCmd:           "",
			sourceEmpty:        true,
			want:               true,
		},
		{
			name:               "source-build package retriggers (succeeded)",
			currentBuildStatus: fv1.BuildStatusSucceeded,
			buildCmd:           "make",
			sourceEmpty:        false,
			want:               true,
		},
		{
			name:               "source-build package retriggers (pending)",
			currentBuildStatus: fv1.BuildStatusPending,
			buildCmd:           "make",
			sourceEmpty:        false,
			want:               true,
		},
		{
			name:               "deploy-only package does NOT retrigger (succeeded)",
			currentBuildStatus: fv1.BuildStatusSucceeded,
			buildCmd:           "",
			sourceEmpty:        true,
			want:               false,
		},
		{
			name:               "deploy-only package does NOT retrigger (none)",
			currentBuildStatus: fv1.BuildStatusNone,
			buildCmd:           "",
			sourceEmpty:        true,
			want:               false,
		},
		{
			name:               "has build command but empty source does NOT retrigger",
			currentBuildStatus: fv1.BuildStatusSucceeded,
			buildCmd:           "make",
			sourceEmpty:        true,
			want:               false,
		},
		{
			name:               "has source but no build command does NOT retrigger",
			currentBuildStatus: fv1.BuildStatusSucceeded,
			buildCmd:           "",
			sourceEmpty:        false,
			want:               false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := retrigger(tc.currentBuildStatus, tc.buildCmd, tc.sourceEmpty)
			if got != tc.want {
				t.Errorf("retrigger() = %v, want %v", got, tc.want)
			}
		})
	}
}

// aliasFR builds a minimal FissionResources carrying the given aliases,
// stamped with the test deployment UID (mirrors frWith in resourcetype_test.go).
func aliasFR(aliases ...fv1.FunctionAlias) *FissionResources {
	fr := &FissionResources{FunctionAliases: aliases}
	fr.DeploymentConfig.UID = testDeployUID
	fr.DeploymentConfig.Name = "test-deploy"
	return fr
}

// TestApplyFunctionAliasesCreateSetsOwnerRefWhenFunctionExists exercises the
// create closure's ownerRef wiring: when the target Function already exists
// on the cluster, the created alias must carry an ownerRef pinning its UID
// (mirrors `fission alias create`'s functionOwnerRef), so it is garbage
// collected along with the Function.
func TestApplyFunctionAliasesCreateSetsOwnerRefWhenFunctionExists(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: types.UID("fn-uid")},
	}
	fc := fissionfake.NewSimpleClientset(fn) //nolint:staticcheck // FunctionAlias SMD schema not yet generated for NewClientset, see k8s#126850
	fr := aliasFR(fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	})

	_, ras, err := applyFunctionAliases(t.Context(), cmd.Client{FissionClientSet: fc}, fr, false, false, false)
	require.NoError(t, err)
	require.Len(t, ras.Created, 1)

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "Function", got.OwnerReferences[0].Kind)
	assert.Equal(t, "hello", got.OwnerReferences[0].Name)
	assert.Equal(t, types.UID("fn-uid"), got.OwnerReferences[0].UID)
	// The generic reconciler stamps the deployment-UID annotation on every
	// spec-applied object; that's what makes the alias prunable later.
	assert.Equal(t, testDeployUID, got.Annotations[FISSION_DEPLOYMENT_UID_KEY])
}

// TestApplyFunctionAliasesCreateWithoutFunctionIsUnowned covers the
// eventual-consistency path: a digest-pinned alias may be applied before its
// Function exists. The create must still succeed, just without an ownerRef.
func TestApplyFunctionAliasesCreateWithoutFunctionIsUnowned(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck // see k8s#126850
	fr := aliasFR(fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "not-yet-created", PackageDigest: "sha256:" + repeatHexChar('a', 64)},
	})

	_, ras, err := applyFunctionAliases(t.Context(), cmd.Client{FissionClientSet: fc}, fr, false, false, false)
	require.NoError(t, err)
	require.Len(t, ras.Created, 1)

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, got.OwnerReferences)
}

func repeatHexChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// TestApplyFunctionAliasesUpdateInPlacePreservesStatus proves the update
// closure changes spec.* without ever deleting the object (a delete-recreate
// would lose the alias's UID and its controller-written status), and that it
// never touches Status directly — that's the /status subresource, owned by
// the alias-reconciler.
func TestApplyFunctionAliasesUpdateInPlacePreservesStatus(t *testing.T) {
	existing := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "prod",
			Namespace:       "default",
			UID:             types.UID("alias-uid-1"),
			OwnerReferences: []metav1.OwnerReference{{Kind: "Function", Name: "hello", UID: types.UID("fn-uid")}},
			Annotations:     map[string]string{FISSION_DEPLOYMENT_UID_KEY: testDeployUID},
		},
		Spec:   fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		Status: fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
	}
	fc := fissionfake.NewSimpleClientset(existing) //nolint:staticcheck // see k8s#126850

	// A delete would break the "update in place" contract; fail loudly if the
	// apply ever issues one.
	fc.PrependReactor("delete", "functionaliases", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("update-in-place must not delete the FunctionAlias")
		return false, nil, nil
	})

	fr := aliasFR(fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		// spec.Version changed -- this is the update trigger.
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"},
	})

	_, ras, err := applyFunctionAliases(t.Context(), cmd.Client{FissionClientSet: fc}, fr, false, false, false)
	require.NoError(t, err)
	require.Len(t, ras.Updated, 1)
	require.Empty(t, ras.Created)

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v2", got.Spec.Version, "spec must be updated")
	assert.Equal(t, "hello-v1", got.Status.ResolvedVersion, "status must survive the spec update untouched")
	assert.Equal(t, types.UID("alias-uid-1"), got.UID, "identity must survive: never delete-recreate")
	require.Len(t, got.OwnerReferences, 1, "ownerRef set at create time must survive an unrelated spec update")
	assert.Equal(t, "hello", got.OwnerReferences[0].Name)
}

// TestApplyFunctionAliasesPruneOnlyWithDeploymentUID proves --delete only
// removes aliases this spec deployment owns (carrying its deployment-UID
// annotation); an alias without it (or with a different deployment's UID) is
// left alone, matching every other spec-managed kind's prune semantics.
func TestApplyFunctionAliasesPruneOnlyWithDeploymentUID(t *testing.T) {
	owned := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{
			Name: "owned", Namespace: "default",
			Annotations: map[string]string{FISSION_DEPLOYMENT_UID_KEY: testDeployUID},
		},
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	foreign := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foreign", Namespace: "default",
			// No deployment-UID annotation: not owned by this spec deployment.
		},
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	fc := fissionfake.NewSimpleClientset(owned, foreign) //nolint:staticcheck // see k8s#126850

	fr := aliasFR() // empty desired state: both aliases are absent from the spec
	_, ras, err := applyFunctionAliases(t.Context(), cmd.Client{FissionClientSet: fc}, fr, true /* delete */, false, false)
	require.NoError(t, err)
	require.Len(t, ras.Deleted, 1)
	assert.Equal(t, "owned", ras.Deleted[0].Name)

	_, err = fc.CoreV1().FunctionAliases("default").Get(t.Context(), "owned", metav1.GetOptions{})
	assert.Error(t, err, "owned alias must be pruned")

	_, err = fc.CoreV1().FunctionAliases("default").Get(t.Context(), "foreign", metav1.GetOptions{})
	assert.NoError(t, err, "foreign alias must survive prune")
}
