// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// applySpec inserts a spec with a tag handler.
func applySpec(t *testing.T, tbl *Table, s *RouteSpec) {
	t.Helper()
	res := tbl.ApplyTrigger(s, func() http.Handler { return tagHandler(s.Name) })
	require.Equal(t, ShapeChanged, res)
}

func pSpec(name string, created time.Time, mutate func(*RouteSpec)) *RouteSpec {
	s := &RouteSpec{
		TriggerUID: types.UID("uid-" + name),
		Namespace:  "default",
		Name:       name,
		TriggerGen: 1,
		Methods:    []string{http.MethodGet},
		Created:    metav1.NewTime(created),
	}
	mutate(s)
	return s
}

// routeOrder extracts "owner/kind" strings for order assertions.
func routeOrder(m Materialization) []string {
	out := make([]string, 0, len(m.Routes))
	for _, r := range m.Routes {
		kind := "prefix"
		if r.Exact {
			kind = "exact"
		}
		out = append(out, r.Owner.Name+":"+kind)
	}
	return out
}

// TestMaterializationPrecedenceOrder pins the four registration groups and
// the within-group ordering rules.
func TestMaterializationPrecedenceOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tbl := New()
	// Insert deliberately out of precedence order.
	applySpec(t, tbl, pSpec("hostless-short-prefix", t0, func(s *RouteSpec) {
		s.PrefixPath = "/api/"
	}))
	applySpec(t, tbl, pSpec("hostless-long-prefix", t0.Add(time.Hour), func(s *RouteSpec) {
		s.PrefixPath = "/api/v1/"
	}))
	applySpec(t, tbl, pSpec("hostless-exact", t0, func(s *RouteSpec) {
		s.ExactPath = "/api/v1/users"
	}))
	applySpec(t, tbl, pSpec("hosted-prefix", t0, func(s *RouteSpec) {
		s.PrefixPath = "/api/"
		s.Host = "api.example.com"
	}))
	applySpec(t, tbl, pSpec("hosted-exact", t0, func(s *RouteSpec) {
		s.ExactPath = "/api/v1/users"
		s.Host = "api.example.com"
	}))
	// Dual registration: contributes to BOTH exact and prefix groups.
	applySpec(t, tbl, pSpec("hostless-dual", t0, func(s *RouteSpec) {
		s.ExactPath = "/files"
		s.PrefixPath = "/files/"
	}))

	m := tbl.Materialization()
	assert.Equal(t, []string{
		"hosted-exact:exact",
		"hosted-prefix:prefix",
		"hostless-dual:exact", // equal timestamps: lexicographic name tiebreak ("-dual" < "-exact")
		"hostless-exact:exact",
		"hostless-long-prefix:prefix", // longest prefix ("/api/v1/", 8) first
		"hostless-dual:prefix",        // "/files/" (7)
		"hostless-short-prefix:prefix",
	}, routeOrder(m))
	assert.Empty(t, m.Conflicts, "no identical shapes here")
}

// TestMaterializationConflicts pins rule 5: identical shapes with
// overlapping methods are conflicts (oldest creation wins, ns/name breaks
// timestamp ties), disjoint methods are NOT, and a dual-registration loser
// is reported once.
func TestMaterializationConflicts(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("identical exact path, oldest wins", func(t *testing.T) {
		tbl := New()
		applySpec(t, tbl, pSpec("younger", t0.Add(time.Hour), func(s *RouteSpec) { s.ExactPath = "/dup" }))
		applySpec(t, tbl, pSpec("older", t0, func(s *RouteSpec) { s.ExactPath = "/dup" }))
		m := tbl.Materialization()
		require.Len(t, m.Conflicts, 1)
		assert.Equal(t, "younger", m.Conflicts[0].Loser.Name)
		assert.Equal(t, "older", m.Conflicts[0].Winner.Name)
		assert.Equal(t, "older:exact", routeOrder(m)[0], "the winner must be registered first")
	})

	t.Run("timestamp tie breaks on namespace/name", func(t *testing.T) {
		tbl := New()
		applySpec(t, tbl, pSpec("bbb", t0, func(s *RouteSpec) { s.ExactPath = "/dup" }))
		applySpec(t, tbl, pSpec("aaa", t0, func(s *RouteSpec) { s.ExactPath = "/dup" }))
		m := tbl.Materialization()
		require.Len(t, m.Conflicts, 1)
		assert.Equal(t, "bbb", m.Conflicts[0].Loser.Name)
		assert.Equal(t, "aaa", m.Conflicts[0].Winner.Name)
	})

	t.Run("disjoint methods both serve, no conflict", func(t *testing.T) {
		tbl := New()
		applySpec(t, tbl, pSpec("getter", t0, func(s *RouteSpec) { s.ExactPath = "/dup" }))
		applySpec(t, tbl, pSpec("poster", t0.Add(time.Hour), func(s *RouteSpec) {
			s.ExactPath = "/dup"
			s.Methods = []string{http.MethodPost}
		}))
		assert.Empty(t, tbl.Materialization().Conflicts)
	})

	t.Run("dual-registration loser reported once", func(t *testing.T) {
		tbl := New()
		applySpec(t, tbl, pSpec("winner", t0, func(s *RouteSpec) {
			s.ExactPath = "/files"
			s.PrefixPath = "/files/"
		}))
		applySpec(t, tbl, pSpec("loser", t0.Add(time.Hour), func(s *RouteSpec) {
			s.ExactPath = "/files"
			s.PrefixPath = "/files/"
		}))
		m := tbl.Materialization()
		require.Len(t, m.Conflicts, 1, "both halves shadowed, one report")
		assert.Equal(t, "loser", m.Conflicts[0].Loser.Name)
	})

	t.Run("different hosts never conflict", func(t *testing.T) {
		tbl := New()
		applySpec(t, tbl, pSpec("host-a", t0, func(s *RouteSpec) {
			s.ExactPath = "/dup"
			s.Host = "a.example.com"
		}))
		applySpec(t, tbl, pSpec("host-b", t0, func(s *RouteSpec) {
			s.ExactPath = "/dup"
			s.Host = "b.example.com"
		}))
		applySpec(t, tbl, pSpec("hostless", t0, func(s *RouteSpec) { s.ExactPath = "/dup" }))
		assert.Empty(t, tbl.Materialization().Conflicts,
			"host-qualified and host-less variants of a path are distinct shapes")
	})
}

// TestMaterializationHomeClaim pins the GKE-fallback suppression signal.
func TestMaterializationHomeClaim(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tbl := New()
	assert.False(t, tbl.Materialization().HomeClaimed)
	applySpec(t, tbl, pSpec("home", t0, func(s *RouteSpec) { s.ExactPath = "/" }))
	assert.True(t, tbl.Materialization().HomeClaimed)
}
