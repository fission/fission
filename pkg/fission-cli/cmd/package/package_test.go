// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
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
		srcOCI      string
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
		{name: "srcoci only", srcOCI: "ghcr.io/x/src:v1"},
		{name: "srcoci plus deploy", deployFiles: []string{"a.zip"}, srcOCI: "ghcr.io/x/src:v1"},
		{name: "srcoci plus src", srcFiles: []string{"src.zip"}, srcOCI: "ghcr.io/x/src:v1", wantErr: true},
		{name: "srcoci plus code", code: "hello.js", srcOCI: "ghcr.io/x/src:v1", wantErr: true},
		{name: "srcoci plus oci", ociImage: "ghcr.io/x/y:v1", srcOCI: "ghcr.io/x/src:v1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateArchiveSources(tc.code, tc.srcFiles, tc.deployFiles, tc.ociImage, tc.srcOCI)
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
	assert.Equal(t, fv1.BuildStatusNone, got.Status.BuildStatus,
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

func TestSplitImageDigest(t *testing.T) {
	cases := []struct{ in, image, digest string }{
		{"ghcr.io/org/pkg:v1", "ghcr.io/org/pkg:v1", ""},
		{"ghcr.io/org/pkg:v1@sha256:abc123", "ghcr.io/org/pkg:v1", "sha256:abc123"},
		{"ghcr.io/org/pkg@sha256:abc123", "ghcr.io/org/pkg", "sha256:abc123"},
		{"localhost:30500/p:tag", "localhost:30500/p:tag", ""},
	}
	for _, tc := range cases {
		image, digest := splitImageDigest(tc.in)
		if image != tc.image || digest != tc.digest {
			t.Errorf("splitImageDigest(%q) = (%q,%q), want (%q,%q)", tc.in, image, digest, tc.image, tc.digest)
		}
	}
}

// TestCreatePackageDeploySecret proves --deploysecret attaches the RFC-0031
// SecretRef to a URL deploy archive WITHOUT the anonymous checksum-generation
// download (the URL here resolves nowhere, so any download attempt would
// fail the create).
func TestCreatePackageDeploySecret(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgDeploySecret, "artifactory-creds")

	_, err := CreatePackage(in, client, "auth-pkg", "default", "node-env",
		nil, []string{"https://repo.example.invalid/a.zip"}, "", "", "", false, "default", "")
	require.NoError(t, err)

	pkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "auth-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fv1.ArchiveTypeUrl, pkg.Spec.Deployment.Type)
	require.NotNil(t, pkg.Spec.Deployment.SecretRef)
	assert.Equal(t, "artifactory-creds", pkg.Spec.Deployment.SecretRef.Name)
	assert.Empty(t, pkg.Spec.Deployment.Checksum.Sum, "no anonymous checksum download for credentialed URLs")
}

// TestCreatePackageDeploySecretExplicitChecksum keeps a user-pinned checksum.
func TestCreatePackageDeploySecretExplicitChecksum(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgDeploySecret, "creds")
	sum := strings.Repeat("a", 64)
	in.Set(flagkey.PkgDeployChecksum, sum)

	_, err := CreatePackage(in, client, "auth-pkg2", "default", "node-env",
		nil, []string{"https://repo.example.invalid/a.zip"}, "", "", "", false, "default", "")
	require.NoError(t, err)

	pkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "auth-pkg2", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, sum, pkg.Spec.Deployment.Checksum.Sum)
	require.NotNil(t, pkg.Spec.Deployment.SecretRef)
}

// TestCreatePackageSecretRequiresURL pins that credential flags refuse local
// files: those upload to storagesvc, whose internal HMAC transport must never
// mix with external credentials.
func TestCreatePackageSecretRequiresURL(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgDeploySecret, "creds")

	_, err := CreatePackage(in, client, "auth-pkg3", "default", "node-env",
		nil, []string{"local.zip"}, "", "", "", false, "default", "")
	require.ErrorContains(t, err, flagkey.PkgDeploySecret)
}

// TestCreatePackageSrcSecret covers the source-archive variant (builder path).
func TestCreatePackageSrcSecret(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgSrcSecret, "src-creds")

	_, err := CreatePackage(in, client, "auth-src-pkg", "default", "node-env",
		[]string{"https://repo.example.invalid/src.zip"}, nil, "", "", "", false, "default", "")
	require.NoError(t, err)

	pkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "auth-src-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, pkg.Spec.Source.SecretRef)
	assert.Equal(t, "src-creds", pkg.Spec.Source.SecretRef.Name)
	assert.Equal(t, fv1.BuildStatus(fv1.BuildStatusPending), pkg.Status.BuildStatus)
}

// TestCreatePackageSrcOCI proves --srcoci builds a Source OCI archive and
// queues a build (RFC-0031 phase 2).
func TestCreatePackageSrcOCI(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgSrcOCI, "ghcr.io/example/hello-src:v1")

	_, err := CreatePackage(in, client, "srcoci-pkg", "default", "node-env",
		nil, nil, "", "", "", false, "default", "")
	require.NoError(t, err)

	pkg, err := fc.CoreV1().Packages("default").Get(t.Context(), "srcoci-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Source.Type)
	require.NotNil(t, pkg.Spec.Source.OCI)
	assert.Equal(t, "ghcr.io/example/hello-src:v1", pkg.Spec.Source.OCI.Image)
	assert.True(t, pkg.Spec.Deployment.IsEmpty(), "deployment stays empty until the builder writes it")
	assert.Equal(t, fv1.BuildStatus(fv1.BuildStatusPending), pkg.Status.BuildStatus, "a source archive means the builder must run")
}

// TestUpdatePackageDeploySecretRotate rotates the credential on an existing
// url package without re-specifying the archive.
func TestUpdatePackageDeploySecretRotate(t *testing.T) {
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "rot-pkg", Namespace: "default"},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "node", Namespace: "default"},
			Deployment: fv1.Archive{
				Type:      fv1.ArchiveTypeUrl,
				URL:       "https://repo.example.invalid/a.zip",
				SecretRef: &apiv1.LocalObjectReference{Name: "creds-v1"},
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusSucceeded},
	}
	fc := fissionfake.NewSimpleClientset(pkg) //nolint:staticcheck
	client := cmd.Client{FissionClientSet: fc, Namespace: "default"}

	in := dummy.TestFlagSet()
	in.Set(flagkey.PkgDeploySecret, "creds-v2")

	_, err := UpdatePackage(in, client, "", pkg.DeepCopy())
	require.NoError(t, err)

	got, err := fc.CoreV1().Packages("default").Get(t.Context(), "rot-pkg", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Deployment.SecretRef)
	assert.Equal(t, "creds-v2", got.Spec.Deployment.SecretRef.Name)
	assert.Equal(t, "https://repo.example.invalid/a.zip", got.Spec.Deployment.URL, "url untouched")
}
