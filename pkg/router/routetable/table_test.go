// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

// tagHandler returns a handler that writes its tag, so tests can observe
// which handler a ref currently serves.
func tagHandler(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tag))
	})
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	return rr.Body.String()
}

func spec(uid string, gen int64, fnGens map[string]int64, mutate func(*RouteSpec)) *RouteSpec {
	s := &RouteSpec{
		TriggerUID: types.UID(uid),
		Namespace:  "default",
		Name:       "trig-" + uid,
		TriggerGen: gen,
		FnGens:     fnGens,
		ExactPath:  "/path",
		Methods:    []string{http.MethodGet},
	}
	if mutate != nil {
		mutate(s)
	}
	return s
}

// mustBuild returns a build func that fails the test if called.
func mustNotBuild(t *testing.T) func() http.Handler {
	t.Helper()
	return func() http.Handler {
		t.Fatal("build must not be called for a NoChange apply")
		return nil
	}
}

// TestApplyTriggerDecisionTable pins the table's change-detection contract:
// which (shape, triggerRV, fnRVs) deltas yield which ApplyResult, and when
// the build callback runs.
func TestApplyTriggerDecisionTable(t *testing.T) {
	t.Run("insert is ShapeChanged and builds", func(t *testing.T) {
		tbl := New()
		res := tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 10}, nil),
			func() http.Handler { return tagHandler("v1") })
		assert.Equal(t, ShapeChanged, res)
		snap := tbl.Snapshot()
		require.Len(t, snap, 1)
		assert.Equal(t, "v1", serve(t, snap[0].Handler))
	})

	t.Run("identical re-apply is NoChange and never builds", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 10}, nil),
			func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 10}, nil), mustNotBuild(t))
		assert.Equal(t, NoChange, res)
	})

	t.Run("trigger generation bump with same shape is HandlerSwapped", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 10}, nil),
			func() http.Handler { return tagHandler("v1") })
		ref := tbl.Snapshot()[0].Handler
		res := tbl.ApplyTrigger(spec("u1", 2, map[string]int64{"fn": 10}, nil),
			func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, HandlerSwapped, res)
		assert.Equal(t, "v2", serve(t, ref), "the EXISTING ref must now serve the new handler")
		assert.Same(t, ref, tbl.Snapshot()[0].Handler, "ref identity must be stable across swaps")
	})

	t.Run("function generation bump with same shape is HandlerSwapped (the canary tick)", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn-a": 10, "fn-b": 20}, nil),
			func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn-a": 11, "fn-b": 20}, nil),
			func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, HandlerSwapped, res)
		assert.Equal(t, "v2", serve(t, tbl.Snapshot()[0].Handler))
	})

	t.Run("path change is ShapeChanged and preserves ref identity", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 10}, nil),
			func() http.Handler { return tagHandler("v1") })
		ref := tbl.Snapshot()[0].Handler
		res := tbl.ApplyTrigger(spec("u1", 2, map[string]int64{"fn": 10}, func(s *RouteSpec) {
			s.ExactPath = "/moved"
		}), func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, ShapeChanged, res)
		snap := tbl.Snapshot()
		require.Len(t, snap, 1)
		assert.Equal(t, "/moved", snap[0].ExactPath)
		assert.Same(t, ref, snap[0].Handler, "shape change must keep the same HandlerRef")
		assert.Equal(t, "v2", serve(t, ref))
	})

	t.Run("methods and host changes are shape changes", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, nil, nil), func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", 2, nil, func(s *RouteSpec) {
			s.Methods = []string{http.MethodGet, http.MethodPost}
		}), func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, ShapeChanged, res)
		res = tbl.ApplyTrigger(spec("u1", 3, nil, func(s *RouteSpec) {
			s.Methods = []string{http.MethodGet, http.MethodPost}
			s.Host = "api.example.com"
		}), func() http.Handler { return tagHandler("v3") })
		assert.Equal(t, ShapeChanged, res)
	})

	t.Run("delete is ShapeChanged once, NoChange after", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", 1, nil, nil), func() http.Handler { return tagHandler("v1") })
		assert.Equal(t, ShapeChanged, tbl.DeleteTrigger("u1"))
		assert.Equal(t, NoChange, tbl.DeleteTrigger("u1"))
		assert.Empty(t, tbl.Snapshot())
	})

	t.Run("delete by name removes every matching UID and cleans the index", func(t *testing.T) {
		tbl := New()
		// Two UIDs for one name: the recreate-with-missed-delete case.
		tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn": 1}, nil),
			func() http.Handler { return tagHandler("v1") })
		stale := spec("u2", 1, map[string]int64{"fn": 1}, nil)
		stale.Name = "trig-u1"
		tbl.ApplyTrigger(stale, func() http.Handler { return tagHandler("v2") })

		key := types.NamespacedName{Namespace: "default", Name: "trig-u1"}
		assert.Equal(t, ShapeChanged, tbl.DeleteTriggerByName(key))
		assert.Empty(t, tbl.Snapshot())
		assert.Empty(t, tbl.TriggersForFunction(types.NamespacedName{Namespace: "default", Name: "fn"}))
		assert.Equal(t, NoChange, tbl.DeleteTriggerByName(key))
	})
}

