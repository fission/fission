// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func checkErr(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func TestPoolCache(t *testing.T) {
	ctx := t.Context()
	logger := loggerfactory.GetLogger()
	concurrency := 5
	requestsPerPod := 2

	keyFunc := crd.CacheKeyUG{
		UID: "func",
	}
	keyFunc2 := crd.CacheKeyUG{
		UID: "func2",
	}

	t.Run("Test create new svc ", func(t *testing.T) {
		c1 := NewPoolCache(logger)

		// should return err since no svc is present
		_, err := c1.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}

		c1.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err = c1.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)
	})

	t.Run("Test return error when functions are busy", func(t *testing.T) {
		c2 := NewPoolCache(logger)
		c2.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)
		c2.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)
		// should return err since all functions are busy
		_, err := c2.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})

	t.Run("Test does not list available values when a function svc is deleted", func(t *testing.T) {
		c3 := NewPoolCache(logger)
		c3.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		c3.SetSvcValue(ctx, keyFunc2, "ip2", &FuncSvc{
			Name: "value2",
		}, resource.MustParse("50m"), 10, 0)

		checkErr(c3.DeleteValue(ctx, keyFunc2, "ip2"))

		cc := c3.ListAvailableValue(nil)
		if len(cc) != 0 {
			log.Panicf("expected 0 available items")
		}
		_, err := c3.GetSvcValue(ctx, keyFunc2, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found deleted element")
		}
	})

	t.Run("Test return error when current CPU usage is more then permissible", func(t *testing.T) {
		c4 := NewPoolCache(logger)
		_, err := c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}

		c4.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err = c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)

		c4.SetCPUUtilization(keyFunc, "ip", resource.MustParse("4m"))

		_, err = c4.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})

	t.Run("Test function should not exist when mark deleted is called", func(t *testing.T) {
		c5 := NewPoolCache(logger)
		c5.SetSvcValue(ctx, keyFunc, "ip", &FuncSvc{
			Name: "value",
		}, resource.MustParse("45m"), 10, 0)

		// should not return any error since we added a svc
		_, err := c5.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		checkErr(err)

		c5.MarkFuncDeleted(keyFunc)
		checkErr(c5.DeleteValue(ctx, keyFunc, "ip"))

		_, err = c5.GetSvcValue(ctx, keyFunc, requestsPerPod, concurrency)
		if err == nil {
			log.Panicf("found value when expected it to be nil")
		}
	})
}

