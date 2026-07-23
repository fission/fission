// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

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

func TestContainerGetObjName(t *testing.T) {
	t.Parallel()
	caaf := &Container{}

	t.Run("unversioned name is deterministic and bounded", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		name := caaf.getObjName(fn)
		assert.Equal(t, name, caaf.getObjName(fn), "name must be stable")
		assert.Contains(t, name, "container-")
		assert.LessOrEqual(t, len(name), 63)
	})

	t.Run("long function/namespace names are truncated to the object name limit", func(t *testing.T) {
		t.Parallel()
		long := make([]byte, 100)
		for i := range long {
			long[i] = 'a'
		}
		fn := fnForObjName(string(long), string(long))
		name := caaf.getObjName(fn)
		assert.LessOrEqual(t, len(name), 63)
	})

	t.Run("versioned function gets a distinct, suffixed name that still fits", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		unversioned := caaf.getObjName(fn)

		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
		versioned := caaf.getObjName(fn)

		assert.LessOrEqual(t, len(versioned), 63)
		assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
		assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
		assert.Equal(t, versioned, caaf.getObjName(fn), "name must be stable")
	})

	t.Run("two versions of the same function get distinct names", func(t *testing.T) {
		t.Parallel()
		fnV1 := fnForObjName("hello", "default")
		fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
		fnV2 := fnForObjName("hello", "default")
		fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

		assert.NotEqual(t, caaf.getObjName(fnV1), caaf.getObjName(fnV2))
	})

	t.Run("version label with no matching -v<seq> tail falls back to a bounded hash suffix", func(t *testing.T) {
		t.Parallel()
		fn := fnForObjName("hello", "default")
		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "garbage-label-with-no-version-tail"}
		name := caaf.getObjName(fn)
		assert.LessOrEqual(t, len(name), 63)
	})
}

// TestContainerGetObjNameLengthBound is the RFC-0025 bound test: for ANY
// function name/namespace length, UID, and published-version sequence
// number (up to math.MaxInt64), both the unversioned and versioned derived
// object names must fit the Kubernetes 63-char name limit, and a versioned
// name must never collide with its function's unversioned name. Exercises
// the container executor's getObjName (shared by Deployment, Service, and
// HPA naming — all call sites pass the same objName).
func TestContainerGetObjNameLengthBound(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
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
		unversioned := caaf.getObjName(fn)
		require.LessOrEqual(rt, len(unversioned), 63, "unversioned name must fit the 63-char limit")

		fn.Labels = map[string]string{
			fv1.FUNCTION_VERSION: fmt.Sprintf("%s-v%d", name, seq),
		}
		versioned := caaf.getObjName(fn)
		require.LessOrEqual(rt, len(versioned), 63, "versioned name must fit the 63-char limit")
		require.NotEqual(rt, unversioned, versioned, "a versioned name must never collide with the unversioned name")
		require.Equal(rt, versioned, caaf.getObjName(fn), "name must be deterministic")
	})
}

// TestContainerGetObjNameHashFallbackLengthBound covers the hash-fallback
// branch of executorUtils.VersionSuffix (a version label that does NOT end
// in "-v<seq>"): the fallback is always exactly "-v" + 8 hex chars, so the
// derived object name stays bounded regardless of the (long, garbage)
// label's own length.
func TestContainerGetObjNameHashFallbackLengthBound(t *testing.T) {
	t.Parallel()
	caaf := &Container{}

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
			name := caaf.getObjName(fn)
			assert.LessOrEqual(t, len(name), 63, "the derived object name must still fit the limit")
			assert.Equal(t, name, caaf.getObjName(fn), "name must be deterministic")
		})
	}
}

// TestContainerGetDeployLabelsPropagatesFunctionVersion asserts
// FUNCTION_VERSION flows from the versioned Function object's labels into
// the Deployment labels via getDeployLabels' existing fnMeta.Labels copy —
// no new merge logic needed, just coverage that the copy actually carries
// the label through.
func TestContainerGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}

	labels := caaf.getDeployLabels(fnMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestContainerGetDeployLabelsUnversionedHasNoVersionLabel is the
// byte-identical control: an unversioned function's labels must not carry
// FUNCTION_VERSION.
func TestContainerGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}

	labels := caaf.getDeployLabels(fnMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}
