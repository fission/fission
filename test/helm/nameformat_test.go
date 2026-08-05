// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNameFormat pins RFC-0029 phase 2's naming groundwork.
//
// The property that matters most is that `legacy` is the default and changes
// NOTHING: for most Kubernetes kinds a rename is a delete-and-create, so an
// upgrade that silently renamed resources would be an outage, not a cosmetic
// change.
func TestNameFormat(t *testing.T) {
	// The legacy form truncates to 24 characters, which is not a Kubernetes
	// limit — it is what makes two releases collide.
	const legacyTrunc = 24

	// The release name matters: with a short one ("fission") both formats
	// produce the SAME names, so a default-check using it passes whichever
	// format is the default and pins nothing. Use one long enough to
	// distinguish them.
	const distinguishing = "fission-platform-team-alpha"

	t.Run("standard is the default", func(t *testing.T) {
		std := jobNamesFor(t, distinguishing, "standard")
		leg := jobNamesFor(t, distinguishing, "legacy")
		require.NotEqual(t, std, leg, "fixture precondition: the release name must distinguish the two formats")

		implicit := jobNamesFrom(renderAs(t, distinguishing))
		assert.Equal(t, std, implicit, "the chart default must be standard")
		assert.NotEqual(t, leg, implicit, "the chart default must no longer be legacy")
	})

	t.Run("legacy remains available as an escape hatch", func(t *testing.T) {
		leg := jobNamesFor(t, distinguishing, "legacy")
		require.NotEmpty(t, leg)
		for _, n := range leg {
			assert.LessOrEqual(t, len(strings.SplitN(n, "-1.", 2)[0]), legacyTrunc,
				"legacy must still truncate to 24, so an operator can pin the old names")
		}
	})

	// Two release names sharing a 24-char prefix collide under legacy. That is
	// the bug; this asserts it is real rather than theoretical, so the value of
	// `standard` is measurable.
	t.Run("legacy collides for long release names; standard does not", func(t *testing.T) {
		a := "fission-platform-team-alpha-one"
		b := "fission-platform-team-alpha-two"
		require.Equal(t, a[:legacyTrunc], b[:legacyTrunc],
			"fixture precondition: the two release names must share a 24-char prefix")

		legacyA := jobNamesFor(t, a, "legacy")
		legacyB := jobNamesFor(t, b, "legacy")
		require.NotEmpty(t, legacyA)
		assert.Equal(t, legacyA, legacyB,
			"legacy truncation makes two distinct releases produce identical names — the collision this phase exists to fix")

		stdA := jobNamesFor(t, a, "standard")
		stdB := jobNamesFor(t, b, "standard")
		require.NotEmpty(t, stdA)
		assert.NotEqual(t, stdA, stdB,
			"standard must keep distinct releases distinct")
	})

	// Names still have to be legal: the suffixes callers append must fit inside
	// the DNS-label budget.
	t.Run("standard names stay within the DNS-label budget", func(t *testing.T) {
		long := "fission-a-very-long-release-name-for-a-shared-cluster"
		for _, n := range jobNamesFor(t, long, "standard") {
			assert.LessOrEqualf(t, len(n), 63, "generated name %q exceeds the 63-char DNS label limit", n)
		}
	})

	t.Run("an unknown value fails the render", func(t *testing.T) {
		_, err := renderErr(t, "--set", "nameFormat=bogus")
		require.Error(t, err, "a typo must fail the render, not fall back to legacy silently")
		assert.Contains(t, err.Error(), "nameFormat must be")
	})
}
