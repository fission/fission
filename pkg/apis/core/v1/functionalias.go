// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

// EffectiveTarget returns the FunctionVersion name a FunctionAlias currently
// points at: Spec.Version if the alias is name-pinned, else
// Status.ResolvedVersion (the async-resolved outcome of a PackageDigest
// pin). THIS is the one precedence rule for "what version does this alias
// mean right now" -- every consumer of a FunctionAlias (the router's
// resolveByAlias, the MCP tool reconciler, ...) must go through this method
// rather than re-deriving the fallback locally, so the rule only needs to
// change in one place. Returns "" when the alias has not resolved to a
// version yet.
func (fa *FunctionAlias) EffectiveTarget() string {
	if fa.Spec.Version != "" {
		return fa.Spec.Version
	}
	return fa.Status.ResolvedVersion
}
