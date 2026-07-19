// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package expr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/workflow/expr"
)

func mustParse(t *testing.T, p string) expr.Path {
	t.Helper()
	path, err := expr.Parse(p)
	require.NoError(t, err)
	return path
}

func TestGet(t *testing.T) {
	t.Parallel()

	doc := map[string]any{
		"a": map[string]any{"b": 1.5},
		"items": []any{
			map[string]any{"id": "x"},
			map[string]any{"id": "y"},
		},
		"null": nil,
	}

	cases := []struct {
		path    string
		want    any
		matched bool
	}{
		{"$", doc, true},
		{"$.a.b", 1.5, true},
		{"$.items[1].id", "y", true},
		{"$.items[*].id", "x", true}, // multiple matches: Get pins the FIRST
		{"$.null", nil, true},        // an explicit null IS a match
		{"$.missing", nil, false},    // no match -> (nil, false); callers map to JSON null
		{"$.a.b.c", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got, ok := mustParse(t, tc.path).Get(doc)
			assert.Equal(t, tc.matched, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSetResult(t *testing.T) {
	t.Parallel()

	t.Run("root replaces the document", func(t *testing.T) {
		t.Parallel()
		got, err := mustParse(t, "$").SetResult(map[string]any{"old": true}, "new")
		require.NoError(t, err)
		assert.Equal(t, "new", got)
	})

	t.Run("nested write into existing parent", func(t *testing.T) {
		t.Parallel()
		// int64 values: alt.Dup normalizes ints, and real documents come from
		// JSON decoding anyway (float64/int64), never bare int.
		doc := map[string]any{"order": map[string]any{"id": int64(1)}}
		got, err := mustParse(t, "$.charge").SetResult(doc, map[string]any{"ok": true})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"order":  map[string]any{"id": int64(1)},
			"charge": map[string]any{"ok": true},
		}, got)
	})

	t.Run("missing map parents auto-create (Step Functions parity)", func(t *testing.T) {
		t.Parallel()
		got, err := mustParse(t, "$.a.b.c").SetResult(map[string]any{}, 1)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}}, got)
	})

	t.Run("descending through a scalar is an error, not a silent drop", func(t *testing.T) {
		t.Parallel()
		_, err := mustParse(t, "$.a.b").SetResult(map[string]any{"a": 5}, 1)
		require.Error(t, err, "the result cannot be written; dropping it silently is the worst default")
	})

	t.Run("original document is not mutated", func(t *testing.T) {
		t.Parallel()
		doc := map[string]any{"keep": 1}
		_, err := mustParse(t, "$.x").SetResult(doc, 2)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"keep": 1}, doc)
	})
}
