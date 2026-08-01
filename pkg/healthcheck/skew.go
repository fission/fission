// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package healthcheck

import (
	"fmt"
	"strconv"
	"strings"
)

// SupportedMinorSkew is how many minor releases the CLI may differ from the
// control plane and still be supported.
//
// It is 1 because RELEASES.md promises fixes for the latest TWO minor lines:
// with 1.N and 1.N-1 both supported, a CLI and a cluster on adjacent minors are
// both in the window, and anything further apart is not. Changing the support
// window means changing this constant, and vice versa — they are one policy.
const SupportedMinorSkew = 1

// SkewVerdict is the outcome of comparing a client and server version.
type SkewVerdict int

const (
	// SkewSame — identical minor. Nothing to report.
	SkewSame SkewVerdict = iota
	// SkewSupported — different minor, still inside the support window. This
	// is the normal state during a staged rollout and must NOT be an error.
	SkewSupported
	// SkewUnsupported — outside the window; behaviour is not guaranteed.
	SkewUnsupported
	// SkewUnknown — at least one version could not be parsed (a dev build, a
	// source build, an unreachable control plane).
	SkewUnknown
)

// semver is a parsed major.minor.patch, ignoring any pre-release or build
// suffix and a leading "v".
type semver struct {
	major, minor, patch int
}

// parseSemver parses "v1.27.0", "1.27.0", "1.27.0-rc1" and "1.27" leniently.
// It reports ok=false for anything it cannot read as major.minor, including the
// empty string and dev builds, so callers can distinguish "no skew" from "no
// idea" rather than silently treating an unparsable version as 0.0.0.
func parseSemver(v string) (semver, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return semver{}, false
	}
	// Drop pre-release / build metadata: 1.27.0-rc1+abc -> 1.27.0
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return semver{}, false
	}
	out := semver{}
	var err error
	if out.major, err = strconv.Atoi(parts[0]); err != nil {
		return semver{}, false
	}
	if out.minor, err = strconv.Atoi(parts[1]); err != nil {
		return semver{}, false
	}
	if len(parts) > 2 {
		// A non-numeric patch is tolerated: the skew rule only reads
		// major.minor, so refusing the whole version over it would report
		// "unknown" for versions we can in fact judge.
		out.patch, _ = strconv.Atoi(parts[2])
	}
	return out, true
}

// CheckVersionSkew judges a client version against a server version under the
// support window in RELEASES.md.
//
// A DIFFERING MINOR IS NOT AN ERROR. The previous check required exact string
// equality, which fails during any staged rollout — the CLI is upgraded on a
// laptop or in CI at a different moment from the cluster — and contradicts the
// published policy of supporting two minor lines. It also failed on any patch
// difference, which the policy explicitly permits.
//
// A differing MAJOR is always unsupported: nothing in the policy promises
// compatibility across a major.
func CheckVersionSkew(clientVersion, serverVersion string) (SkewVerdict, string) {
	c, cok := parseSemver(clientVersion)
	s, sok := parseSemver(serverVersion)
	if !cok || !sok {
		return SkewUnknown, fmt.Sprintf(
			"cannot compare versions (client %q, server %q); this is expected for dev or source builds",
			clientVersion, serverVersion)
	}

	if c.major != s.major {
		return SkewUnsupported, fmt.Sprintf(
			"client %s and server %s are on different major versions; compatibility is not supported across a major",
			clientVersion, serverVersion)
	}

	skew := c.minor - s.minor
	if skew < 0 {
		skew = -skew
	}
	switch {
	case skew == 0:
		return SkewSame, ""
	case skew <= SupportedMinorSkew:
		return SkewSupported, fmt.Sprintf(
			"client %s and server %s differ by one minor version; both are inside the supported window",
			clientVersion, serverVersion)
	default:
		return SkewUnsupported, fmt.Sprintf(
			"client %s and server %s are %d minor versions apart; only %d is supported (see RELEASES.md). Upgrade the CLI or the cluster before relying on this client",
			clientVersion, serverVersion, skew, SupportedMinorSkew)
	}
}
