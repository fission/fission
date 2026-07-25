// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"strings"
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

// TestGetVersionRendersSnapshotPackage covers the legacy (non-OCI-digest)
// publish path: versioning.Publish repoints Snapshot.Package.PackageRef at
// the version-owned snapshot Package, so `fn get --version` must render that
// package's Literal, not the live function's package.
func TestGetVersionRendersSnapshotPackage(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Namespace: "default", Name: "hello-pkg"}},
		},
	}
	livePkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
		Spec:       fv1.PackageSpec{Deployment: fv1.Archive{Literal: []byte("live content")}},
	}
	snapshotPkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1-pkg", Namespace: "default"},
		Spec:       fv1.PackageSpec{Deployment: fv1.Archive{Literal: []byte("v1 snapshot content")}},
	}
	version := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello",
			Sequence:     1,
			Snapshot: fv1.FunctionSpec{
				Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Namespace: "default", Name: "hello-v1-pkg"}},
			},
		},
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fn, livePkg, snapshotPkg, version),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnTestVersion, "hello-v1")

	out := captureStdout(t, func() error { return Get(in) })
	assert.Equal(t, "v1 snapshot content", out)
	assert.False(t, strings.Contains(out, "live content"))
}

// TestGetVersionRendersOriginalPackageForOCIDigest covers the OCI-digest
// path: Publish never repoints the ref for a digest-pinned package (there is
// no version-owned copy), so `fn get --version` fetches the ORIGINAL package
// by name -- correct because the digest already content-addresses it.
func TestGetVersionRendersOriginalPackageForOCIDigest(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Namespace: "default", Name: "hello-pkg"}},
		},
	}
	livePkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-pkg", Namespace: "default"},
		Spec:       fv1.PackageSpec{Deployment: fv1.Archive{Literal: []byte("digest-pinned content")}},
	}
	version := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:  "hello",
			Sequence:      1,
			PackageDigest: "sha256:deadbeef",
			// OCI-digest path: PackageRef still names the ORIGINAL package,
			// never a version-owned copy.
			Snapshot: fv1.FunctionSpec{
				Package: fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Namespace: "default", Name: "hello-pkg"}},
			},
		},
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fn, livePkg, version),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnTestVersion, "hello-v1")

	out := captureStdout(t, func() error { return Get(in) })
	assert.Equal(t, "digest-pinned content", out)
}

// TestGetVersionNotFound covers the preflight: a --version name that does
// not exist surfaces getOwnedFunctionVersion's clear error instead of an
// opaque downstream failure.
func TestGetVersionNotFound(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fn),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnTestVersion, "does-not-exist")

	err := Get(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "does-not-exist" not found for function "hello"`)
}

// TestGetVersionWrongFunction covers the preflight's ownership check: a
// --version that exists but belongs to a different function is rejected
// rather than silently rendering the wrong function's version.
func TestGetVersionWrongFunction(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}
	otherVersion := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "other-v1", Namespace: "default"},
		Spec:       fv1.FunctionVersionSpec{FunctionName: "other", Sequence: 1},
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(fn, otherVersion),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.FnTestVersion, "other-v1")

	err := Get(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "other-v1" targets function "other", not "hello"`)
}
