// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestPoolKeyEmptyImageMatchesEnvUID(t *testing.T) {
	t.Parallel()
	// Backward-compat guard: for non-OCI pools the key must be byte-for-byte
	// the env UID, exactly as before per-image pools existed.
	uid := k8sTypes.UID("0fdd76f6-b2b5-4b3e-8a52-6c3ca60aa006")
	assert.Equal(t, string(uid), poolKey(uid, ""))
	assert.Equal(t, string(uid)+"/abc123", poolKey(uid, "abc123"))
}

func TestOCIImageHashStable(t *testing.T) {
	t.Parallel()
	h1 := ociImageHash("registry.example.com/code/hello:v1")
	h2 := ociImageHash("registry.example.com/code/hello:v1")
	h3 := ociImageHash("registry.example.com/code/hello:v2")
	assert.Equal(t, h1, h2, "hash must be deterministic")
	assert.NotEqual(t, h1, h3, "different references must hash differently")
	assert.Len(t, h1, 16)
	assert.Empty(t, ociImageHash(""), "empty image means no per-image pool")
}

func ociEligibilityFixtures(envVersion int, secrets []fv1.SecretReference, cfgmaps []fv1.ConfigMapReference, deployment fv1.Archive) (*fv1.Function, *fv1.Environment, *fv1.Package) {
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "pkg", Namespace: "fn-ns"},
		Spec:       fv1.PackageSpec{Deployment: deployment},
	}
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "fn-ns"},
		Spec: fv1.FunctionSpec{
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{Name: "pkg", Namespace: "fn-ns"},
			},
			Secrets:    secrets,
			ConfigMaps: cfgmaps,
		},
	}
	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "fn-ns"},
		Spec:       fv1.EnvironmentSpec{Version: envVersion},
	}
	return fn, env, pkg
}

func TestGetFunctionOCIArchiveEligibility(t *testing.T) {
	t.Parallel()
	ociArchive := fv1.Archive{Type: fv1.ArchiveTypeOCI, OCI: &fv1.OCIArchive{Image: "reg.example.com/code:v1"}}
	urlArchive := fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://example.com/a.zip"}

	cases := []struct {
		name       string
		envVersion int
		secrets    []fv1.SecretReference
		cfgmaps    []fv1.ConfigMapReference
		deployment fv1.Archive
		want       bool
	}{
		{"oci package on v2 env", 2, nil, nil, ociArchive, true},
		{"non-oci package", 2, nil, nil, urlArchive, false},
		{"env v1 needs the fetcher", 1, nil, nil, ociArchive, false},
		{"secrets need the fetcher", 2, []fv1.SecretReference{{Name: "s"}}, nil, ociArchive, false},
		{"configmaps need the fetcher", 2, nil, []fv1.ConfigMapReference{{Name: "c"}}, ociArchive, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn, env, pkg := ociEligibilityFixtures(tc.envVersion, tc.secrets, tc.cfgmaps, tc.deployment)
			gpm := &GenericPoolManager{
				logger:        logr.Discard(),
				fissionClient: fissionfake.NewSimpleClientset(pkg),
			}
			got := gpm.getFunctionOCIArchive(t.Context(), fn, env)
			if tc.want {
				require.NotNil(t, got)
				assert.Equal(t, "reg.example.com/code:v1", got.Image)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestGetFunctionOCIArchiveMissingPackage(t *testing.T) {
	t.Parallel()
	fn, env, _ := ociEligibilityFixtures(2, nil, nil, fv1.Archive{})
	gpm := &GenericPoolManager{
		logger:        logr.Discard(),
		fissionClient: fissionfake.NewSimpleClientset(),
	}
	assert.Nil(t, gpm.getFunctionOCIArchive(t.Context(), fn, env),
		"a missing package must fall back to Path A, not panic")
}
