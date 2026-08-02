// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanMountPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"app", "app", false},
		{"app/creds", "app/creds", false},
		{"app//creds", "app/creds", false},
		{"./app/creds", "app/creds", false},
		{"app/./creds", "app/creds", false},
		{"app/sub/../creds", "app/creds", false},

		// Absolute paths are the headline rejection: the value is resolved
		// under the shared root, and a user writing "/etc/app" would otherwise
		// reasonably expect the container filesystem root.
		{"/etc/app", "", true},
		{"/", "", true},

		// Escapes. RootJoin would also stop these, but a clear admission error
		// beats a runtime one.
		{"..", "", true},
		{"../outside", "", true},
		{"app/../..", "", true},
		{".", "", true},

		{`app\creds`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := CleanMountPath(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateMountPaths(t *testing.T) {
	t.Parallel()

	t.Run("distinct paths are accepted", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "a", MountPath: "app/a"},
				{Namespace: "default", Name: "b", MountPath: "app/b"},
			},
		}
		assert.NoError(t, spec.validateMountPaths())
	})

	t.Run("unset paths never collide with each other", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "a"},
				{Namespace: "default", Name: "b"},
			},
		}
		assert.NoError(t, spec.validateMountPaths(),
			"an empty mountPath keeps the per-object default layout, which cannot collide")
	})

	t.Run("two secrets on one path are rejected", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "a", MountPath: "app"},
				{Namespace: "default", Name: "b", MountPath: "app"},
			},
		}
		err := spec.validateMountPaths()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already used")
	})

	// Equivalent spellings must collide too, or the rule is trivially bypassed.
	t.Run("paths that normalise to the same directory collide", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "a", MountPath: "app/creds"},
				{Namespace: "default", Name: "b", MountPath: "./app/sub/../creds"},
			},
		}
		assert.Error(t, spec.validateMountPaths(),
			"duplicate detection must run on the cleaned path, not the raw string")
	})

	// Secrets and configmaps land under different roots, so the same relative
	// path under each is not a collision.
	t.Run("a secret and a configmap may share a path", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets:    []SecretReference{{Namespace: "default", Name: "a", MountPath: "app"}},
			ConfigMaps: []ConfigMapReference{{Namespace: "default", Name: "a", MountPath: "app"}},
		}
		assert.NoError(t, spec.validateMountPaths(),
			"/secrets/app and /configs/app are different directories")
	})

	t.Run("two configmaps on one path are rejected", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			ConfigMaps: []ConfigMapReference{
				{Namespace: "default", Name: "a", MountPath: "app"},
				{Namespace: "default", Name: "b", MountPath: "app"},
			},
		}
		assert.Error(t, spec.validateMountPaths())
	})

	t.Run("an absolute path is rejected", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{{Namespace: "default", Name: "a", MountPath: "/etc/app"}},
		}
		err := spec.validateMountPaths()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "relative")
	})

	t.Run("an escaping path is rejected", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			ConfigMaps: []ConfigMapReference{{Namespace: "default", Name: "a", MountPath: "../../etc"}},
		}
		assert.Error(t, spec.validateMountPaths())
	})
}

// TestValidateMountPathsImplicitCollision is the case explicit-vs-explicit
// comparison misses: a reference with no MountPath still occupies
// <namespace>/<name>, so another reference naming that exact path shares the
// directory while only one of the two sets the field.
func TestValidateMountPathsImplicitCollision(t *testing.T) {
	t.Parallel()

	t.Run("explicit path colliding with another reference's default", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "creds"},
				{Namespace: "default", Name: "other", MountPath: "default/creds"},
			},
		}
		err := spec.validateMountPaths()
		require.Error(t, err, "an explicit mountPath equal to another reference's default directory is a collision")
		assert.Contains(t, err.Error(), "default/creds")
	})

	t.Run("configmaps too", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			ConfigMaps: []ConfigMapReference{
				{Namespace: "ns", Name: "cfg"},
				{Namespace: "ns", Name: "other", MountPath: "ns/cfg"},
			},
		}
		assert.Error(t, spec.validateMountPaths())
	})

	t.Run("two defaults still never collide", func(t *testing.T) {
		t.Parallel()
		spec := FunctionSpec{
			Secrets: []SecretReference{
				{Namespace: "default", Name: "a"},
				{Namespace: "default", Name: "b"},
			},
		}
		assert.NoError(t, spec.validateMountPaths(),
			"distinct objects have distinct default directories by construction")
	})
}
