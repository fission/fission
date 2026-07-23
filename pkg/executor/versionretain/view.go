// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package versionretain answers one question for the executor's idle reaper
// (RFC-0025 "warm rollback" correction): is this (function UID, generation)
// pin still referenced by a live FunctionAlias? Before this package,
// poolcache.PoolCache.ListAvailableValue forced svcRetain to 0 for every
// generation but the latest, so rolling an alias back to an older
// FunctionVersion always paid a cold start — the very thing an alias move is
// meant to avoid. A generation an alias still points at is now an
// independent retain reason alongside "latest generation".
package versionretain

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// retainKey identifies one (function UID, generation) pin.
type retainKey struct {
	uid types.UID
	gen int64
}

// View is a read-mostly, eventually-consistent set of (function UID,
// generation) pins that at least one FunctionAlias currently references —
// directly (spec.Version / spec.SecondaryVersion, during a weighted rollout)
// or as resolved (status.ResolvedVersion, the digest-pinned path). It is fed
// by Rebuild, which the versionretain reconcilers call with a full List of
// FunctionAliases and FunctionVersions on any create/update/delete of either
// kind.
//
// A full recompute on every event — rather than incremental per-key
// patching — is a deliberate simplicity trade: alias and FunctionVersion
// counts are small per function (not per-request), so relisting both kinds
// and rebuilding the set from scratch is cheap, and it sidesteps every
// bookkeeping bug a diff-based update could hide (a stale entry surviving an
// alias repoint, a missed delete). The reaper's own idle ticks run on the
// order of seconds, so the set does not need lower propagation latency than
// a List+Rebuild affords.
type View struct {
	mu       sync.RWMutex
	retained map[retainKey]struct{}
}

// New returns an empty View. Nothing is retained until the first Rebuild —
// callers that construct a View ahead of the reconciler registration that
// feeds it (see RegisterReconcilers) see "nothing retained" in the interim,
// matching ListAvailableValue's pre-RFC-0025 behaviour.
func New() *View {
	return &View{retained: make(map[retainKey]struct{})}
}

// Retained reports whether (uid, gen) is referenced by a live FunctionAlias.
// Its signature matches the `retained` parameter poolcache.ListAvailableValue
// and idle.NewPoolDeleteStrategy accept, so a *View can be plumbed in as-is.
// Safe for concurrent use.
func (v *View) Retained(uid types.UID, gen int64) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.retained[retainKey{uid: uid, gen: gen}]
	return ok
}

// Rebuild recomputes the retained set from a full snapshot of
// FunctionAliases and FunctionVersions and atomically swaps it in. It is a
// pure function of its inputs — no k8s client, no informer — so it is
// unit-testable without a Manager or fake clientset.
//
// For each alias, the referenced FunctionVersion NAMES are
// {spec.Version, spec.SecondaryVersion, status.ResolvedVersion} (empty names
// skipped); each is resolved to a FunctionVersion in the alias's own
// namespace. A name with no matching FunctionVersion in that namespace
// contributes nothing — the alias may be mid-resolution (PackageDigest path,
// ResolvedVersion not yet written), or point at a version already garbage
// collected. That is never treated as an error: an unresolved reference
// simply retains nothing extra, leaving ListAvailableValue's existing
// latest-generation rule as the only retain reason for that entry.
func (v *View) Rebuild(aliases []fv1.FunctionAlias, versions []fv1.FunctionVersion) {
	type nsName struct {
		namespace string
		name      string
	}
	byNsName := make(map[nsName]*fv1.FunctionVersion, len(versions))
	for i := range versions {
		ver := &versions[i]
		byNsName[nsName{namespace: ver.Namespace, name: ver.Name}] = ver
	}

	next := make(map[retainKey]struct{}, len(aliases))
	for i := range aliases {
		alias := &aliases[i]
		for _, name := range [...]string{alias.Spec.Version, alias.Spec.SecondaryVersion, alias.Status.ResolvedVersion} {
			if name == "" {
				continue
			}
			ver, ok := byNsName[nsName{namespace: alias.Namespace, name: name}]
			if !ok || ver.Spec.FunctionUID == "" {
				continue
			}
			next[retainKey{uid: ver.Spec.FunctionUID, gen: ver.Spec.FunctionGeneration}] = struct{}{}
		}
	}

	v.mu.Lock()
	v.retained = next
	v.mu.Unlock()
}