func TestPoolCacheRequests(t *testing.T) {
	key := crd.CacheKeyUG{
		UID:        "func",
		Generation: 1,
	}
	type structForTest struct {
		name             string
		requests         int
		concurrency      int
		rpp              int
		simultaneous     int
		failedRequests   int
		retainPods       int
		generationUpdate bool
	}

	for _, tt := range []structForTest{
		{
			name:        "test1",
			requests:    1,
			concurrency: 1,
			rpp:         1,
		},
		{
			name:        "test2",
			requests:    2,
			concurrency: 2,
			rpp:         1,
		},
		{
			name:        "test3",
			requests:    300,
			concurrency: 5,
			rpp:         60,
		},
		{
			name:           "test4",
			requests:       6,
			concurrency:    1,
			rpp:            5,
			failedRequests: 1,
		},
		{
			name:           "test5",
			requests:       6,
			concurrency:    5,
			rpp:            1,
			failedRequests: 1,
		},
		{
			name:         "test6",
			requests:     300,
			concurrency:  5,
			rpp:          60,
			simultaneous: 30,
		},
		{
			name:           "test7",
			requests:       310,
			concurrency:    5,
			rpp:            60,
			simultaneous:   30,
			failedRequests: 10,
		},
		{
			name:        "test8",
			requests:    2,
			concurrency: 2,
			rpp:         1,
			retainPods:  1,
		},
		{
			name:             "test9",
			requests:         10,
			concurrency:      5,
			rpp:              2,
			retainPods:       2,
			generationUpdate: true,
		},
	} {
		t.Run(fmt.Sprintf("scenario-%s", tt.name), func(t *testing.T) {
			var failedRequests, svcCounter uint64
			p := NewPoolCache(loggerfactory.GetLogger())
			wg := sync.WaitGroup{}
			simultaneous := tt.simultaneous
			if simultaneous == 0 {
				simultaneous = 1
			}
			for i := 1; i <= tt.requests; i++ {
				reqno := i
				wg.Go(func() {
					func(reqno int) {
						svc, err := p.GetSvcValue(t.Context(), key, tt.rpp, tt.concurrency)
						if err != nil {
							code, _ := ferror.GetHTTPError(err)
							if code == http.StatusNotFound {
								atomic.AddUint64(&svcCounter, 1)
								address := fmt.Sprintf("svc-%d", atomic.LoadUint64(&svcCounter))
								p.SetSvcValue(t.Context(), key, address, &FuncSvc{
									Name: address,
								}, resource.MustParse("45m"), tt.rpp, tt.retainPods)
							} else {
								t.Log(reqno, "=>", err)
								atomic.AddUint64(&failedRequests, 1)
							}
						} else {
							if svc == nil {
								t.Log(reqno, "=>", "svc is nil")
								atomic.AddUint64(&failedRequests, 1)
							}
							// } else {
							// 	t.Log(reqno, "=>", svc.Name)
							// }
						}
					}(reqno)
				})

				if reqno%simultaneous == 0 {
					wg.Wait()
				}
			}
			wg.Wait()

			require.Equal(t, tt.failedRequests, int(atomic.LoadUint64(&failedRequests)))
			require.Equal(t, tt.concurrency, int(atomic.LoadUint64(&svcCounter)))

			for i := 0; i < tt.concurrency; i++ {
				for j := 0; j < tt.rpp; j++ {
					svcno := i
					wg.Go(func() {
						func(svcno int) {
							p.MarkAvailable(key, fmt.Sprintf("svc-%d", svcno+1))
						}(svcno)
					})
				}
			}
			wg.Wait()
			if tt.generationUpdate {
				newKey := crd.CacheKeyUG{
					UID:        "func",
					Generation: 2,
				}
				address := fmt.Sprintf("svc-%d", svcCounter)
				p.SetSvcValue(t.Context(), newKey, address, &FuncSvc{
					Name: address,
				}, resource.MustParse("45m"), tt.rpp, tt.retainPods)
				funcSvc := p.ListAvailableValue(nil)
				require.Equal(t, tt.concurrency, len(funcSvc))
			} else {
				funcSvc := p.ListAvailableValue(nil)
				require.Equal(t, tt.concurrency-tt.retainPods, len(funcSvc))
			}
		})
	}
}

