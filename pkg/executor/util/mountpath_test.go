// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func fnWith(secrets []fv1.SecretReference, cms []fv1.ConfigMapReference) *fv1.Function {
	return &fv1.Function{
		Name: "fn", Namespace: "default",
		Spec: fv1.FunctionSpec{Secrets: secrets, ConfigMaps: cms},
	}
}

func TestMountPathVolumes(t *testing.T) {
	t.Parallel()

	t.Run("no mountPath produces nothing", func(t *testing.T) {
		t.Parallel()
		vols, mounts, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: "s"}},
			[]fv1.ConfigMapReference{{Namespace: "default", Name: "c"}},
		))
		require.NoError(t, err)
		assert.Empty(t, vols, "a reference with no redirect keeps the legacy envFrom projection")
		assert.Empty(t, mounts)
	})

	t.Run("a redirect mounts under the shared root", func(t *testing.T) {
		t.Parallel()
		vols, mounts, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: "creds", MountPath: "app/creds"}},
			nil,
		))
		require.NoError(t, err)
		require.Len(t, vols, 1)
		require.Len(t, mounts, 1)
		assert.Equal(t, "creds", vols[0].Secret.SecretName)
		// The path must be rooted, not the raw user value: that is what keeps a
		// chosen path off /var/run/secrets/kubernetes.io/serviceaccount.
		assert.Equal(t, SharedSecretRoot+"/app/creds", mounts[0].MountPath)
		assert.True(t, mounts[0].ReadOnly, "projected platform files must not be writable by the function")
		assert.Equal(t, vols[0].Name, mounts[0].Name)
	})

	t.Run("configmaps use their own root", func(t *testing.T) {
		t.Parallel()
		vols, mounts, err := MountPathVolumes(fnWith(nil,
			[]fv1.ConfigMapReference{{Namespace: "default", Name: "cfg", MountPath: "app"}},
		))
		require.NoError(t, err)
		require.Len(t, mounts, 1)
		assert.Equal(t, SharedConfigRoot+"/app", mounts[0].MountPath)
		assert.Equal(t, "cfg", vols[0].ConfigMap.Name)
	})

	// The admission rules should already have rejected these, but this function
	// builds a real mount path and must not depend on having been called after
	// validation — the same reasoning as the fetcher re-validating.
	t.Run("an absolute path is refused here too", func(t *testing.T) {
		t.Parallel()
		_, _, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: "s", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"}},
			nil,
		))
		require.Error(t, err, "an absolute path must not be able to target a platform mount")
	})

	t.Run("an escaping path is refused here too", func(t *testing.T) {
		t.Parallel()
		_, _, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: "s", MountPath: "../../userfunc"}},
			nil,
		))
		require.Error(t, err)
	})

	t.Run("volume names are unique per object", func(t *testing.T) {
		t.Parallel()
		vols, _, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{
				{Namespace: "default", Name: "a", MountPath: "x"},
				{Namespace: "default", Name: "b", MountPath: "y"},
			},
			[]fv1.ConfigMapReference{{Namespace: "default", Name: "a", MountPath: "z"}},
		))
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, v := range vols {
			require.Falsef(t, seen[v.Name], "duplicate volume name %q would be rejected by the apiserver", v.Name)
			seen[v.Name] = true
		}
		// A secret and a configmap may share an object NAME; the volume names
		// must still differ.
		assert.Len(t, seen, 3)
	})
}

// TestMountPathVolumeNamesAreBoundedAndUnique pins the two ways a derived
// volume name breaks a pod outright.
func TestMountPathVolumeNamesAreBoundedAndUnique(t *testing.T) {
	t.Parallel()

	// Admission rejects colliding DIRECTORIES, not repeated objects — so one
	// Secret at two mount paths is legal, and must not yield two volumes with
	// the same name (the apiserver refuses such a pod).
	t.Run("the same object at two paths gets distinct volumes", func(t *testing.T) {
		t.Parallel()
		vols, mounts, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{
				{Namespace: "default", Name: "s", MountPath: "a"},
				{Namespace: "default", Name: "s", MountPath: "b"},
			}, nil))
		require.NoError(t, err)
		require.Len(t, vols, 2)
		assert.NotEqual(t, vols[0].Name, vols[1].Name, "duplicate volume names make the pod unschedulable")
		assert.Equal(t, vols[0].Name, mounts[0].Name)
		assert.Equal(t, vols[1].Name, mounts[1].Name)
		assert.Equal(t, SharedSecretRoot+"/a", mounts[0].MountPath)
		assert.Equal(t, SharedSecretRoot+"/b", mounts[1].MountPath)
	})

	// Object names may already be a full 63-char DNS label, so a prefixed
	// volume name can overflow the limit Kubernetes enforces.
	t.Run("a maximal object name stays within the DNS-label limit", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("a", 63)
		vols, _, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: long, MountPath: "x"}}, nil))
		require.NoError(t, err)
		require.Len(t, vols, 1)
		assert.LessOrEqual(t, len(vols[0].Name), maxVolumeNameLen,
			"a volume name over 63 chars is rejected by the apiserver")
		assert.NotEmpty(t, vols[0].Name)
	})

	// The fetcher writes its files with this mode; the native projection must
	// match or the same function sees different permissions per executor.
	t.Run("projected files match the fetcher's mode", func(t *testing.T) {
		t.Parallel()
		vols, _, err := MountPathVolumes(fnWith(
			[]fv1.SecretReference{{Namespace: "default", Name: "s", MountPath: "a"}},
			[]fv1.ConfigMapReference{{Namespace: "default", Name: "c", MountPath: "b"}}))
		require.NoError(t, err)
		require.Len(t, vols, 2)
		require.NotNil(t, vols[0].Secret.DefaultMode)
		assert.EqualValues(t, projectedFileMode, *vols[0].Secret.DefaultMode)
		require.NotNil(t, vols[1].ConfigMap.DefaultMode)
		assert.EqualValues(t, projectedFileMode, *vols[1].ConfigMap.DefaultMode)
	})
}
