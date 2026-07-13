// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/fission/fission/pkg/statestore"
)

// K1 as a model-based property: a random sequence of KV writes against one key
// keeps the driver's observable (version, value) equal to a hand-written
// reference register — the executable spec validated against a simpler spec.
func TestMemoryKV_K1_CASLinearizable_Rapid(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		kv := newKV(t)
		ctx := t.Context()

		var version int64 // 0 == absent
		var value []byte
		exists := false

		steps := rapid.IntRange(1, 40).Draw(rt, "steps")
		for range steps {
			switch rapid.SampledFrom([]string{"setUncond", "createOnly", "cas", "casStale", "delete", "get"}).Draw(rt, "op") {
			case "setUncond":
				v := rapid.SliceOf(rapid.Byte()).Draw(rt, "v")
				require.NoError(t, kv.Set(ctx, testScope, "k", v, statestore.SetOptions{}))
				version++
				value, exists = v, true
			case "createOnly":
				v := rapid.SliceOf(rapid.Byte()).Draw(rt, "v")
				err := kv.Set(ctx, testScope, "k", v, statestore.SetOptions{IfVersion: new(int64(0))})
				if exists {
					require.ErrorIs(t, err, statestore.ErrVersionConflict)
				} else {
					require.NoError(t, err)
					version, value, exists = 1, v, true
				}
			case "cas":
				// IfVersion == the current version (0 when absent → create-only,
				// N when present → CAS-match), so this always succeeds.
				v := rapid.SliceOf(rapid.Byte()).Draw(rt, "v")
				require.NoError(t, kv.Set(ctx, testScope, "k", v, statestore.SetOptions{IfVersion: &version}))
				version++
				value, exists = v, true
			case "casStale":
				stale := version + rapid.Int64Range(1, 5).Draw(rt, "skew")
				err := kv.Set(ctx, testScope, "k", []byte("stale"), statestore.SetOptions{IfVersion: &stale})
				require.ErrorIs(t, err, statestore.ErrVersionConflict) // never matches
			case "delete":
				require.NoError(t, kv.Delete(ctx, testScope, "k", 0))
				version, value, exists = 0, nil, false
			case "get":
				got, err := kv.Get(ctx, testScope, "k")
				if exists {
					require.NoError(t, err)
					require.EqualValues(t, version, got.Version)
					require.Equal(t, value, got.Data)
				} else {
					require.ErrorIs(t, err, statestore.ErrNotFound)
				}
			}
		}
	})
}

// Q4/T1 as properties: across a random queue op sequence, attempts never exceed
// the budget and conservation drift stays zero after every op.
func TestMemoryQueue_Invariants_Rapid(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := newStore()
		s.maxAttempts = rapid.IntRange(1, 4).Draw(rt, "maxAttempts")
		q, err := s.Queue()
		require.NoError(t, err)
		ctx := t.Context()

		// receipts currently held (from the last lease batch)
		var held []statestore.LeasedMessage
		steps := rapid.IntRange(1, 50).Draw(rt, "steps")
		for range steps {
			switch rapid.SampledFrom([]string{"enqueue", "lease", "ack", "nack", "kill"}).Draw(rt, "op") {
			case "enqueue":
				_, err := q.Enqueue(ctx, "rq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
				require.NoError(t, err)
			case "lease":
				n := rapid.IntRange(1, 3).Draw(rt, "n")
				l, err := q.Lease(ctx, "rq", n, time.Minute)
				require.NoError(t, err)
				for _, m := range l {
					require.LessOrEqual(t, m.Attempts, s.maxAttempts) // Q4
				}
				held = l
			case "ack":
				if len(held) > 0 {
					_ = q.Ack(ctx, held[0].Receipt)
					held = held[1:]
				}
			case "nack":
				if len(held) > 0 {
					_ = q.Nack(ctx, held[0].Receipt, 0)
					held = held[1:]
				}
			case "kill":
				if len(held) > 0 {
					_ = q.Kill(ctx, held[0].Receipt, "x")
					held = held[1:]
				}
			}
			require.Zero(t, s.ConservationStats().Drift(), "T1: conservation drift must stay zero") // T1
		}
	})
}
