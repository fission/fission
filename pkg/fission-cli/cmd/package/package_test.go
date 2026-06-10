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
