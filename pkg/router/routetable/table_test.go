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

func spec(uid, rv string, fnRVs map[string]string, mutate func(*RouteSpec)) *RouteSpec {
	s := &RouteSpec{
		TriggerUID: types.UID(uid),
		Namespace:  "default",
		Name:       "trig-" + uid,
		TriggerRV:  rv,
		FnRVs:      fnRVs,
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
		res := tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "10"}, nil),
			func() http.Handler { return tagHandler("v1") })
		assert.Equal(t, ShapeChanged, res)
		snap := tbl.Snapshot()
		require.Len(t, snap, 1)
		assert.Equal(t, "v1", serve(t, snap[0].Handler))
	})

	t.Run("identical re-apply is NoChange and never builds", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "10"}, nil),
			func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "10"}, nil), mustNotBuild(t))
		assert.Equal(t, NoChange, res)
	})

	t.Run("trigger RV bump with same shape is HandlerSwapped", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "10"}, nil),
			func() http.Handler { return tagHandler("v1") })
		ref := tbl.Snapshot()[0].Handler
		res := tbl.ApplyTrigger(spec("u1", "2", map[string]string{"fn": "10"}, nil),
			func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, HandlerSwapped, res)
		assert.Equal(t, "v2", serve(t, ref), "the EXISTING ref must now serve the new handler")
		assert.Same(t, ref, tbl.Snapshot()[0].Handler, "ref identity must be stable across swaps")
	})

	t.Run("function RV bump with same shape is HandlerSwapped (the canary tick)", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn-a": "10", "fn-b": "20"}, nil),
			func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn-a": "11", "fn-b": "20"}, nil),
			func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, HandlerSwapped, res)
		assert.Equal(t, "v2", serve(t, tbl.Snapshot()[0].Handler))
	})

	t.Run("path change is ShapeChanged and preserves ref identity", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "10"}, nil),
			func() http.Handler { return tagHandler("v1") })
		ref := tbl.Snapshot()[0].Handler
		res := tbl.ApplyTrigger(spec("u1", "2", map[string]string{"fn": "10"}, func(s *RouteSpec) {
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
		tbl.ApplyTrigger(spec("u1", "1", nil, nil), func() http.Handler { return tagHandler("v1") })
		res := tbl.ApplyTrigger(spec("u1", "2", nil, func(s *RouteSpec) {
			s.Methods = []string{http.MethodGet, http.MethodPost}
		}), func() http.Handler { return tagHandler("v2") })
		assert.Equal(t, ShapeChanged, res)
		res = tbl.ApplyTrigger(spec("u1", "3", nil, func(s *RouteSpec) {
			s.Methods = []string{http.MethodGet, http.MethodPost}
			s.Host = "api.example.com"
		}), func() http.Handler { return tagHandler("v3") })
		assert.Equal(t, ShapeChanged, res)
	})

	t.Run("delete is ShapeChanged once, NoChange after", func(t *testing.T) {
		tbl := New()
		tbl.ApplyTrigger(spec("u1", "1", nil, nil), func() http.Handler { return tagHandler("v1") })
		assert.Equal(t, ShapeChanged, tbl.DeleteTrigger("u1"))
		assert.Equal(t, NoChange, tbl.DeleteTrigger("u1"))
		assert.Empty(t, tbl.Snapshot())
	})

	t.Run("delete by name removes every matching UID and cleans the index", func(t *testing.T) {
		tbl := New()
		// Two UIDs for one name: the recreate-with-missed-delete case.
		tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn": "1"}, nil),
			func() http.Handler { return tagHandler("v1") })
		stale := spec("u2", "1", map[string]string{"fn": "1"}, nil)
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
// delete touch the internal mux, RV bumps are pure swaps.
func TestApplyFunctionDecisionTable(t *testing.T) {
	key := types.NamespacedName{Namespace: "default", Name: "fn"}
	tbl := New()

	assert.Equal(t, ShapeChanged, tbl.ApplyFunction(key, "1", func() http.Handler { return tagHandler("v1") }),
		"first sighting of a function must add its internal route")
	assert.Equal(t, NoChange, tbl.ApplyFunction(key, "1", func() http.Handler {
		t.Fatal("same-RV apply must not build")
		return nil
	}))

	ref := tbl.InternalSnapshot()[0].Handler
	assert.Equal(t, HandlerSwapped, tbl.ApplyFunction(key, "2", func() http.Handler { return tagHandler("v2") }))
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

	tbl.ApplyTrigger(spec("u1", "1", map[string]string{"fn-a": "1"}, nil),
		func() http.Handler { return tagHandler("t1") })
	tbl.ApplyTrigger(spec("u2", "1", map[string]string{"fn-a": "1", "fn-b": "1"}, func(s *RouteSpec) {
		s.ExactPath = "/two"
	}), func() http.Handler { return tagHandler("t2") })

	assert.Len(t, tbl.TriggersForFunction(fnA), 2, "both triggers resolve through fn-a")
	assert.Len(t, tbl.TriggersForFunction(fnB), 1)

	// Re-target u1 from fn-a to fn-b: the index must follow.
	tbl.ApplyTrigger(spec("u1", "2", map[string]string{"fn-b": "1"}, nil),
		func() http.Handler { return tagHandler("t1b") })
	assert.Len(t, tbl.TriggersForFunction(fnA), 1, "u1 no longer resolves through fn-a")
	assert.Len(t, tbl.TriggersForFunction(fnB), 2)

	// Delete u2: its entries drop out of both.
	tbl.DeleteTrigger("u2")
	assert.Empty(t, tbl.TriggersForFunction(fnA))
	require.Len(t, tbl.TriggersForFunction(fnB), 1)
	assert.Equal(t, "trig-u1", tbl.TriggersForFunction(fnB)[0].Name)
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
		tbl.ApplyTrigger(spec(uid, "1", nil, func(s *RouteSpec) {
			s.ExactPath = "/" + uid
		}), func() http.Handler { return tagHandler(uid) })
	}
	snap := tbl.Snapshot()
	require.Len(t, snap, 3)
	assert.Equal(t, []string{"trig-u1", "trig-u2", "trig-u3"},
		[]string{snap[0].Name, snap[1].Name, snap[2].Name}, "snapshot must be name-sorted")

	// Mutate after snapshot: the held snapshot must be unaffected.
	tbl.ApplyTrigger(spec("u1", "2", nil, func(s *RouteSpec) {
		s.ExactPath = "/moved"
	}), func() http.Handler { return tagHandler("u1b") })
	assert.Equal(t, "/u1", snap[0].ExactPath, "snapshot specs are copies, not live pointers")

	pub, internal := tbl.Sizes()
	assert.Equal(t, 3, pub)
	assert.Equal(t, 0, internal)
}
