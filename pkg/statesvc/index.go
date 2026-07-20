// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// keyspaceRef identifies a keyspace within a namespace (Scope.Owner is the
// fixed StateOwner, so it carries no information here).
type keyspaceRef struct {
	namespace string
	keyspace  string
}

// fnState is one Function's declared state config, as indexed.
type fnState struct {
	ref           keyspaceRef
	maxValueBytes int64
	maxKeys       int64
	defaultTTL    time.Duration
}

// FunctionIndex is the reconciler-fed view of every Function's StateConfig,
// keyed for the two lookups the head needs: is (ns, keyspace) claimed by any
// live Function (token defense-in-depth), and what quota/TTL governs it. It
// implements statestore.QuotaResolver. Multiple Functions claiming one
// keyspace is legal but discouraged; the index resolves to the MINIMUM quota
// across claimants so a lax claimant can never widen a stricter one's budget.
type FunctionIndex struct {
	mu    sync.RWMutex
	byFn  map[types.NamespacedName]fnState
	byRef map[keyspaceRef]map[types.NamespacedName]struct{}
}

func NewFunctionIndex() *FunctionIndex {
	return &FunctionIndex{
		byFn:  make(map[types.NamespacedName]fnState),
		byRef: make(map[keyspaceRef]map[types.NamespacedName]struct{}),
	}
}

// Upsert records fn's StateConfig (which must be non-nil).
func (ix *FunctionIndex) Upsert(fn types.NamespacedName, sc *fv1.StateConfig) {
	st := fnState{
		ref:           keyspaceRef{namespace: fn.Namespace, keyspace: sc.EffectiveKeyspace(fn.Name)},
		maxValueBytes: sc.EffectiveMaxValueBytes(),
		maxKeys:       sc.EffectiveMaxKeys(),
	}
	if sc.DefaultTTL != nil {
		st.defaultTTL = sc.DefaultTTL.Duration
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(fn)
	ix.byFn[fn] = st
	claimants := ix.byRef[st.ref]
	if claimants == nil {
		claimants = make(map[types.NamespacedName]struct{})
		ix.byRef[st.ref] = claimants
	}
	claimants[fn] = struct{}{}
}

// Delete removes fn from the index (no-op if absent).
func (ix *FunctionIndex) Delete(fn types.NamespacedName) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(fn)
}

func (ix *FunctionIndex) removeLocked(fn types.NamespacedName) {
	st, ok := ix.byFn[fn]
	if !ok {
		return
	}
	delete(ix.byFn, fn)
	if claimants := ix.byRef[st.ref]; claimants != nil {
		delete(claimants, fn)
		if len(claimants) == 0 {
			delete(ix.byRef, st.ref)
		}
	}
}

// Known reports whether any live Function claims (namespace, keyspace).
func (ix *FunctionIndex) Known(namespace, keyspace string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.byRef[keyspaceRef{namespace: namespace, keyspace: keyspace}]) > 0
}

// ClaimedByOther reports whether a Function other than fn claims (ns, keyspace)
// — the finalizer's "shared keyspace" purge guard.
func (ix *FunctionIndex) ClaimedByOther(fn types.NamespacedName, namespace, keyspace string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for claimant := range ix.byRef[keyspaceRef{namespace: namespace, keyspace: keyspace}] {
		if claimant != fn {
			return true
		}
	}
	return false
}

// lookup returns the effective quota and TTL for a keyspace: the MINIMUM
// across its claimants' effective values (per-function defaults were applied
// at Upsert, so a function declaring a quota ABOVE the platform default gets
// it — the default is a fallback, never a ceiling; a lax claimant can never
// widen a stricter one's budget). An unclaimed keyspace gets the platform
// defaults (defensive — unclaimed keyspaces are only reachable on the admin
// path).
func (ix *FunctionIndex) lookup(namespace, keyspace string) (statestore.Quota, time.Duration) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	claimants := ix.byRef[keyspaceRef{namespace: namespace, keyspace: keyspace}]
	if len(claimants) == 0 {
		return statestore.Quota{
			MaxValueBytes: fv1.DefaultStateMaxValueBytes,
			MaxKeys:       fv1.DefaultStateMaxKeys,
		}, 0
	}

	var (
		q   statestore.Quota
		ttl time.Duration
	)
	first := true
	for claimant := range claimants {
		st := ix.byFn[claimant]
		if first {
			q = statestore.Quota{MaxValueBytes: st.maxValueBytes, MaxKeys: st.maxKeys}
			first = false
		} else {
			q.MaxValueBytes = min(q.MaxValueBytes, st.maxValueBytes)
			q.MaxKeys = min(q.MaxKeys, st.maxKeys)
		}
		if st.defaultTTL > 0 && (ttl == 0 || st.defaultTTL < ttl) {
			ttl = st.defaultTTL
		}
	}
	return q, ttl
}

// Resolve implements statestore.QuotaResolver for the scoped store.
func (ix *FunctionIndex) Resolve(s statestore.Scope) statestore.Quota {
	q, _ := ix.lookup(s.Namespace, s.Keyspace)
	return q
}

// DefaultTTL returns the write-default TTL for a keyspace (0 = none).
func (ix *FunctionIndex) DefaultTTL(namespace, keyspace string) time.Duration {
	_, ttl := ix.lookup(namespace, keyspace)
	return ttl
}