// TestApplyFunctionDecisionTable pins the internal-route contract: insert /
// delete touch the internal mux, generation bumps are pure swaps.
func TestApplyFunctionDecisionTable(t *testing.T) {
	key := InternalKey{NamespacedName: types.NamespacedName{Namespace: "default", Name: "fn"}}
	tbl := New()

	assert.Equal(t, ShapeChanged, tbl.ApplyFunction(key, 1, func() http.Handler { return tagHandler("v1") }),
		"first sighting of a function must add its internal route")
	assert.Equal(t, NoChange, tbl.ApplyFunction(key, 1, func() http.Handler {
		t.Fatal("same-generation apply must not build")
		return nil
	}))

	ref := tbl.InternalSnapshot()[0].Handler
	assert.Equal(t, HandlerSwapped, tbl.ApplyFunction(key, 2, func() http.Handler { return tagHandler("v2") }))
	assert.Equal(t, "v2", serve(t, ref), "function update must swap in place")

	assert.Equal(t, ShapeChanged, tbl.DeleteFunction(key))
	assert.Equal(t, NoChange, tbl.DeleteFunction(key))
	assert.Empty(t, tbl.InternalSnapshot())
}

// TestFnIndexMaintenance pins the function→triggers index across re-targeting
// and deletion: a function event must find exactly the triggers currently
// resolving through it.
func TestFnIndexMaintenance(t *testing.T) {
	tbl := New()
	fnA := types.NamespacedName{Namespace: "default", Name: "fn-a"}
	fnB := types.NamespacedName{Namespace: "default", Name: "fn-b"}

	tbl.ApplyTrigger(spec("u1", 1, map[string]int64{"fn-a": 1}, nil),
		func() http.Handler { return tagHandler("t1") })
	tbl.ApplyTrigger(spec("u2", 1, map[string]int64{"fn-a": 1, "fn-b": 1}, func(s *RouteSpec) {
		s.ExactPath = "/two"
	}), func() http.Handler { return tagHandler("t2") })

	assert.Len(t, tbl.TriggersForFunction(fnA), 2, "both triggers resolve through fn-a")
	assert.Len(t, tbl.TriggersForFunction(fnB), 1)

	// Re-target u1 from fn-a to fn-b: the index must follow.
	tbl.ApplyTrigger(spec("u1", 2, map[string]int64{"fn-b": 1}, nil),
		func() http.Handler { return tagHandler("t1b") })
	assert.Len(t, tbl.TriggersForFunction(fnA), 1, "u1 no longer resolves through fn-a")
	assert.Len(t, tbl.TriggersForFunction(fnB), 2)

	// Delete u2: its entries drop out of both.
	tbl.DeleteTrigger("u2")
	assert.Empty(t, tbl.TriggersForFunction(fnA))
	require.Len(t, tbl.TriggersForFunction(fnB), 1)
	assert.Equal(t, "trig-u1", tbl.TriggersForFunction(fnB)[0].Name)
}

