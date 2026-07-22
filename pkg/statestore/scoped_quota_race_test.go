// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
)

// RFC-0023 S3 (docs/rfc/specs/quota.tla, QuotaNeverExceeded): the MaxKeys
// budget check is atomic with the write. scopedKV delegates to the driver's
// CountedKV, where the live-key count and the write are one step — the
// AtomicQuota=FALSE config traces exactly the read-check-then-write overshoot
// this design forbids. Counting live keys directly also makes the budget
// TTL-exact: an expired key frees its slot with no external counter to drift.

var rsc = statestore.Scope{Namespace: "ns", Owner: "function-state", Keyspace: "ks"}

func rawAndScoped(t *testing.T, maxKeys int64) (raw statestore.KVStore, kv statestore.KVStore) {
	t.Helper()
	inner, err := memory.New()
	require.NoError(t, err)
	caps := statestore.NewScoped(inner, statestore.StaticQuota(statestore.Quota{MaxKeys: maxKeys}))
	t.Cleanup(func() { _ = caps.Close() })
	kv, err = caps.KV()
	require.NoError(t, err)
	raw, err = inner.KV()
	require.NoError(t, err)
	return raw, kv
}

func liveCount(t *testing.T, raw statestore.KVStore, s statestore.Scope) int64 {
	t.Helper()
	var n int64
	page := statestore.Page{}
	for {
		kp, err := raw.List(t.Context(), s, "", page)
		require.NoError(t, err)
		n += int64(len(kp.Keys))
		if kp.Next == "" {
			return n
		}
		page.Token = kp.Next
	}
}

// TestScopedImplementsCountedKV guards the statestoresvc path: that head wraps
// its driver in NewScoped and type-asserts CountedKV on the result, so the
// scoped wrapper MUST forward the capability (a regression here surfaces as a
// 503 "state backend unavailable" from statesvc, not a test failure).
func TestScopedImplementsCountedKV(t *testing.T) {
	inner, err := memory.New()
	require.NoError(t, err)
	caps := statestore.NewScoped(inner, nil)
	t.Cleanup(func() { _ = caps.Close() })
	kv, err := caps.KV()
	require.NoError(t, err)
	ck, ok := kv.(statestore.CountedKV)
	require.True(t, ok, "scoped KV must satisfy CountedKV for the statestoresvc head")
	// And it must actually enforce the forwarded budget.
	require.NoError(t, ck.SetCounted(t.Context(), rsc, "a", []byte("v"), statestore.SetOptions{}, 1))
	require.ErrorIs(t, ck.SetCounted(t.Context(), rsc, "b", []byte("v"), statestore.SetOptions{}, 1), statestore.ErrQuotaExceeded)
}

