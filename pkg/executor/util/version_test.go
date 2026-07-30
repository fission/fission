// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

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

// fnForObjName builds a Function with a 36-char UUID-shaped UID
// (VersionedObjName slices the last 17 chars of fn.UID, matching every real
// Kubernetes UID).
func fnForObjName(name, namespace string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
	}
}

// objNamePrefixes are the two real prefixes newdeploy and container pass to
// VersionedObjName (each 10 chars including the trailing dash, per the
// engineered 63-char budget) — the property tests below run once per prefix
// so both callers' shapes stay covered by this single suite.
var objNamePrefixes = []string{"newdeploy-", "container-"}

func TestVersionedObjName(t *testing.T) {
	t.Parallel()

	for _, prefix := range objNamePrefixes {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()

			t.Run("unversioned name is deterministic and bounded", func(t *testing.T) {
				t.Parallel()
				fn := fnForObjName("hello", "default")
				name := VersionedObjName(prefix, fn)
				assert.Equal(t, name, VersionedObjName(prefix, fn), "name must be stable")
				assert.Contains(t, name, prefix)
				assert.LessOrEqual(t, len(name), 63)
			})

			t.Run("long function/namespace names are truncated to the object name limit", func(t *testing.T) {
				t.Parallel()
				long := make([]byte, 100)
				for i := range long {
					long[i] = 'a'
				}
				fn := fnForObjName(string(long), string(long))
				name := VersionedObjName(prefix, fn)
				assert.LessOrEqual(t, len(name), 63)
			})

			t.Run("versioned function gets a distinct, suffixed name that still fits", func(t *testing.T) {
				t.Parallel()
				fn := fnForObjName("hello", "default")
				unversioned := VersionedObjName(prefix, fn)

				fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
				versioned := VersionedObjName(prefix, fn)

				assert.LessOrEqual(t, len(versioned), 63)
				assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
				assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
				assert.Equal(t, versioned, VersionedObjName(prefix, fn), "name must be stable")
			})

			t.Run("two versions of the same function get distinct names", func(t *testing.T) {
				t.Parallel()
				fnV1 := fnForObjName("hello", "default")
				fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
				fnV2 := fnForObjName("hello", "default")
				fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

				assert.NotEqual(t, VersionedObjName(prefix, fnV1), VersionedObjName(prefix, fnV2))
			})

			t.Run("version label with no matching -v<seq> tail falls back to a bounded hash suffix", func(t *testing.T) {
				t.Parallel()
				fn := fnForObjName("hello", "default")
				fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "garbage-label-with-no-version-tail"}
				name := VersionedObjName(prefix, fn)
				assert.LessOrEqual(t, len(name), 63)
			})
		})
	}
}

// TestVersionedObjNameLengthBound is the RFC-0025 bound test: for ANY
// function name/namespace length, UID, and published-version sequence
// number (up to math.MaxInt64 — the widest a versioning.Publish-minted
// sequence can be), both the unversioned and versioned derived object names
// must fit the Kubernetes 63-char name limit, and a versioned name must
// never collide with its function's unversioned name. Runs once per real
// caller prefix (newdeploy's and container's getObjName both delegate to
// VersionedObjName with a 10-char prefix) so this single property test
// covers both call sites.
func TestVersionedObjNameLengthBound(t *testing.T) {
	t.Parallel()
	for _, prefix := range objNamePrefixes {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			rapid.Check(t, func(rt *rapid.T) {
				name := rapid.StringMatching(`[a-z]([a-z0-9-]{0,251}[a-z0-9])?`).Draw(rt, "name")
				namespace := rapid.StringMatching(`[a-z]([a-z0-9-]{0,251}[a-z0-9])?`).Draw(rt, "namespace")
				// Real Kubernetes UIDs are always 36-char UUIDs; VersionedObjName
				// slices the last 17 bytes of fn.UID unconditionally, so bound the
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
				unversioned := VersionedObjName(prefix, fn)
				require.LessOrEqual(rt, len(unversioned), 63, "unversioned name must fit the 63-char limit")

				fn.Labels = map[string]string{
					fv1.FUNCTION_VERSION: fmt.Sprintf("%s-v%d", name, seq),
				}
				versioned := VersionedObjName(prefix, fn)
				require.LessOrEqual(rt, len(versioned), 63, "versioned name must fit the 63-char limit")
				require.NotEqual(rt, unversioned, versioned, "a versioned name must never collide with the unversioned name")
				require.Equal(rt, versioned, VersionedObjName(prefix, fn), "name must be deterministic")
			})
		})
	}
}