// TestAliasIndexMaintenance pins the alias→triggers index (RFC-0025)
// end-to-end: RouteSpec.Aliases is mirrored into aliasIndex by reindexLocked
// exactly like FnGens is for fnIndex, TriggersForAlias finds the resolved
// triggers, MarkUnresolved's alias half lets an unresolved reference be found
// too, and both cascades clean up on delete — the alias index gets the same
// maintenance guarantees as fnIndex, for free, from the same code path.
func TestAliasIndexMaintenance(t *testing.T) {
	tbl := New()
	prodAlias := types.NamespacedName{Namespace: "default", Name: "prod"}
	stagingAlias := types.NamespacedName{Namespace: "default", Name: "staging"}

	u1 := spec("u1", 1, map[string]int64{"hello@hello-v1": 1}, func(s *RouteSpec) {
		s.Aliases = []string{"prod"}
	})
	tbl.ApplyTrigger(u1, func() http.Handler { return tagHandler("t1") })
	u2 := spec("u2", 1, map[string]int64{"hello@hello-v2": 1}, func(s *RouteSpec) {
		s.ExactPath = "/two"
		s.Aliases = []string{"prod", "staging"}
	})
	tbl.ApplyTrigger(u2, func() http.Handler { return tagHandler("t2") })

	assert.Len(t, tbl.TriggersForAlias(prodAlias.Namespace, prodAlias.Name), 2, "both triggers resolve through prod")
	assert.Len(t, tbl.TriggersForAlias(stagingAlias.Namespace, stagingAlias.Name), 1)

	// Re-target u1 from prod to staging: the index must follow, mirroring
	// TestFnIndexMaintenance's re-target case.
	u1b := spec("u1", 2, map[string]int64{"hello@hello-v3": 1}, func(s *RouteSpec) {
		s.Aliases = []string{"staging"}
	})
	tbl.ApplyTrigger(u1b, func() http.Handler { return tagHandler("t1b") })
	assert.Len(t, tbl.TriggersForAlias(prodAlias.Namespace, prodAlias.Name), 1, "u1 no longer resolves through prod")
	assert.Len(t, tbl.TriggersForAlias(stagingAlias.Namespace, stagingAlias.Name), 2)

	// Delete u2: its entries drop out of both, exactly like fnIndex.
	tbl.DeleteTrigger("u2")
	assert.Empty(t, tbl.TriggersForAlias(prodAlias.Namespace, prodAlias.Name))
	require.Len(t, tbl.TriggersForAlias(stagingAlias.Namespace, stagingAlias.Name), 1)
	assert.Equal(t, "trig-u1", tbl.TriggersForAlias(stagingAlias.Namespace, stagingAlias.Name)[0].Name)

	// Unresolved alias reference: a trigger whose alias does not exist yet
	// must still be found by TriggersForAlias (mirroring the unresolved-fn
	// cascade), and clearing it on a successful apply must remove it.
	early := types.NamespacedName{Namespace: "default", Name: "early"}
	tbl.MarkUnresolved(early, nil, []types.NamespacedName{{Namespace: "default", Name: "canary"}})
	found := tbl.TriggersForAlias("default", "canary")
	require.Len(t, found, 1)
	assert.Equal(t, "early", found[0].Name)

	tbl.DeleteTriggerByName(early)
	assert.Empty(t, tbl.TriggersForAlias("default", "canary"), "delete-by-name clears unresolved alias edges too")
}

// TestInternalKeySuffixIsolation pins InternalKey's identity contract
// (RFC-0025): a live function's own route (Suffix "") and its materialized
// `:<alias>`/`:<version>` siblings share a NamespacedName but are
// independent routes — applying, swapping, or deleting one must not touch
// the others.
func TestInternalKeySuffixIsolation(t *testing.T) {
	tbl := New()
	fn := types.NamespacedName{Namespace: "default", Name: "hello"}
	plainKey := InternalKey{NamespacedName: fn}
	aliasKey := InternalKey{NamespacedName: fn, Suffix: "prod"}
	versionKey := InternalKey{NamespacedName: fn, Suffix: "hello-v1"}

	assert.Equal(t, ShapeChanged, tbl.ApplyFunction(plainKey, 1, func() http.Handler { return tagHandler("plain") }))
	assert.Equal(t, ShapeChanged, tbl.ApplyFunction(aliasKey, 1, func() http.Handler { return tagHandler("alias") }))
	assert.Equal(t, ShapeChanged, tbl.ApplyFunction(versionKey, 1, func() http.Handler { return tagHandler("version") }))
	require.Len(t, tbl.InternalSnapshot(), 3)

	// Swapping the alias route's generation must not touch the plain or
	// version routes.
	assert.Equal(t, HandlerSwapped, tbl.ApplyFunction(aliasKey, 2, func() http.Handler { return tagHandler("alias-v2") }))
	byKey := map[InternalKey]InternalSpec{}
	for _, s := range tbl.InternalSnapshot() {
		byKey[s.Key] = s
	}
	assert.Equal(t, "plain", serve(t, byKey[plainKey].Handler))
	assert.Equal(t, "alias-v2", serve(t, byKey[aliasKey].Handler))
	assert.Equal(t, "version", serve(t, byKey[versionKey].Handler))

	// Deleting the plain route must leave the alias/version siblings alone.
	assert.Equal(t, ShapeChanged, tbl.DeleteFunction(plainKey))
	require.Len(t, tbl.InternalSnapshot(), 2)
}

