// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package expr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/workflow/expr"
)

// TestParse pins the workflow JSONPath dialect (ojg/jp): what parses here is
// the contract admission enforces, so additions to either table are an API
// change, not a library detail.
func TestParse(t *testing.T) {
	t.Parallel()

	valid := []string{
		"$",
		"$.a",
		"$.a.b[0]",
		"$.items[*].id",
		"$['weird key']",
		"$.a[?(@.b > 1)]",
		"$.charge",
	}
	for _, p := range valid {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			_, err := expr.Parse(p)
			assert.NoError(t, err)
		})
	}

	invalid := []string{
		"",
		"a.b",       // relative: every documented example is $-rooted
		"@.a",       // filter-local root outside a filter
		"$.a[?(@.b >]",
		"$[",
	}
	for _, p := range invalid {
		t.Run("invalid_"+p, func(t *testing.T) {
			t.Parallel()
			_, err := expr.Parse(p)
			assert.Error(t, err)
		})
	}
}
