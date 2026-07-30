// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// VersionedFunction projects live onto one of its immutable FunctionVersion
// snapshots: an in-memory Function reflecting v's spec, generation, and
// identity label, for callers (executor, router) that need to address a
// specific published version's warm pool rather than whatever the live spec
// currently says. The result is never persisted -- it is a view, not a
// write.
//
// Invariant: one (UID, Generation) maps to at most one FunctionVersion.
// This holds because versions are minted only by versioning.Publish, from
// the live spec, and never any other way:
//   - Idempotence dedups an unchanged spec -- Publish called again with the
//     same snapshot and package digest returns the existing newest version
//     rather than minting a duplicate, so no two versions ever share a
//     snapshot taken at the same live Generation.
//   - Every spec change bumps ObjectMeta.Generation (Kubernetes' own
//     spec-only counter), so a version minted from a changed spec always
//     records a Generation strictly newer than any version minted before
//     it.
//   - A revert to a prior spec still lands on a fresh Generation -- the
//     counter never rewinds -- so reverting never re-targets an
//     already-claimed Generation.
//
// Consequently ObjectMeta.Generation, set below from v.Spec.FunctionGeneration,
// uniquely pins v's identity: it is exactly the (UID, Generation) pair the
// executor's pool/cache key (crd.CacheKeyUG) and the Service/pod labels
// derived from it already key on, so a versioned Function projection slots
// into that machinery without a second identity scheme.
func VersionedFunction(live *fv1.Function, v *fv1.FunctionVersion) *fv1.Function {
	fn := live.DeepCopy()
	fn.Spec = *v.Spec.Snapshot.DeepCopy()
	fn.Generation = v.Spec.FunctionGeneration

	// Copy-on-write: fn.Labels is already an independent map courtesy of
	// live.DeepCopy() (metav1.ObjectMeta deep-copies map fields), so setting
	// the version label here can never reach back into live.Labels.
	if fn.Labels == nil {
		fn.Labels = make(map[string]string, 1)
	}
	fn.Labels[fv1.FUNCTION_VERSION] = v.Name

	return fn
}
