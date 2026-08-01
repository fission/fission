// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package healthcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCheckVersionSkew pins the support window from RELEASES.md: the latest two
// minor lines are supported, so adjacent minors are fine and anything further
// apart is not.
//
// The cases that matter most are the ones the previous exact-equality check got
// wrong — a patch difference and a one-minor difference are both NORMAL, and
// failing them is what made `fission check` noisy enough to ignore.
func TestCheckVersionSkew(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		client, server string
		want           SkewVerdict
	}{
		{"identical", "1.27.0", "1.27.0", SkewSame},
		{"identical with v prefix on one side", "v1.27.0", "1.27.0", SkewSame},
		{"patch differs — explicitly permitted by the policy", "1.27.3", "1.27.0", SkewSame},
		{"pre-release suffix is ignored", "1.27.0-rc1", "1.27.0", SkewSame},
		{"build metadata is ignored", "1.27.0+abc123", "1.27.0", SkewSame},

		{"client one minor ahead — staged rollout", "1.28.0", "1.27.0", SkewSupported},
		{"client one minor behind — staged rollout", "1.26.0", "1.27.0", SkewSupported},

		{"two minors apart is outside the window", "1.29.0", "1.27.0", SkewUnsupported},
		{"two minors behind is outside the window", "1.25.0", "1.27.0", SkewUnsupported},
		{"different major is never supported", "2.0.0", "1.27.0", SkewUnsupported},
		{"different major, adjacent minor number", "2.27.0", "1.27.0", SkewUnsupported},

		{"dev build client", "dev", "1.27.0", SkewUnknown},
		{"empty server (control plane unreachable)", "1.27.0", "", SkewUnknown},
		{"both unparsable", "", "", SkewUnknown},
		{"major only is not enough to judge a minor skew", "1", "1.27.0", SkewUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, detail := CheckVersionSkew(tc.client, tc.server)
			assert.Equal(t, tc.want, got)
			if tc.want == SkewSame {
				assert.Empty(t, detail, "an in-sync pair must produce no message to print")
			} else {
				assert.NotEmpty(t, detail, "every non-trivial verdict must explain itself to the user")
			}
		})
	}
}

// TestCheckVersionSkewIsSymmetric pins that the rule judges DISTANCE, not
// direction: a client one minor ahead of the cluster and one minor behind it
// are equally supported. Only the message differs.
func TestCheckVersionSkewIsSymmetric(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{"1.28.0", "1.27.0"},
		{"1.29.0", "1.27.0"},
		{"1.27.0", "1.27.0"},
	} {
		fwd, _ := CheckVersionSkew(pair[0], pair[1])
		rev, _ := CheckVersionSkew(pair[1], pair[0])
		assert.Equalf(t, fwd, rev, "verdict must not depend on which side is newer (%s vs %s)", pair[0], pair[1])
	}
}

// TestSupportedMinorSkewMatchesPolicy guards the coupling between this constant
// and RELEASES.md. Two supported minor lines means adjacent minors are in the
// window and two-apart is not; if someone widens the support window without
// touching the constant (or the reverse), one of these fails.
func TestSupportedMinorSkewMatchesPolicy(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, SupportedMinorSkew,
		"RELEASES.md promises the latest TWO minor lines, which is a skew of exactly 1")

	adjacent, _ := CheckVersionSkew("1.28.0", "1.27.0")
	assert.Equal(t, SkewSupported, adjacent, "two supported lines must make adjacent minors supported")

	twoApart, _ := CheckVersionSkew("1.29.0", "1.27.0")
	assert.Equal(t, SkewUnsupported, twoApart, "two supported lines must make a two-minor gap unsupported")
}