// TestInternalKeysBySuffix pins the FunctionAlias/FunctionVersion DELETE
// path's CANDIDATE lookup: the object (and with it the function-name half of
// InternalKey) is already gone, so the deleted route can only be FOUND by
// namespace + suffix — but, unlike DeleteTriggerByName's by-name lookup on
// the public side, this must NOT itself delete: a suffix can legitimately be
// shared across different functions in the same namespace (an alias
// "hello-v1" on function "world" and a FunctionVersion "hello-v1" on
// function "hello" are two independent, equally-live routes), so the method
// only enumerates candidates — scoping which one actually orphaned is the
// caller's job (incremental.go's deleteInternalRouteBySuffix, which Gets
// each candidate's live claimant before deleting).
func TestInternalKeysBySuffix(t *testing.T) {
	tbl := New()
	prod := InternalKey{NamespacedName: types.NamespacedName{Namespace: "default", Name: "hello"}, Suffix: "prod"}
	otherFnSameSuffix := InternalKey{NamespacedName: types.NamespacedName{Namespace: "default", Name: "world"}, Suffix: "prod"}
	otherNS := InternalKey{NamespacedName: types.NamespacedName{Namespace: "other", Name: "hello"}, Suffix: "prod"}
	plain := InternalKey{NamespacedName: types.NamespacedName{Namespace: "default", Name: "hello"}}

	for _, k := range []InternalKey{prod, otherFnSameSuffix, otherNS, plain} {
		tbl.ApplyFunction(k, 1, func() http.Handler { return tagHandler(k.Name + ":" + k.Suffix) })
	}
	require.Len(t, tbl.InternalSnapshot(), 4)

	got := tbl.InternalKeysBySuffix("default", "prod")
	assert.ElementsMatch(t, []InternalKey{prod, otherFnSameSuffix}, got,
		"both default-namespace :prod routes are candidates, regardless of function name")
	assert.NotContains(t, got, otherNS, "a same-suffix route in a DIFFERENT namespace is not a candidate")
	assert.NotContains(t, got, plain, "the function's own (Suffix-less) route is not a candidate")

	assert.Empty(t, tbl.InternalKeysBySuffix("default", "no-such-suffix"))

	// Read-only: the table must be unchanged after the lookup.
	require.Len(t, tbl.InternalSnapshot(), 4)
}

// TestHandlerRefSwapUnderConcurrentServe drives sustained traffic through a
// ref while another goroutine swaps handlers; run with -race. Every response
// must be a complete write from SOME version — no torn or empty responses.
func TestHandlerRefSwapUnderConcurrentServe(t *testing.T) {
	ref := NewHandlerRef(tagHandler("v0"))
	stop := make(chan struct{})
	var swapper, servers sync.WaitGroup

	swapper.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
				ref.Swap(tagHandler(fmt.Sprintf("v%d", i%8)))
			}
		}
	})

	for range 4 {
		servers.Go(func() {
			for range 2000 {
				rr := httptest.NewRecorder()
				ref.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
				body := rr.Body.String()
				if len(body) < 2 || body[0] != 'v' {
					t.Errorf("torn response: %q", body)
					return
				}
			}
		})
	}

	servers.Wait()
	close(stop)
	swapper.Wait()
}

// TestSnapshotDeterminismAndIsolation pins that snapshots are (a) sorted
// deterministically and (b) copies — mutating the table after a snapshot
// does not change what a materializer already iterated.
func TestSnapshotDeterminismAndIsolation(t *testing.T) {
	tbl := New()
	for _, uid := range []string{"u3", "u1", "u2"} {
		tbl.ApplyTrigger(spec(uid, 1, nil, func(s *RouteSpec) {
			s.ExactPath = "/" + uid
		}), func() http.Handler { return tagHandler(uid) })
	}
	snap := tbl.Snapshot()
	require.Len(t, snap, 3)
	assert.Equal(t, []string{"trig-u1", "trig-u2", "trig-u3"},
		[]string{snap[0].Name, snap[1].Name, snap[2].Name}, "snapshot must be name-sorted")

	// Mutate after snapshot: the held snapshot must be unaffected.
	tbl.ApplyTrigger(spec("u1", 2, nil, func(s *RouteSpec) {
		s.ExactPath = "/moved"
	}), func() http.Handler { return tagHandler("u1b") })
	assert.Equal(t, "/u1", snap[0].ExactPath, "snapshot specs are copies, not live pointers")

	pub, internal := tbl.Sizes()
	assert.Equal(t, 3, pub)
	assert.Equal(t, 0, internal)
}
