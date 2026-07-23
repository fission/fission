// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import "strings"

// backendKeySep separates the function name from a pinned version in a
// BackendKey. Function names are Kubernetes DNS-1123 labels
// (lowercase alphanumeric and '-', RFC 1123), which cannot contain '@', so
// '@' is safe as an unambiguous separator: ParseBackendKey never mistakes
// part of a name for the version, or vice versa.
const backendKeySep = "@"

// BackendKey identifies one resolved routing backend: either a live
// Function (version == "", the key is just name — byte-identical with the
// pre-versioning key so unversioned routes are unaffected) or one pinned
// FunctionVersion snapshot of it (version != "", key is "name@version").
// It is the map key RouteSpec.FnGens, resolveResult.functionMap, and
// functionWeightDistribution.name all share so a versioned backend has one
// consistent identity across the resolver, route table, and handler.
func BackendKey(name, version string) string {
	if version == "" {
		return name
	}
	return name + backendKeySep + version
}

// ParseBackendKey splits a BackendKey back into its function name and
// pinned version (version is "" when key names the live Function rather
// than a pinned snapshot). The inverse of BackendKey. A key with no '@'
// separator returns it unchanged as name with an empty version.
func ParseBackendKey(key string) (name, version string) {
	name, version, found := strings.Cut(key, backendKeySep)
	if !found {
		return key, ""
	}
	return name, version
}