func TestScopedQuota_ExistingKeySetConsumesNoSlot(t *testing.T) {
	_, kv := rawAndScoped(t, 2)
	ctx := t.Context()
	require.NoError(t, kv.Set(ctx, rsc, "a", []byte("v"), statestore.SetOptions{}))
	require.NoError(t, kv.Set(ctx, rsc, "b", []byte("v"), statestore.SetOptions{}))
	for range 5 {
		require.NoError(t, kv.Set(ctx, rsc, "a", []byte("v2"), statestore.SetOptions{}))
	}
	require.ErrorIs(t, kv.Set(ctx, rsc, "c", []byte("v"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)
}

func TestScopedQuota_DeleteFreesSlot(t *testing.T) {
	_, kv := rawAndScoped(t, 1)
	ctx := t.Context()
	require.NoError(t, kv.Set(ctx, rsc, "a", []byte("v"), statestore.SetOptions{}))
	require.ErrorIs(t, kv.Set(ctx, rsc, "b", []byte("v"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)
	require.NoError(t, kv.Delete(ctx, rsc, "a", 0))
	require.NoError(t, kv.Set(ctx, rsc, "b", []byte("v"), statestore.SetOptions{}))
}

func TestScopedQuota_TTLExpiryFreesSlot(t *testing.T) {
	raw, kv := rawAndScoped(t, 2)
	ctx := t.Context()
	require.NoError(t, kv.Set(ctx, rsc, "a", []byte("v"), statestore.SetOptions{}))
	require.NoError(t, kv.Set(ctx, rsc, "b", []byte("v"), statestore.SetOptions{TTL: time.Nanosecond}))
	time.Sleep(2 * time.Millisecond) // the driver filters expired keys on read
	require.Equal(t, int64(1), liveCount(t, raw, rsc), "precondition: b expired")
	// The budget is the live count, so the expired key's slot is free.
	require.NoError(t, kv.Set(ctx, rsc, "c", []byte("v"), statestore.SetOptions{}))
	require.ErrorIs(t, kv.Set(ctx, rsc, "d", []byte("v"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)
}

func TestScopedQuota_CallerCASOnMissingKeyIsConflictNotQuota(t *testing.T) {
	_, kv := rawAndScoped(t, 1)
	ctx := t.Context()
	require.NoError(t, kv.Set(ctx, rsc, "a", []byte("v"), statestore.SetOptions{}))
	// At the budget boundary an impossible CAS reports conflict, not quota.
	require.ErrorIs(t, kv.Set(ctx, rsc, "nope", []byte("v"), statestore.SetOptions{IfVersion: new(int64(7))}), statestore.ErrVersionConflict)
}

func TestScopedQuota_MaxValueBytesStillEnforced(t *testing.T) {
	inner, err := memory.New()
	require.NoError(t, err)
	caps := statestore.NewScoped(inner, statestore.StaticQuota(statestore.Quota{MaxKeys: 10, MaxValueBytes: 4}))
	t.Cleanup(func() { _ = caps.Close() })
	kv, err := caps.KV()
	require.NoError(t, err)
	require.NoError(t, kv.Set(t.Context(), rsc, "ok", []byte("1234"), statestore.SetOptions{}))
	require.ErrorIs(t, kv.Set(t.Context(), rsc, "big", []byte("12345"), statestore.SetOptions{}), statestore.ErrQuotaExceeded)
}

// TestScopedQuota_ConcurrentCreatesNeverOvershoot is the direct Go analogue of
// quota.tla's QuotaNeverExceeded under AtomicQuota=TRUE: N racing writers of
// distinct keys against MaxKeys=K must end with exactly K live keys.
func TestScopedQuota_ConcurrentCreatesNeverOvershoot(t *testing.T) {
	const maxKeys, writers = 4, 16
	raw, kv := rawAndScoped(t, maxKeys)

	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			_ = kv.Set(context.Background(), rsc, fmt.Sprintf("key-%02d", i), []byte("v"), statestore.SetOptions{})
		})
	}
	wg.Wait()

	live := liveCount(t, raw, rsc)
	assert.Equal(t, int64(maxKeys), live, "S3: exactly MaxKeys live keys, no overshoot, all slots fillable")
}

// TestScopedQuota_MixedChurnInvariant races creates, overwrites, and deletes;
// the live count must respect MaxKeys at all times (checked after quiescence
// and implicitly throughout by the atomic counted set).
func TestScopedQuota_MixedChurnInvariant(t *testing.T) {
	const maxKeys = 3
	raw, kv := rawAndScoped(t, maxKeys)

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			ctx := context.Background()
			for i := range 40 {
				key := fmt.Sprintf("k%d", (w+i)%6)
				switch i % 3 {
				case 0, 1:
					_ = kv.Set(ctx, rsc, key, []byte("v"), statestore.SetOptions{})
				case 2:
					_ = kv.Delete(ctx, rsc, key, 0)
				}
			}
		})
	}
	wg.Wait()

	assert.LessOrEqual(t, liveCount(t, raw, rsc), int64(maxKeys), "S3: live keys exceed MaxKeys")
}

// TestScopedQuota_RapidSequential drives random single-threaded op sequences
// against a model and checks exact admission bookkeeping after every op.
func TestScopedQuota_RapidSequential(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		maxKeys := rapid.Int64Range(1, 5).Draw(rt, "maxKeys")
		inner, err := memory.New()
		if err != nil {
			rt.Fatal(err)
		}
		caps := statestore.NewScoped(inner, statestore.StaticQuota(statestore.Quota{MaxKeys: maxKeys}))
		defer caps.Close()
		kv, _ := caps.KV()
		ctx := context.Background()

		live := map[string]bool{}
		keys := []string{"a", "b", "c", "d", "e", "f", "g"}

		rt.Repeat(map[string]func(*rapid.T){
			"set": func(rt *rapid.T) {
				key := rapid.SampledFrom(keys).Draw(rt, "key")
				err := kv.Set(ctx, rsc, key, []byte("v"), statestore.SetOptions{})
				switch {
				case live[key]:
					if err != nil {
						rt.Fatalf("overwrite of live key %q failed: %v", key, err)
					}
				case int64(len(live)) >= maxKeys:
					if err == nil {
						rt.Fatalf("create of %q admitted beyond MaxKeys=%d", key, maxKeys)
					}
				default:
					if err != nil {
						rt.Fatalf("create of %q under quota failed: %v", key, err)
					}
					live[key] = true
				}
			},
			"delete": func(rt *rapid.T) {
				key := rapid.SampledFrom(keys).Draw(rt, "key")
				if err := kv.Delete(ctx, rsc, key, 0); err != nil {
					rt.Fatalf("unconditional delete failed: %v", err)
				}
				delete(live, key)
			},
		})
	})
}
