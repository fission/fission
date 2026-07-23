// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// fnForObjName builds a Function with a 36-char UUID-shaped UID (getObjName
// slices the last 17 chars of fn.UID, matching every real Kubernetes UID).
func fnForObjName(name, namespace string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
	}
}

func TestGetObjName(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}

	t.Run("unversioned name is deterministic and bounded", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		name := deploy.getObjName(fn)
		assert.Equal(t, name, deploy.getObjName(fn), "name must be stable")
		assert.Contains(t, name, "newdeploy-")
		assert.LessOrEqual(t, len(name), 63)
	})

	t.Run("long function/namespace names are truncated to the object name limit", func(t *testing.T) {
		t.Parallel()
		long := make([]byte, 100)
		for i := range long {
			long[i] = 'a'
		}
		fn := fnForObjName(string(long), string(long))
		name := deploy.getObjName(fn)
		assert.LessOrEqual(t, len(name), 63)
	})

	t.Run("versioned function gets a distinct, suffixed name that still fits", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		unversioned := deploy.getObjName(fn)

		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
		versioned := deploy.getObjName(fn)

		assert.LessOrEqual(t, len(versioned), 63)
		assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
		assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
		assert.Equal(t, versioned, deploy.getObjName(fn), "name must be stable")
	})

	t.Run("two versions of the same function get distinct names", func(t *testing.T) {
		t.Parallel()
		fnV1 := fnForObjName("hello", "default")
		fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
		fnV2 := fnForObjName("hello", "default")
		fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

		assert.NotEqual(t, deploy.getObjName(fnV1), deploy.getObjName(fnV2))
	})

	t.Run("version label with no matching -v<seq> tail falls back to a bounded hash suffix", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "garbage-label-with-no-version-tail"}
		name := deploy.getObjName(fn)
		assert.LessOrEqual(t, len(name), 63)
	})
}

// TestGetObjNameLengthBound is the RFC-0025 bound test: for ANY function
// name/namespace length, UID, and published-version sequence number (up to
// math.MaxInt64 — the widest a versioning.Publish-minted sequence can be),
// both the unversioned and versioned derived object names must fit the
// Kubernetes 63-char name limit, and a versioned name must never collide
// with its function's unversioned name. Exercises newdeploy's getObjName
// (shared by Deployment, Service, and HPA naming — all three call sites
// pass the same objName).
func TestGetObjNameLengthBound(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.StringMatching(`[a-z]([a-z0-9-]{0,251}[a-z0-9])?`).Draw(rt, "name")
		namespace := rapid.StringMatching(`[a-z]([a-z0-9-]{0,251}[a-z0-9])?`).Draw(rt, "namespace")
		// Real Kubernetes UIDs are always 36-char UUIDs; getObjName slices
		// the last 17 bytes of fn.UID unconditionally, so bound the
		// generator at >=17 to stay within that pre-existing contract.
		uid := rapid.StringMatching(`[a-f0-9-]{17,64}`).Draw(rt, "uid")
		seq := rapid.Int64Range(1, math.MaxInt64).Draw(rt, "seq")

		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				UID:       types.UID(uid),
			},
		}
		unversioned := deploy.getObjName(fn)
		require.LessOrEqual(rt, len(unversioned), 63, "unversioned name must fit the 63-char limit")

		fn.Labels = map[string]string{
			fv1.FUNCTION_VERSION: fmt.Sprintf("%s-v%d", name, seq),
		}
		versioned := deploy.getObjName(fn)
		require.LessOrEqual(rt, len(versioned), 63, "versioned name must fit the 63-char limit")
		require.NotEqual(rt, unversioned, versioned, "a versioned name must never collide with the unversioned name")
		require.Equal(rt, versioned, deploy.getObjName(fn), "name must be deterministic")
	})
}

// TestGetObjNameHashFallbackLengthBound covers the hash-fallback branch of
// executorUtils.VersionSuffix (a version label that does NOT end in
// "-v<seq>"): the fallback is always exactly "-v" + 8 hex chars, so the
// derived object name stays bounded regardless of the (long, garbage)
// label's own length.
func TestGetObjNameHashFallbackLengthBound(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}

	tests := []struct {
		name  string
		label string
	}{
		{"no -v at all", "garbage"},
		{"-v tail with non-digit suffix", "fn-vabc"},
		{"-v<seq> not at the end", "fn-v3-extra"},
		{"long garbage label", "this-label-does-not-end-in-a-version-tail-and-is-quite-long-indeed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := fnForObjName("hello", "default")
			fn.Labels = map[string]string{fv1.FUNCTION_VERSION: tc.label}
			name := deploy.getObjName(fn)
			assert.LessOrEqual(t, len(name), 63, "the derived object name must still fit the limit")
			assert.Equal(t, name, deploy.getObjName(fn), "name must be deterministic")
		})
	}
}

// TestGetDeployLabelsPropagatesFunctionVersion asserts FUNCTION_VERSION
// flows from the versioned Function object's labels into the Deployment
// labels via getDeployLabels' existing fnMeta.Labels merge — no new
// merge logic needed, just coverage that the merge actually carries the
// label through.
func TestGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestGetDeployLabelsUnversionedHasNoVersionLabel is the byte-identical
// control: an unversioned function's labels must not carry FUNCTION_VERSION.
func TestGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}
