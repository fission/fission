// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCacheKeyUGFromMeta_StableAcrossStatusUpdates(t *testing.T) {
	t.Parallel()

	// A function's metadata at two points in time: same UID + Generation
	// (no spec change), but different ResourceVersion (a status update
	// bumped it). The cache key MUST be identical — otherwise
	// UnTapService (keyed on CacheKeyUG) misses the entry created by
	// SetSvcValue and leaks activeRequests.
	uid := types.UID("fn-uid-1")
	gen := int64(3)

	before := &metav1.ObjectMeta{
		UID:             uid,
		Generation:      gen,
		ResourceVersion: "100",
	}
	after := &metav1.ObjectMeta{
		UID:             uid,
		Generation:      gen,
		ResourceVersion: "200", // status update bumped RV
	}

	keyBefore := CacheKeyUGFromMeta(before)
	keyAfter := CacheKeyUGFromMeta(after)

	assert.Equal(t, keyBefore, keyAfter,
		"CacheKeyUG must be stable across status-only updates (RV change, same UID+Generation)")
	assert.Equal(t, keyBefore.String(), keyAfter.String(),
		"CacheKeyUG.String() must be stable across status-only updates")
}

func TestCacheKeyUGFromMeta_ChangesOnSpecUpdate(t *testing.T) {
	t.Parallel()

	// A spec update increments Generation. The cache key MUST change —
	// the old generation's specialized pods are stale and should not be
	// reused under the new key.
	uid := types.UID("fn-uid-1")

	before := &metav1.ObjectMeta{
		UID:             uid,
		Generation:      3,
		ResourceVersion: "100",
	}
	after := &metav1.ObjectMeta{
		UID:             uid,
		Generation:      4, // spec update bumped Generation
		ResourceVersion: "101",
	}

	keyBefore := CacheKeyUGFromMeta(before)
	keyAfter := CacheKeyUGFromMeta(after)

	assert.NotEqual(t, keyBefore, keyAfter,
		"CacheKeyUG must change on spec updates (Generation increment)")
}

func TestCacheKeyUGFromMeta_ChangesOnUIDChange(t *testing.T) {
	t.Parallel()

	// Different function (different UID) — different key, even if
	// Generation and ResourceVersion happen to match.
	meta1 := &metav1.ObjectMeta{
		UID:             types.UID("fn-uid-1"),
		Generation:      1,
		ResourceVersion: "100",
	}
	meta2 := &metav1.ObjectMeta{
		UID:             types.UID("fn-uid-2"),
		Generation:      1,
		ResourceVersion: "100",
	}

	assert.NotEqual(t, CacheKeyUGFromMeta(meta1), CacheKeyUGFromMeta(meta2),
		"CacheKeyUG must differ for different UIDs")
}

func TestCacheKeyUG_String(t *testing.T) {
	t.Parallel()

	ck := CacheKeyUG{UID: types.UID("abc"), Generation: 7}
	assert.Equal(t, "abc_7", ck.String())
}

func TestCacheKeyUGFromObject_StableAcrossStatusUpdates(t *testing.T) {
	t.Parallel()

	// Mirrors TestCacheKeyUGFromMeta_StableAcrossStatusUpdates for the
	// metav1.Object-typed constructor (used where callers hold a
	// *fv1.Function rather than a bare ObjectMeta, e.g. the executor
	// dispatcher's dedup key).
	uid := types.UID("fn-uid-1")
	gen := int64(3)

	before := &metav1.ObjectMeta{UID: uid, Generation: gen, ResourceVersion: "100"}
	after := &metav1.ObjectMeta{UID: uid, Generation: gen, ResourceVersion: "200"}

	assert.Equal(t, CacheKeyUGFromObject(before), CacheKeyUGFromObject(after),
		"CacheKeyUGFromObject must be stable across status-only updates (RV change, same UID+Generation)")
}

func TestCacheKeyUGFromObject_ChangesOnSpecUpdate(t *testing.T) {
	t.Parallel()

	uid := types.UID("fn-uid-1")
	before := &metav1.ObjectMeta{UID: uid, Generation: 3, ResourceVersion: "100"}
	after := &metav1.ObjectMeta{UID: uid, Generation: 4, ResourceVersion: "101"}

	assert.NotEqual(t, CacheKeyUGFromObject(before), CacheKeyUGFromObject(after),
		"CacheKeyUGFromObject must change on spec updates (Generation increment)")
}

func TestCacheKeyUGFromObject_MatchesCacheKeyUGFromMeta(t *testing.T) {
	t.Parallel()

	meta := &metav1.ObjectMeta{UID: types.UID("fn-uid-1"), Generation: 2, ResourceVersion: "50"}
	assert.Equal(t, CacheKeyUGFromMeta(meta), CacheKeyUGFromObject(meta),
		"the two constructors must agree for the same object")
}
