// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestFunctionServiceMap(t *testing.T) {
	logger := loggerfactory.GetLogger()

	m := makeFunctionServiceMap(logger, 0)
	fn := &metav1.ObjectMeta{Name: "foo", Namespace: metav1.NamespaceDefault}
	u, err := url.Parse("/foo012")
	if err != nil {
		t.Errorf("can't parse url")
	}

	m.assign(fn, u)

	v, err := m.lookup(fn)
	if err != nil {
		t.Errorf("Lookup error: %v", err)
	}
	if *v != *u {
		t.Errorf("Expected %#v, got %#v", u, v)
	}

	fn.Name = "bar"
	_, err2 := m.lookup(fn)
	if err2 == nil {
		t.Errorf("No error on missing entry")
	}
}

// TestFunctionServiceMapStatusOnlyResourceVersionBump locks the #3596
// status-churn fix at the router's functionServiceMap: ResourceVersion moves
// on status-only writes (not just spec changes), and the router's informer
// cache can lag the executor's view, so an RV-keyed lookup misses the entry
// assign() populated for the pre-status-update object. Keying on Generation
// instead (stable across status updates) must make the second lookup hit.
func TestFunctionServiceMapStatusOnlyResourceVersionBump(t *testing.T) {
	logger := loggerfactory.GetLogger()

	m := makeFunctionServiceMap(logger, 0)
	u, err := url.Parse("/foo012")
	require.NoError(t, err)

	before := &metav1.ObjectMeta{
		Name:            "foo",
		Namespace:       metav1.NamespaceDefault,
		UID:             types.UID("fn-uid-1"),
		Generation:      1,
		ResourceVersion: "100",
	}
	m.assign(before, u)

	// Same object, later observed with a bumped ResourceVersion (a
	// status-only update) but unchanged Generation.
	after := &metav1.ObjectMeta{
		Name:            "foo",
		Namespace:       metav1.NamespaceDefault,
		UID:             types.UID("fn-uid-1"),
		Generation:      1,
		ResourceVersion: "200",
	}

	v, err := m.lookup(after)
	require.NoError(t, err, "status-only RV bump must not miss the cache entry")
	require.Equal(t, *u, *v)

	// A cache should have exactly one entry for the two RVs of the same
	// generation: assigning under "after" must overwrite, not duplicate.
	m.assign(after, u)
	require.Len(t, m.cache.Copy(), 1, "status-only RV bump must not split the cache entry")
}