// TestListAvailableValue_RetainSemantics is the RFC-0025 "warm rollback"
// matrix: the latest generation is always eligible to keep its retained
// pods; a non-latest generation drains (svcRetain forced to 0) UNLESS
// retained reports true for it (an alias still points at it); a deleted
// group always drains regardless of retained; and a nil retained func
// reproduces the pre-RFC-0025 behaviour where every non-latest generation
// drains unconditionally. Each scenario uses one service per cache group so
// which address ListAvailableValue returns is deterministic (map iteration
// order over multiple same-group services is not).
func TestListAvailableValue_RetainSemantics(t *testing.T) {
	logger := loggerfactory.GetLogger()
	set := func(p *PoolCache, uid types.UID, gen int64, addr string, retain int) {
		key := crd.CacheKeyUG{UID: uid, Generation: gen}
		p.SetSvcValue(t.Context(), key, addr,
			&FuncSvc{Name: addr}, resource.MustParse("45m"), 10, retain)
		// SetSvcValue marks the service active (activeRequests++, as it does
		// for a real specialization); ListAvailableValue only considers idle
		// (activeRequests == 0) services reapable, so mark it available again —
		// matching how TestPoolCacheRequests above exercises the same cache.
		p.MarkAvailable(key, addr)
	}
	names := func(svcs []*FuncSvc) []string {
		out := make([]string, len(svcs))
		for i, s := range svcs {
			out[i] = s.Name
		}
		return out
	}

	t.Run("latest generation keeps its retained pod; non-latest drains (nil retained)", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 1) // non-latest; requested retain is irrelevant once forced to 0
		set(p, "fn", 2, "new", 1) // latest; retain honoured, so nothing to reap
		avail := names(p.ListAvailableValue(nil))
		assert.Equal(t, []string{"old"}, avail, "non-latest generation drains, latest generation's retained pod stays")
	})

	t.Run("non-latest generation drains when retained says false", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 1)
		set(p, "fn", 2, "new", 0)
		retained := func(uid types.UID, gen int64) bool { return false }
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"new", "old"}, sortedCopy(avail), "retained=false behaves exactly like nil")
	})

	t.Run("non-latest generation retained by a live alias is kept, not drained", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 1) // non-latest, but an alias retains gen 1
		set(p, "fn", 2, "new", 0) // latest, no retain requested
		retained := func(uid types.UID, gen int64) bool { return uid == "fn" && gen == 1 }
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"new"}, avail, "alias-retained non-latest generation must NOT drain")
	})

	t.Run("deleted group always drains, even when retained", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 1)
		p.MarkFuncDeleted(crd.CacheKeyUG{UID: "fn", Generation: 1})
		retained := func(uid types.UID, gen int64) bool { return true } // would otherwise keep it
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"old"}, avail, "deleted groups drain unconditionally, retained or not")
	})

	t.Run("latest generation is unaffected by retained reporting false for it", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 0)
		set(p, "fn", 2, "new", 1) // latest; retained() is never even consulted for it
		retained := func(uid types.UID, gen int64) bool { return false }
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"old"}, avail, "latest generation keeps its retained pod regardless of retained's answer")
	})

	// The following scenarios lock the "floor at one warm pod" fix: a
	// retained-non-latest generation has no organic traffic to re-warm it
	// (the alias moved away), so svcRetain=0 there is not "less warm" — it is
	// a guaranteed cold start on the very next rollback. The latest
	// generation is deliberately NOT floored (it self-warms from live
	// traffic), and deleted always overrides the floor.

	t.Run("retained non-latest with svcRetain=0 floors to one kept pod, rest reaped", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old-1", 0) // non-latest, RetainPods=0 (CLI default), alias-retained
		set(p, "fn", 1, "old-2", 0)
		set(p, "fn", 2, "new", 1) // latest, retain == its own svc count so it stays silent in this assertion
		retained := func(uid types.UID, gen int64) bool { return uid == "fn" && gen == 1 }
		avail := p.ListAvailableValue(retained)
		require.Len(t, avail, 1, "svcRetain floors to 1, so exactly one of the two pods is reapable")
		assert.Contains(t, []string{"old-1", "old-2"}, avail[0].Name, "the kept-vs-reaped pod is the retained (gen 1) group, not the latest")
	})

	t.Run("retained non-latest with svcRetain=2 keeps both, floor is a no-op", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old-1", 2) // non-latest, explicit RetainPods=2, alias-retained
		set(p, "fn", 1, "old-2", 2)
		set(p, "fn", 2, "new", 1) // latest, stays silent
		retained := func(uid types.UID, gen int64) bool { return uid == "fn" && gen == 1 }
		avail := names(p.ListAvailableValue(retained))
		assert.Empty(t, avail, "an explicit RetainPods above the floor is never lowered by it")
	})

	t.Run("latest generation with svcRetain=0 is NOT floored: drains per idle as today", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 1) // non-latest but retained, stays silent (fully retained, 1 svc)
		set(p, "fn", 2, "new", 0) // latest, RetainPods=0: must drain, not be floored to 1
		retained := func(uid types.UID, gen int64) bool { return uid == "fn" && gen == 1 }
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"new"}, avail, "the floor is asymmetric: it never applies to the latest generation")
	})

	t.Run("deleted and retained with svcRetain=0 still drains: deleted overrides the floor", func(t *testing.T) {
		p := NewPoolCache(logger)
		set(p, "fn", 1, "old", 0)
		p.MarkFuncDeleted(crd.CacheKeyUG{UID: "fn", Generation: 1})
		retained := func(uid types.UID, gen int64) bool { return true } // would otherwise floor to 1
		avail := names(p.ListAvailableValue(retained))
		assert.Equal(t, []string{"old"}, avail, "deleted takes priority over the retain floor, not just the retain exemption")
	})
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// TestReserveCapacity locks the RFC-0002 ensureCapacity contract: the
// check-and-reserve is atomic under the cache lock, rejects at the
// concurrency cap, and the reservation is symmetric with SetSvcValue /
// MarkSpecializationFailure.
func TestReserveCapacity(t *testing.T) {
	key := crd.CacheKeyUG{UID: "reserve-fn", Generation: 1}
	c := NewPoolCache(logr.Discard())

	// concurrency=2: two reservations succeed, the third hits the cap.
	require.NoError(t, c.ReserveCapacity(key, 2, 0))
	require.NoError(t, c.ReserveCapacity(key, 2, 0))
	err := c.ReserveCapacity(key, 2, 0)
	require.Error(t, err)
	fe, ok := err.(ferror.Error)
	require.True(t, ok)
	require.EqualValues(t, ferror.ErrorTooManyRequests, fe.Code)

	// A failed specialization releases its reservation...
	c.MarkSpecializationFailure(key)
	require.NoError(t, c.ReserveCapacity(key, 2, 0))

	// ...and a successful one is consumed by setValue (the pod now counts via
	// len(svcs) instead), keeping the cap exact.
	c.SetSvcValue(t.Context(), key, "10.0.0.1:8888", &FuncSvc{Function: &metav1.ObjectMeta{Name: "fn"}}, resource.MustParse("45m"), 10, 0)
	err = c.ReserveCapacity(key, 2, 0)
	require.Error(t, err, "1 pod + 1 in-flight reservation == cap 2")
}

