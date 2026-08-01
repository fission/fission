// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestVersioningAutoHint guards the UX-review breadcrumb (F8): `fn update`
// on a function with versioning mode auto must point the caller at `fn
// versions` since the new version is minted asynchronously (once the build
// succeeds), not synchronously with the update that just printed.
func TestVersioningAutoHint(t *testing.T) {
	t.Run("no versioning config yields no hint", func(t *testing.T) {
		fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello"}}
		assert.Empty(t, versioningAutoHint(fn))
	})

	t.Run("manual mode yields no hint", func(t *testing.T) {
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "hello"},
			Spec:       fv1.FunctionSpec{Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningModeManual}},
		}
		assert.Empty(t, versioningAutoHint(fn))
	})

	t.Run("auto mode names the function and the versions command", func(t *testing.T) {
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "hello"},
			Spec:       fv1.FunctionSpec{Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto}},
		}
		hint := versioningAutoHint(fn)
		assert.Contains(t, hint, "versioning=auto")
		assert.Contains(t, hint, "fission fn versions --name hello")
	})
}

// TestUpdateRepointsPackage pins the contract that `fn update --pkg <other>`
// moves the function's PackageRef, including its ResourceVersion.
//
// The trap this guards: UpdatePackage's no-op path returns the CALLER'S OWN
// ObjectMeta (nothing was written, so there is no new ResourceVersion to
// report), and re-pointing at an existing package takes exactly that path.
// Reading "returned meta == passed-in meta" as "nothing to do" therefore
// skips the re-stamp and leaves the function serving its old package — a
// failure only the integration suite caught, at ~13 minutes a run.
func TestUpdateRepointsPackage(t *testing.T) {
	pkgA := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "pkg-a", Namespace: "default", ResourceVersion: "100"},
		Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"}},
	}
	pkgB := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "pkg-b", Namespace: "default", ResourceVersion: "200"},
		Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"}},
	}
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{Name: "pkg-a", Namespace: "default", ResourceVersion: "100"},
			},
		},
	}

	fc := fissionfake.NewSimpleClientset(fn, pkgA, pkgB) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	t.Cleanup(cmd.ResetClientsetForTest)

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnPackageName, "pkg-b")
	require.NoError(t, Update(in))

	got, err := fc.CoreV1().Functions("default").Get(t.Context(), "hello", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "pkg-b", got.Spec.Package.PackageRef.Name,
		"--pkg must re-point the function at the named package")
	assert.Equal(t, "200", got.Spec.Package.PackageRef.ResourceVersion,
		"the stamp must carry the new package's ResourceVersion, or nothing recycles the pods")
}

// TestUpdateLeavesStampAloneWhenPackageUntouched is the other half of the
// contract, and the direction that is expensive to get wrong.
//
// An update that names no package flag (`--versioning auto`, a label edit)
// must NOT refresh PackageRef.ResourceVersion. The package's live
// ResourceVersion drifts ahead of the function's stamp on every controller
// STATUS write — buildermgr records a content hash there — so copying it
// unconditionally turns an unrelated update into a FunctionSpec change and
// mints an RFC-0025 version for content that did not move.
func TestUpdateLeavesStampAloneWhenPackageUntouched(t *testing.T) {
	// The package has been written since the function was stamped: status
	// writes carried it from 100 to 140.
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "pkg-a", Namespace: "default", ResourceVersion: "140"},
		Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"}},
	}
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{Name: "pkg-a", Namespace: "default", ResourceVersion: "100"},
			},
		},
	}

	fc := fissionfake.NewSimpleClientset(fn, pkg) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	t.Cleanup(cmd.ResetClientsetForTest)

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnVersioning, "manual")
	require.NoError(t, Update(in))

	got, err := fc.CoreV1().Functions("default").Get(t.Context(), "hello", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "100", got.Spec.Package.PackageRef.ResourceVersion,
		"an update that touches no package must leave the stamp alone, or it mints a version for nothing")
}
