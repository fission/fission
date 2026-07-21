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

func TestCacheKeyURGFromMeta_StableAcrossStatusUpdates(t *testing.T) {
	t.Parallel()

	// A function's metadata at two points in time: same UID + Generation
	// (no spec change), but different ResourceVersion (a status update
	// bumped it). The cache key MUST be identical — otherwise
	// UnTapService (keyed on CacheKeyURG) misses the entry created by
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

	keyBefore := CacheKeyURGFromMeta(before)
	keyAfter := CacheKeyURGFromMeta(after)

	assert.Equal(t, keyBefore, keyAfter,
		"CacheKeyURG must be stable across status-only updates (RV change, same UID+Generation)")
	assert.Equal(t, keyBefore.String(), keyAfter.String(),
		"CacheKeyURG.String() must be stable across status-only updates")
}

func TestCacheKeyURGFromMeta_ChangesOnSpecUpdate(t *testing.T) {
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

	keyBefore := CacheKeyURGFromMeta(before)
	keyAfter := CacheKeyURGFromMeta(after)

	assert.NotEqual(t, keyBefore, keyAfter,
		"CacheKeyURG must change on spec updates (Generation increment)")
}

func TestCacheKeyURGFromMeta_ChangesOnUIDChange(t *testing.T) {
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

	assert.NotEqual(t, CacheKeyURGFromMeta(meta1), CacheKeyURGFromMeta(meta2),
		"CacheKeyURG must differ for different UIDs")
}

func TestCacheKeyURG_String(t *testing.T) {
	t.Parallel()

	ck := CacheKeyURG{UID: types.UID("abc"), Generation: 7}
	assert.Equal(t, "abc_7", ck.String())
}