// TestReserveCapacityMaxPending locks the saturation-storm bound: in-flight
// specializations are capped independently of (and far below) the concurrency
// cap, released symmetrically, and 0 disables the bound (legacy behavior).
func TestReserveCapacityMaxPending(t *testing.T) {
	key := crd.CacheKeyUG{UID: "pending-fn", Generation: 1}
	c := NewPoolCache(logr.Discard())

	// concurrency far above the pending bound: the pending bound must fire
	// first (the c500-collapse storm sat at 88 in-flight, well under the
	// default concurrency of 500).
	require.NoError(t, c.ReserveCapacity(key, 500, 2))
	require.NoError(t, c.ReserveCapacity(key, 500, 2))
	err := c.ReserveCapacity(key, 500, 2)
	require.Error(t, err)
	fe, ok := err.(ferror.Error)
	require.True(t, ok)
	require.EqualValues(t, ferror.ErrorTooManyRequests, fe.Code)

	// A completed specialization frees a pending slot: the pod now counts
	// toward concurrency (len(svcs)), not the in-flight bound.
	c.SetSvcValue(t.Context(), key, "10.0.0.2:8888", &FuncSvc{Function: &metav1.ObjectMeta{Name: "fn"}}, resource.MustParse("45m"), 10, 0)
	require.NoError(t, c.ReserveCapacity(key, 500, 2))

	// A failed one frees it too.
	c.MarkSpecializationFailure(key)
	require.NoError(t, c.ReserveCapacity(key, 500, 2))

	// 0 disables the bound.
	free := crd.CacheKeyUG{UID: "unbounded-fn", Generation: 1}
	for range 50 {
		require.NoError(t, c.ReserveCapacity(free, 0, 0))
	}
}

// TestMarkSpecializationFailureUnknownKey guards the ensureCapacity error
// path: failing a specialization for a function the cache has never seen must
// be a no-op, not a nil-map panic that takes the executor down.
func TestMarkSpecializationFailureUnknownKey(t *testing.T) {
	c := NewPoolCache(logr.Discard())
	c.MarkSpecializationFailure(crd.CacheKeyUG{UID: "never-seen", Generation: 1})
	// The cache survives: a follow-up request still gets served.
	require.NoError(t, c.ReserveCapacity(crd.CacheKeyUG{UID: "never-seen", Generation: 1}, 0, 0))
}

// TestPoolCacheTouchByAddress locks the RFC-0002 tap-liveness fix: the
// router's batched taps must refresh the Atime of pool-cache entries (the
// idle reaper ages on it once the warm path stops calling the executor).
func TestPoolCacheTouchByAddress(t *testing.T) {
	key := crd.CacheKeyUG{UID: "touch-fn", Generation: 1}
	c := NewPoolCache(logr.Discard())

	old := time.Now().Add(-time.Hour)
	fsvc := &FuncSvc{Function: &metav1.ObjectMeta{Name: "fn"}, Address: "10.0.0.9:8888", Atime: old}
	c.SetSvcValue(t.Context(), key, "10.0.0.9:8888", fsvc, resource.MustParse("45m"), 10, 0)
	c.MarkAvailable(key, "10.0.0.9:8888")

	require.NoError(t, c.TouchByAddress("10.0.0.9:8888"))
	require.True(t, fsvc.Atime.After(old), "tap must refresh the pool-cache entry's Atime")

	err := c.TouchByAddress("10.9.9.9:1")
	require.Error(t, err, "unknown address still 404s")
}