// TestVersionedObjNameHashFallbackLengthBound covers the hash-fallback
// branch of VersionSuffix (a version label that does NOT end in "-v<seq>"):
// the fallback is always exactly "-v" + 8 hex chars, so the derived object
// name stays bounded regardless of the (long, garbage) label's own length.
func TestVersionedObjNameHashFallbackLengthBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		label string
	}{
		{"no -v at all", "garbage"},
		{"-v tail with non-digit suffix", "fn-vabc"},
		{"-v<seq> not at the end", "fn-v3-extra"},
		{"long garbage label", "this-label-does-not-end-in-a-version-tail-and-is-quite-long-indeed"},
	}
	for _, prefix := range objNamePrefixes {
		for _, tc := range tests {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				fn := fnForObjName("hello", "default")
				fn.Labels = map[string]string{fv1.FUNCTION_VERSION: tc.label}
				name := VersionedObjName(prefix, fn)
				assert.LessOrEqual(t, len(name), 63, "the derived object name must still fit the limit")
				assert.Equal(t, name, VersionedObjName(prefix, fn), "name must be deterministic")
			})
		}
	}
}

// TestVersionSuffix covers VersionSuffix directly: the "-v<seq>" match
// branch, the hash-fallback branch, and determinism/distinctness of both.
func TestVersionSuffix(t *testing.T) {
	t.Parallel()

	t.Run("matches the -v<seq> tail verbatim", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "-v3", VersionSuffix("hello-v3"))
		assert.Equal(t, "-v42", VersionSuffix("my-func-v42"))
	})

	t.Run("falls back to a bounded hash suffix when there is no -v<seq> tail", func(t *testing.T) {
		t.Parallel()
		suffix := VersionSuffix("garbage-label-with-no-version-tail")
		assert.Len(t, suffix, 10, `fallback must be "-v" + 8 hex chars`)
		assert.Regexp(t, `^-v[0-9a-f]{8}$`, suffix)
	})

	t.Run("is deterministic", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, VersionSuffix("hello-v3"), VersionSuffix("hello-v3"))
		assert.Equal(t, VersionSuffix("no-tail-here"), VersionSuffix("no-tail-here"))
	})

	t.Run("distinct labels produce distinct fallback suffixes", func(t *testing.T) {
		t.Parallel()
		assert.NotEqual(t, VersionSuffix("garbage-one"), VersionSuffix("garbage-two"))
	})

	rapid.Check(t, func(rt *rapid.T) {
		label := rapid.String().Draw(rt, "label")
		suffix := VersionSuffix(label)
		require.LessOrEqual(rt, len(suffix), 20, "suffix must stay short regardless of label length")
		require.Equal(rt, suffix, VersionSuffix(label), "must be deterministic")
	})
}

// TestTruncateForSuffix exercises the shared budget/clamp/truncate helper
// directly, covering both real call sites' shapes (VersionedObjName's
// 35-char budget, functionServiceName's Service-name budget) plus the edge
// cases: suffix wider than budget (clamped at 0) and base already within
// budget (no-op).
func TestTruncateForSuffix(t *testing.T) {
	t.Parallel()

	t.Run("base within budget is untouched", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "hello", TruncateForSuffix("hello", 35, ""))
		assert.Equal(t, "hello", TruncateForSuffix("hello", 35, "-v3"))
	})

	t.Run("base longer than budget is truncated to make room for suffix", func(t *testing.T) {
		t.Parallel()
		base := "this-is-a-very-long-base-string-well-over-budget"
		got := TruncateForSuffix(base, 10, "-v123")
		assert.Equal(t, base[:10-len("-v123")], got)
		assert.LessOrEqual(t, len(got), 10-len("-v123"))
	})

	t.Run("suffix wider than budget clamps at zero, not negative", func(t *testing.T) {
		t.Parallel()
		got := TruncateForSuffix("hello", 3, "-verylongsuffix")
		assert.Equal(t, "", got)
	})

	t.Run("empty suffix leaves budget unchanged", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "hello", TruncateForSuffix("hello", 10, ""))
		assert.Equal(t, "helloworld", TruncateForSuffix("helloworldextra", 10, ""))
	})

	rapid.Check(t, func(rt *rapid.T) {
		base := rapid.String().Draw(rt, "base")
		budget := rapid.IntRange(0, 100).Draw(rt, "budget")
		suffix := rapid.StringN(0, 20, -1).Draw(rt, "suffix")

		got := TruncateForSuffix(base, budget, suffix)
		wantMax := budget - len(suffix)
		if wantMax < 0 {
			wantMax = 0
		}
		require.LessOrEqual(rt, len(got), wantMax, "result must fit the shrunken budget")
		require.LessOrEqual(rt, len(got), len(base), "result must never be longer than the input")
	})
}
