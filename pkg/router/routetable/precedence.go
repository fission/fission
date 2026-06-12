// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import (
	"net/http"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

// Phase 2 of RFC-0013: specified route precedence and observable conflicts.
//
// gorilla/mux dispatches to the FIRST registered route that matches, so the
// materializer controls precedence purely through registration order. Until
// this phase the order was an accident of cache list order; it is now:
//
//  1. Host-qualified routes before host-less routes.
//  2. Exact paths before prefixes.
//  3. Among prefixes, longest prefix first.
//  4. Method sets filter rather than rank (a non-matching method falls
//     through to the next route / 405, exactly as gorilla already behaves).
//  5. Exact-duplicate shapes (same host + same path/prefix, overlapping
//     methods): oldest creationTimestamp first, then lexicographic
//     namespace/name. The loser stays registered (shadowed — it starts
//     serving the moment the winner is deleted) and is reported as a
//     Conflict so the router can set RouteAdmitted=False/RouteConflict.
//
// For non-overlapping route sets — the overwhelmingly common case — this
// ordering is behavior-identical to any other, since order only matters when
// two routes match the same request.

// Route is one mux registration (a dual-registration trigger contributes
// two: its exact path and its prefix subtree).
type Route struct {
	Exact   bool // exact path (mux.Handle) vs prefix (mux.PathPrefix)
	Path    string
	Host    string
	Methods []string
	Handler *HandlerRef
	Owner   types.NamespacedName // owning trigger, for logs
}

// Conflict reports a shadowed route: Loser registered the same shape as
// Winner (with overlapping methods) and lost the precedence tiebreak.
type Conflict struct {
	Loser  types.NamespacedName
	Winner types.NamespacedName
}

// Materialization is everything a mux build needs, derived atomically under
// one table lock.
type Materialization struct {
	Routes      []Route
	Conflicts   []Conflict
	HomeClaimed bool // a user trigger claims GET / exactly (suppresses the GKE fallback)
}

// Materialization flattens the public table into precedence-ordered route
// registrations plus the conflicts the ordering shadowed.
func (t *Table) Materialization() Materialization {
	t.mu.Lock()
	specs := make([]RouteSpec, 0, len(t.public))
	for _, spec := range t.public {
		specs = append(specs, *spec)
	}
	t.mu.Unlock()

	var m Materialization
	var exact, prefix []Route
	for i := range specs {
		spec := &specs[i]
		owner := types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name}
		if spec.ExactPath != "" {
			exact = append(exact, Route{
				Exact: true, Path: spec.ExactPath, Host: spec.Host,
				Methods: spec.Methods, Handler: spec.Handler, Owner: owner,
			})
		}
		if spec.PrefixPath != "" {
			prefix = append(prefix, Route{
				Path: spec.PrefixPath, Host: spec.Host,
				Methods: spec.Methods, Handler: spec.Handler, Owner: owner,
			})
		}
		if spec.PrefixPath == "" && spec.ExactPath == "/" &&
			len(spec.Methods) == 1 && spec.Methods[0] == http.MethodGet {
			m.HomeClaimed = true
		}
	}

	// byCreation orders duplicate-shape candidates: oldest creationTimestamp
	// first, then namespace/name. Used as the within-group tiebreak too, so
	// the whole ordering is deterministic.
	specByOwner := make(map[types.NamespacedName]*RouteSpec, len(specs))
	for i := range specs {
		specByOwner[types.NamespacedName{Namespace: specs[i].Namespace, Name: specs[i].Name}] = &specs[i]
	}
	byCreation := func(a, b Route) int {
		sa, sb := specByOwner[a.Owner], specByOwner[b.Owner]
		if !sa.Created.Equal(&sb.Created) {
			if sa.Created.Before(&sb.Created) {
				return -1
			}
			return 1
		}
		return cmpNamespacedName(a.Owner, b.Owner)
	}
	hostedFirst := func(a, b Route) int {
		switch {
		case a.Host != "" && b.Host == "":
			return -1
		case a.Host == "" && b.Host != "":
			return 1
		default:
			return 0
		}
	}
	slices.SortFunc(exact, func(a, b Route) int {
		if c := hostedFirst(a, b); c != 0 {
			return c
		}
		return byCreation(a, b)
	})
	slices.SortFunc(prefix, func(a, b Route) int {
		if c := hostedFirst(a, b); c != 0 {
			return c
		}
		// Longest prefix first: the more specific subtree wins the overlap.
		if len(a.Path) != len(b.Path) {
			return len(b.Path) - len(a.Path)
		}
		return byCreation(a, b)
	})

	// Group order implements rules 1+2 jointly: a hosted route of either
	// kind outranks every host-less route, and exact outranks prefix within
	// a host class.
	split := func(rs []Route) (hosted, hostless []Route) {
		for _, r := range rs {
			if r.Host != "" {
				hosted = append(hosted, r)
			} else {
				hostless = append(hostless, r)
			}
		}
		return hosted, hostless
	}
	hostedExact, hostlessExact := split(exact)
	hostedPrefix, hostlessPrefix := split(prefix)
	m.Routes = make([]Route, 0, len(exact)+len(prefix))
	m.Routes = append(m.Routes, hostedExact...)
	m.Routes = append(m.Routes, hostedPrefix...)
	m.Routes = append(m.Routes, hostlessExact...)
	m.Routes = append(m.Routes, hostlessPrefix...)

	m.Conflicts = findConflicts(m.Routes)
	return m
}

// findConflicts scans the precedence-ordered routes for exact-duplicate
// shapes: same kind (exact/prefix), host, and path, with overlapping method
// sets. The first occurrence (highest precedence) is the winner; later ones
// are shadowed. A trigger is reported at most once even if both halves of
// its dual registration are shadowed.
func findConflicts(routes []Route) []Conflict {
	type key struct {
		exact bool
		host  string
		path  string
	}
	winners := make(map[key]Route)
	var conflicts []Conflict
	seenLoser := make(map[types.NamespacedName]struct{})
	for _, r := range routes {
		k := key{exact: r.Exact, host: r.Host, path: r.Path}
		w, taken := winners[k]
		if !taken {
			winners[k] = r
			continue
		}
		if w.Owner == r.Owner {
			continue // the same trigger cannot conflict with itself
		}
		if !methodsOverlap(w.Methods, r.Methods) {
			// Disjoint methods both serve (rule 4: methods filter, not
			// rank) — not a conflict. The winner stays the method-space
			// reference point; ties among three-plus routes with mixed
			// methods resolve pairwise against the first registration.
			continue
		}
		if _, dup := seenLoser[r.Owner]; dup {
			continue
		}
		seenLoser[r.Owner] = struct{}{}
		conflicts = append(conflicts, Conflict{
			Loser:  r.Owner,
			Winner: w.Owner,
		})
	}
	return conflicts
}

// methodsOverlap reports whether two method sets share an element. An empty
// set matches no request, so it cannot conflict with anything.
func methodsOverlap(a, b []string) bool {
	for _, m := range a {
		if slices.Contains(b, m) {
			return true
		}
	}
	return false
}

// String renders a route for logs.
func (r Route) String() string {
	var sb strings.Builder
	if r.Host != "" {
		sb.WriteString(r.Host)
	}
	sb.WriteString(r.Path)
	if !r.Exact {
		sb.WriteString("*")
	}
	return sb.String()
}
