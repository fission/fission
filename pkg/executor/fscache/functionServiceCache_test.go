// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestFunctionServiceCache(t *testing.T) {
	logger := loggerfactory.GetLogger()

	fsc := MakeFunctionServiceCache(logger)
	require.NotNil(t, fsc)

	var fsvc *FuncSvc
	now := time.Now()

	objects := []apiv1.ObjectReference{
		{
			Kind:       "pod",
			Name:       "xxx",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
		{
			Kind:       "pod",
			Name:       "xxx2",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
	}

	fsvc = &FuncSvc{
		Function: &metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
		Environment: &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo-env",
				UID:  "2323",
			},
			Spec: fv1.EnvironmentSpec{
				Version: 1,
				Runtime: fv1.Runtime{
					Image: "fission/foo-env",
				},
				Builder: fv1.Builder{},
			},
		},
		Address:           "xxx",
		KubernetesObjects: objects,
		Ctime:             now,
		Atime:             now,
	}
	_, err := fsc.Add(*fsvc)
	require.NoError(t, err)

	_, err = fsc.GetByFunction(fsvc.Function)
	require.NoError(t, err)

	f, err := fsc.GetByFunctionUID(fsvc.Function.UID)
	require.NoError(t, err)

	fsvc.Atime = f.Atime
	fsvc.Ctime = f.Ctime
	require.Equal(t, fsvc.Address, f.Address)

	err = fsc.TouchByAddress(fsvc.Address)
	require.NoError(t, err)

	// TODO: fix flaky test
	// deleted, err := fsc.DeleteOld(fsvc, 0)
	// require.NoError(t, err)
	// require.False(t, deleted)

	_, err = fsc.GetByFunction(fsvc.Function)
	require.NoError(t, err)

	_, err = fsc.GetByFunctionUID(fsvc.Function.UID)
	require.NoError(t, err)
}

func TestFunctionServiceNewCache(t *testing.T) {
	logger := loggerfactory.GetLogger()

	fsc := MakeFunctionServiceCache(logger)
	require.NotNil(t, fsc)

	var fsvc *FuncSvc
	now := time.Now()

	objects := []apiv1.ObjectReference{
		{
			Kind:       "pod",
			Name:       "xxx",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
		{
			Kind:       "pod",
			Name:       "xxx2",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
	}

	fsvc = &FuncSvc{
		Function: &metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
		Environment: &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo-env",
				UID:  "2323",
			},
			Spec: fv1.EnvironmentSpec{
				Version: 1,
				Runtime: fv1.Runtime{
					Image: "fission/foo-env",
				},
				Builder: fv1.Builder{},
			},
		},
		Address:           "xxx",
		KubernetesObjects: objects,
		CPULimit:          resource.MustParse("5m"),
		Ctime:             now.Add(-2 * time.Minute),
		Atime:             now.Add(-1 * time.Minute),
	}
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
	}

	ctx := t.Context()

	fsc.AddFunc(ctx, *fsvc, 10, fn.GetRetainPods())
	concurrency := 10
	_, err := fsc.GetFuncSvc(ctx, fsvc.Function, 5, concurrency)
	require.NoError(t, err)

	key := crd.CacheKeyUGFromMeta(&fn.ObjectMeta)
	fsc.MarkAvailable(key, fsvc.Address)

	_, err = fsc.GetFuncSvc(ctx, fsvc.Function, 5, concurrency)
	require.NoError(t, err)

	for range 2 {
		fsc.MarkAvailable(key, fsvc.Address)
	}
	vals, err := fsc.ListOldForPool(30*time.Second, nil)
	require.NoError(t, err)
	require.Equal(t, 0, len(vals))

	vals, err = fsc.ListOldForPool(0, nil)
	require.NoError(t, err)
	require.Equal(t, 1, len(vals))

	fsvc.Address = "xxx2"
	fn.Spec.RetainPods = 2
	fsc.AddFunc(ctx, *fsvc, 10, fn.GetRetainPods())

	vals, err = fsc.ListOldForPool(0, nil)
	require.NoError(t, err)
	require.Equal(t, 0, len(vals))
}

// TestFunctionServiceCacheConcurrentTouchAndList validates that the operations
// the removed actor used to mutually exclude — the TouchByAddress Atime write
// and the ListOld/ListOldForPool/Log scans — stay race-free under the cache
// lock. The race detector is the real assertion here.
func TestFunctionServiceCacheConcurrentTouchAndList(t *testing.T) {
	fsc := MakeFunctionServiceCache(loggerfactory.GetLogger())
	require.NotNil(t, fsc)

	now := time.Now()
	const fns = 6
	// Populate byFunction/byAddress/byFunctionUID (via Add) and the pool cache.
	for i := range fns {
		addr := fmt.Sprintf("10.0.0.%d:8888", i)
		_, err := fsc.Add(FuncSvc{
			Function: &metav1.ObjectMeta{Name: fmt.Sprintf("fn-%d", i), UID: types.UID(fmt.Sprintf("uid-%d", i))},
			Address:  addr,
			Ctime:    now,
			Atime:    now,
		})
		require.NoError(t, err)

		poolKey := crd.CacheKeyUG{UID: types.UID(fmt.Sprintf("pool-%d", i)), Generation: 1}
		poolAddr := fmt.Sprintf("10.1.0.%d:8888", i)
		fsc.connFunctionCache.SetSvcValue(t.Context(), poolKey, poolAddr,
			&FuncSvc{Function: &metav1.ObjectMeta{Name: fmt.Sprintf("pool-fn-%d", i)}, Address: poolAddr, Atime: now},
			resource.MustParse("45m"), 10, 0)
		fsc.connFunctionCache.MarkAvailable(poolKey, poolAddr)
	}

	const workers = 24
	const iters = 120
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iters {
				switch i % 4 {
				case 0:
					_ = fsc.TouchByAddress(fmt.Sprintf("10.0.0.%d:8888", (w+i)%fns))
				case 1:
					_, _ = fsc.ListOld(time.Millisecond)
				case 2:
					_, _ = fsc.ListOldForPool(time.Millisecond, nil)
				case 3:
					fsc.Log()
				}
			}
		}(w)
	}
	wg.Wait()
}

