// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

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

func TestValidateArchiveSources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		code        string
		srcFiles    []string
		deployFiles []string
		ociImage    string
		wantErr     bool
	}{
		{name: "oci only", ociImage: "ghcr.io/x/y:v1"},
		{name: "deploy only", deployFiles: []string{"a.zip"}},
		{name: "src only", srcFiles: []string{"src.zip"}},
		{name: "code only", code: "hello.js"},
		{name: "none", wantErr: true},
		{name: "oci plus code", code: "hello.js", ociImage: "ghcr.io/x/y:v1", wantErr: true},
		{name: "oci plus deploy", deployFiles: []string{"a.zip"}, ociImage: "ghcr.io/x/y:v1", wantErr: true},
		{name: "oci plus src", srcFiles: []string{"src.zip"}, ociImage: "ghcr.io/x/y:v1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateArchiveSources(tc.code, tc.srcFiles, tc.deployFiles, tc.ociImage)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCreatePackageOCI proves --oci builds the Deployment archive inline with
// no file globbing, zipping, or upload (the fake clientset is the only
// dependency touched), per RFC-0001 Phase 1.
func TestCreatePackageOCI(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()

	meta, err := CreatePackage(in, client, "oci-pkg", "default", "node-env",
		nil, nil, "", "", "", false, "default", "ghcr.io/example/hello-code:v1")
	require.NoError(t, err)
	require.NotNil(t, meta)

	pkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "oci-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Deployment.Type)
	require.NotNil(t, pkg.Spec.Deployment.OCI)
	assert.Equal(t, "ghcr.io/example/hello-code:v1", pkg.Spec.Deployment.OCI.Image)
	assert.Empty(t, pkg.Spec.Deployment.URL)
	assert.Empty(t, pkg.Spec.Deployment.Literal)
	assert.True(t, pkg.Spec.Source.IsEmpty(), "source archive must stay empty for --oci")
}

// TestCreatePackageOCIGeneratedName checks the package name derives from the
// image when --name is omitted.
func TestCreatePackageOCIGeneratedName(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()

	meta, err := CreatePackage(in, client, "", "default", "node-env",
		nil, nil, "", "", "", false, "default", "ghcr.io/example/hello-code:v1")
	require.NoError(t, err)
	assert.Contains(t, meta.Name, "hello-code")
}

// TestGetDeployOCIRefusesCleanly pins the fix for a nil-reader panic: a
// package whose deployment archive is an OCI image has no downloadable
// archive, so `package getdeploy` must return a clear error naming the image
// instead of crashing.
func TestGetDeployOCIRefusesCleanly(t *testing.T) {
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "oci-pkg", Namespace: "default"},
		Spec: fv1.PackageSpec{
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  &fv1.OCIArchive{Image: "ghcr.io/example/hello-code:v1"},
			},
		},
	}
	fc := fissionfake.NewSimpleClientset(pkg) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	t.Cleanup(cmd.ResetClientsetForTest)

	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgName, "oci-pkg")

	err := GetDeploy(in)
	require.Error(t, err, "getdeploy on an OCI package must error, not panic")
	assert.Contains(t, err.Error(), "ghcr.io/example/hello-code:v1")
	assert.Contains(t, err.Error(), "no downloadable archive")
}

// TestUpdatePackageOCI covers `package update --oci`: the deployment archive
// is replaced wholesale (literal/url/checksum cleared), the source archive
// survives, and a stale build status from the package's previous life (a
// failed source build, say) resets to none — the fetcher refuses to serve a
// package whose status is failed/pending/running.
func TestUpdatePackageOCI(t *testing.T) {
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "conv-pkg", Namespace: "default"},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "python", Namespace: "default"},
			Source:      fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://storage/src.zip"},
			Deployment: fv1.Archive{
				Type:     fv1.ArchiveTypeUrl,
				URL:      "http://storage/deploy.zip",
				Checksum: fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: "abc"},
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusFailed, BuildLog: "boom"},
	}
	fc := fissionfake.NewSimpleClientset(pkg) //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}

	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgOCI, "ghcr.io/example/hello-code:v2")

	_, err := UpdatePackage(in, client, "", pkg.DeepCopy())
	require.NoError(t, err)

	got, err := fc.CoreV1().Packages("default").Get(t.Context(), "conv-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fv1.ArchiveTypeOCI, got.Spec.Deployment.Type)
	require.NotNil(t, got.Spec.Deployment.OCI)
	assert.Equal(t, "ghcr.io/example/hello-code:v2", got.Spec.Deployment.OCI.Image)
	assert.Empty(t, got.Spec.Deployment.URL, "the old deploy URL must be cleared")
	assert.Empty(t, got.Spec.Deployment.Checksum.Sum, "the old checksum must be cleared")
	assert.Equal(t, "http://storage/src.zip", got.Spec.Source.URL, "the source archive must survive")
	assert.Equal(t, fv1.BuildStatus(fv1.BuildStatusNone), got.Status.BuildStatus,
		"a stale failed status must reset so the fetcher serves the OCI package")
}

// TestUpdatePackageOCIMutualExclusion pins the flag guard.
func TestUpdatePackageOCIMutualExclusion(t *testing.T) {
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "conv-pkg", Namespace: "default"},
		Spec:       fv1.PackageSpec{Environment: fv1.EnvironmentReference{Name: "python", Namespace: "default"}},
	}
	fc := fissionfake.NewSimpleClientset(pkg) //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}

	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgOCI, "ghcr.io/example/hello-code:v2")
	in.Set(flagkey.PkgCode, "hello.py")

	_, err := UpdatePackage(in, client, "", pkg.DeepCopy())
	require.Error(t, err, "--oci combined with --code must be rejected")
}

func TestIsClusterLocalRef(t *testing.T) {
	cases := map[string]bool{
		"test-registry.default.svc.cluster.local:5000/x/y:v1": true,
		"registry.fission.svc:5000/x":                         true,
		"ghcr.io/org/pkg:v1":                                  false,
		"localhost:30500/x:v1":                                false,
		"10.0.0.5:5000/x":                                     false,
	}
	for ref, want := range cases {
		if got := isClusterLocalRef(ref); got != want {
			t.Errorf("isClusterLocalRef(%q) = %v, want %v", ref, got, want)
		}
	}
}
