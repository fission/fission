// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
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

func TestOCIPoolHashStable(t *testing.T) {
	t.Parallel()
	base := &fv1.OCIArchive{Image: "registry.example.com/code/hello:v1"}
	assert.Equal(t, ociPoolHash(base), ociPoolHash(base), "hash must be deterministic")
	assert.Len(t, ociPoolHash(base), 16)
	assert.Empty(t, ociPoolHash(nil), "nil archive means the plain pool")

	// Every pod-spec-affecting field must contribute to the pool identity:
	// two archives that would produce different pods must never share a pool
	// (e.g. one image holding several functions under different sub-paths).
	variants := []*fv1.OCIArchive{
		{Image: "registry.example.com/code/hello:v2"},
		{Image: "registry.example.com/code/hello:v1", SubPath: "app"},
		{Image: "registry.example.com/code/hello:v1", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Image: "registry.example.com/code/hello:v1", ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "regcred"}}},
	}
	for _, v := range variants {
		assert.NotEqualf(t, ociPoolHash(base), ociPoolHash(v), "variant %+v must key its own pool", v)
	}
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
			got, err := gpm.getFunctionOCIArchive(t.Context(), fn, env)
			require.NoError(t, err)
			if tc.want {
				require.NotNil(t, got)
				assert.Equal(t, "reg.example.com/code:v1", got.Image)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestGetFunctionOCIArchiveInfiniteEnvFallsBack(t *testing.T) {
	t.Parallel()
	// Infinite-functions-per-container envs store code at per-function
	// paths; a shared per-image mount cannot serve them.
	ociArchive := fv1.Archive{Type: fv1.ArchiveTypeOCI, OCI: &fv1.OCIArchive{Image: "reg.example.com/code:v1"}}
	fn, env, pkg := ociEligibilityFixtures(2, nil, nil, ociArchive)
	env.Spec.AllowedFunctionsPerContainer = fv1.AllowedFunctionsPerContainerInfinite
	gpm := &GenericPoolManager{
		logger:        logr.Discard(),
		fissionClient: fissionfake.NewSimpleClientset(pkg),
	}
	got, err := gpm.getFunctionOCIArchive(t.Context(), fn, env)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestGetFunctionOCIArchiveMissingPackage(t *testing.T) {
	t.Parallel()
	fn, env, _ := ociEligibilityFixtures(2, nil, nil, fv1.Archive{})
	gpm := &GenericPoolManager{
		logger:        logr.Discard(),
		fissionClient: fissionfake.NewSimpleClientset(),
	}
	got, err := gpm.getFunctionOCIArchive(t.Context(), fn, env)
	require.NoError(t, err, "a deleted package must fall back to Path A (the fetcher reports it precisely)")
	assert.Nil(t, got)
}