// TestListOldPartialReturnOnDanglingIndex locks the one deliberate behavior
// change in the actor->lock refactor: a byFunctionUID entry with no matching
// byFunction entry (a TOCTOU gap under concurrent delete) must make ListOld
// log and return the entries it did resolve — not hang. The old actor `return`ed
// out of its service() loop on this byFunction.Get miss, never sent a response,
// and permanently wedged every later cache request.
func TestListOldPartialReturnOnDanglingIndex(t *testing.T) {
	fsc := MakeFunctionServiceCache(loggerfactory.GetLogger())
	require.NotNil(t, fsc)

	// In-package access lets us forge the dangling secondary-index state the
	// concurrent-delete race would otherwise produce: present in byFunctionUID,
	// absent in byFunction.
	fsc.byFunctionUID.Upsert(types.UID("ghost-uid"), metav1.ObjectMeta{Name: "ghost", UID: types.UID("ghost-uid")})

	done := make(chan struct{})
	go func() {
		vals, err := fsc.ListOld(0)
		require.NoError(t, err)
		require.Empty(t, vals)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ListOld hung on a dangling byFunctionUID entry (the actor-wedge regression)")
	}
}

// TestGetByFunctionStatusOnlyResourceVersionBump locks the #3596
// status-churn fix at the byFunction cache: ResourceVersion moves on
// status-only writes (not just spec changes), so a status-only bump
// observed by a later caller must still hit the entry Add() populated for
// an earlier RV of the same UID+Generation — an RV-keyed lookup would miss
// it and, worse, Add() would create a second entry for the "same" function.
func TestGetByFunctionStatusOnlyResourceVersionBump(t *testing.T) {
	fsc := MakeFunctionServiceCache(loggerfactory.GetLogger())
	require.NotNil(t, fsc)

	before := &metav1.ObjectMeta{
		Name:            "foo",
		UID:             types.UID("fn-uid-1"),
		Generation:      1,
		ResourceVersion: "100",
	}
	now := time.Now()
	fsvc := FuncSvc{
		Function: before,
		Address:  "10.5.5.5:8888",
		Ctime:    now,
		Atime:    now,
	}
	_, err := fsc.Add(fsvc)
	require.NoError(t, err)

	// Same object, later observed with a bumped ResourceVersion (a
	// status-only update) but unchanged UID+Generation.
	after := &metav1.ObjectMeta{
		Name:            "foo",
		UID:             types.UID("fn-uid-1"),
		Generation:      1,
		ResourceVersion: "200",
	}

	got, err := fsc.GetByFunction(after)
	require.NoError(t, err, "status-only RV bump must not miss the byFunction cache entry")
	require.Equal(t, fsvc.Address, got.Address)

	// Adding again under the post-status-update metadata must overwrite the
	// existing entry (IsNameExistError / Add's existing-entry path), not
	// create a second one.
	fsvc2 := fsvc
	fsvc2.Function = after
	existing, err := fsc.Add(fsvc2)
	require.NoError(t, err)
	require.NotNil(t, existing, "Add under a status-only-bumped RV must report the pre-existing entry, not silently create a duplicate")
}

// TestTouchByAddressPoolCacheFallback locks the RFC-0002 tap-liveness fix at
// the FunctionServiceCache layer: poolmgr registers specialized pods only in
// the pool cache (never byAddress), so a byAddress miss MUST fall through to
// connFunctionCache.TouchByAddress — reverting that fallback silently turns
// every warm-path tap into a 404 and the idle reaper ages serving pods on
// their specialization time.
func TestTouchByAddressPoolCacheFallback(t *testing.T) {
	fsc := MakeFunctionServiceCache(loggerfactory.GetLogger())
	require.NotNil(t, fsc)

	key := crd.CacheKeyUG{UID: "pool-only-fn", Generation: 1}
	old := time.Now().Add(-time.Hour)
	fsvc := &FuncSvc{Function: &metav1.ObjectMeta{Name: "fn"}, Address: "10.3.4.5:8888", Atime: old}
	fsc.connFunctionCache.SetSvcValue(t.Context(), key, fsvc.Address, fsvc, resource.MustParse("45m"), 10, 0)
	fsc.connFunctionCache.MarkAvailable(key, fsvc.Address)

	require.NoError(t, fsc.TouchByAddress(fsvc.Address),
		"a pool-cache-only address must be touchable through the fsc")
	require.True(t, fsvc.Atime.After(old), "the fallback must refresh the pool-cache entry's Atime")

	err := fsc.TouchByAddress("10.99.99.99:1")
	require.Error(t, err, "an address unknown to BOTH caches still errors")
}
