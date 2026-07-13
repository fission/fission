// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestoretest

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anishathalye/porcupine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// linScope is the scope the linearizability workload runs in.
var linScope = statestore.Scope{Namespace: "ns", Owner: "function/lin", Keyspace: "ks"}

// regState models a versioned compare-and-swap register: an absent key is
// version 0, and each successful write increments the version.
type regState struct {
	ver int64
	val int
}

type regInput struct {
	op       string // "get" | "cas"
	expected int64  // cas: the version the client compared against
	newVal   int    // cas: the value written
}

type regOutput struct {
	ver int64 // get: observed version
	val int   // get: observed value
	ok  bool  // cas: whether the write applied
}

// registerModel is the linearizability specification the recorded history is
// checked against: reads observe the current (version, value); a CAS applies iff
// its expected version equals the current version, incrementing the version.
var registerModel = porcupine.Model{
	Init: func() any { return regState{ver: 1, val: 0} }, // seeded before the workload
	Step: func(st, in, out any) (bool, any) {
		s := st.(regState)
		i := in.(regInput)
		o := out.(regOutput)
		switch i.op {
		case "get":
			return o.ver == s.ver && o.val == s.val, s
		case "cas":
			if i.expected == s.ver {
				if !o.ok {
					return false, s // must have applied
				}
				return true, regState{ver: s.ver + 1, val: i.newVal}
			}
			return !o.ok, s // stale expected version must conflict
		default:
			return false, s
		}
	},
	Equal: func(a, b any) bool { return a.(regState) == b.(regState) },
}

// RunKVLinearizability drives concurrent read-modify-write CAS traffic against a
// single key and checks the recorded history for linearizability with porcupine
// (invariant K1). Phase 2 reuses it against real Postgres.
func RunKVLinearizability(t *testing.T, newCaps Factory) {
	t.Helper()
	kv, err := newCaps(t).KV()
	if err != nil {
		t.Skipf("KV capability unavailable: %v", err)
	}
	ctx := t.Context()
	const key = "reg"

	// Seed the register at version 1, value 0.
	require.NoError(t, kv.Set(ctx, linScope, key, []byte("0"), statestore.SetOptions{IfVersion: new(int64(0))}))

	var clock atomic.Int64
	tick := func() int64 { return clock.Add(1) }

	var mu sync.Mutex
	var ops []porcupine.Operation
	record := func(clientID int, in regInput, call int64, out regOutput, ret int64) {
		mu.Lock()
		ops = append(ops, porcupine.Operation{ClientId: clientID, Input: in, Call: call, Output: out, Return: ret})
		mu.Unlock()
	}

	const clients, iters = 6, 15
	var wg sync.WaitGroup
	for c := range clients {
		wg.Go(func() {
			for it := range iters {
				// Read.
				call := tick()
				v, gerr := kv.Get(ctx, linScope, key)
				ret := tick()
				// assert (not require) — these run in a worker goroutine, where
				// require's FailNow is unsafe; a failure still fails the test.
				assert.NoError(t, gerr) // the key always exists after seeding
				curVal, _ := strconv.Atoi(string(v.Data))
				record(c, regInput{op: "get"}, call, regOutput{ver: v.Version, val: curVal}, ret)

				// Compare-and-swap from the observed version to a unique value.
				newVal := c*1000 + it + 1
				expected := v.Version
				call = tick()
				serr := kv.Set(ctx, linScope, key, []byte(strconv.Itoa(newVal)), statestore.SetOptions{IfVersion: &expected})
				ret = tick()
				if serr != nil {
					assert.ErrorIs(t, serr, statestore.ErrVersionConflict)
				}
				record(c, regInput{op: "cas", expected: expected, newVal: newVal}, call, regOutput{ok: serr == nil}, ret)
			}
		})
	}
	wg.Wait()

	require.True(t, porcupine.CheckOperations(registerModel, ops), "KV CAS history is not linearizable (K1)")
}
