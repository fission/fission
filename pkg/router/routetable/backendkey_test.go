// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

func TestBackendKey(t *testing.T) {
	tests := []struct {
		name    string
		fnName  string
		version string
		want    string
	}{
		{name: "unversioned", fnName: "hello", version: "", want: "hello"},
		{name: "versioned", fnName: "hello", version: "hello-v1", want: "hello@hello-v1"},
		{name: "name containing no @, still safe", fnName: "my-func-name", version: "my-func-name-v42", want: "my-func-name@my-func-name-v42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BackendKey(tt.fnName, tt.version))
		})
	}
}

func TestParseBackendKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		wantName    string
		wantVersion string
	}{
		{name: "unversioned", key: "hello", wantName: "hello", wantVersion: ""},
		{name: "versioned", key: "hello@hello-v1", wantName: "hello", wantVersion: "hello-v1"},
		{name: "no separator at all", key: "plain-name", wantName: "plain-name", wantVersion: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, version := ParseBackendKey(tt.key)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

// TestBackendKeyRoundTrip pins BackendKey/ParseBackendKey as exact inverses
// for both unversioned and versioned identities — including function/version
// names that themselves contain no '@' (the only shape DNS-1123 names ever
// take, which is exactly why '@' is a safe separator; see BackendKey's doc
// comment).
func TestBackendKeyRoundTrip(t *testing.T) {
	cases := []struct{ name, version string }{
		{"hello", ""},
		{"hello", "hello-v1"},
		{"a", "a-v999999"},
		{"my-func-name-63-chars-abcdefghijklmnopqrstuvwxyz0123456789-abc", "v1"},
	}
	for _, c := range cases {
		key := BackendKey(c.name, c.version)
		gotName, gotVersion := ParseBackendKey(key)
		assert.Equal(t, c.name, gotName, "round-trip name for %q/%q", c.name, c.version)
		assert.Equal(t, c.version, gotVersion, "round-trip version for %q/%q", c.name, c.version)
	}
}

// TestReindexLockedStripsBackendKeyVersionSuffix is the plan-review
// regression (warning #4): a versioned trigger's FnGens key is a BackendKey
// ("name@version"), but reindexLocked must index it under the live
// function's plain NamespacedName — otherwise a live-Function event (which
// TriggersForFunction is always queried with the plain name for) would never
// find the versioned trigger, and its route would never re-resolve.
func TestReindexLockedStripsBackendKeyVersionSuffix(t *testing.T) {
	tbl := New()
	fnKey := types.NamespacedName{Namespace: "default", Name: "hello"}

	// A versioned trigger: FnGens is keyed by BackendKey("hello", "hello-v1"),
	// not the plain function name.
	versionedSpec := spec("u1", 1, map[string]int64{BackendKey("hello", "hello-v1"): 1}, nil)
	res := tbl.ApplyTrigger(versionedSpec, func() http.Handler { return tagHandler("v1") })
	assert.Equal(t, ShapeChanged, res)

	// A live-Function event looks the trigger up by the PLAIN function key —
	// exactly what applyFunctionIncremental/reapplyTriggersForFunction do.
	triggers := tbl.TriggersForFunction(fnKey)
	assert.Len(t, triggers, 1, "a versioned trigger must still be indexed under its live function's plain name")
	assert.Equal(t, "trig-u1", triggers[0].Name)

	// A query under the literal BackendKey (as if it were a function name)
	// must find nothing — the index is never keyed on the composite string.
	compositeKey := types.NamespacedName{Namespace: "default", Name: BackendKey("hello", "hello-v1")}
	assert.Empty(t, tbl.TriggersForFunction(compositeKey))

	// Deleting the trigger must clean up the plain-name index entry too.
	tbl.DeleteTrigger("u1")
	assert.Empty(t, tbl.TriggersForFunction(fnKey))
}

// TestReindexLockedWeightedAliasBothTargetsIndexSameFunction covers a
// weighted-alias trigger, whose FnGens carries TWO BackendKeys for the same
// underlying function (primary@vX, secondary@vY): both must strip down to
// the one plain function index entry, and TriggersForFunction must not
// return the trigger twice.
func TestReindexLockedWeightedAliasBothTargetsIndexSameFunction(t *testing.T) {
	tbl := New()
	fnKey := types.NamespacedName{Namespace: "default", Name: "hello"}

	weightedSpec := spec("u1", 1, map[string]int64{
		BackendKey("hello", "hello-v1"): 1,
		BackendKey("hello", "hello-v2"): 1,
	}, nil)
	tbl.ApplyTrigger(weightedSpec, func() http.Handler { return tagHandler("weighted") })

	triggers := tbl.TriggersForFunction(fnKey)
	assert.Len(t, triggers, 1, "both weighted targets of the same function must collapse to one index entry")
}
