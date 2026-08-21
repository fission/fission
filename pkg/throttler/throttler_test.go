// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package throttler

import (
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestThrottler_RunOnce_Sequential(t *testing.T) {
	throttler := MakeThrottler(1 * time.Second)
	var executed bool
	got, err := throttler.RunOnce("key1", func(first bool) (string, error) {
		executed = true
		return "result", nil
	})
	require.NoError(t, err)
	require.True(t, executed)
	require.Equal(t, "result", got)
}

func TestThrottler_RunOnce_Concurrent(t *testing.T) {
	throttler := MakeThrottler(1 * time.Second)
	var wg sync.WaitGroup
	wg.Add(2)

	var g1First, g2First bool

	// G1
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (struct{}, error) {
			g1First = first
			time.Sleep(100 * time.Millisecond)
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	// Ensure G1 starts first
	time.Sleep(10 * time.Millisecond)

	// G2
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (struct{}, error) {
			g2First = first
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	wg.Wait()

	require.True(t, g1First, "G1 should be first")
	require.False(t, g2First, "G2 should not be first")
}

func TestThrottler_RunOnce_Expiry(t *testing.T) {
	// Short TTL
	throttler := MakeThrottler(50 * time.Millisecond)
	var wg sync.WaitGroup
	wg.Add(2)

	var g1First, g2First bool

	// G1 takes longer than TTL
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (struct{}, error) {
			g1First = first
			time.Sleep(100 * time.Millisecond)
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	time.Sleep(60 * time.Millisecond) // Wait for expiry

	// G2 should see it as expired and take over
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (struct{}, error) {
			g2First = first
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	wg.Wait()

	require.True(t, g1First, "G1 should be first")
	require.True(t, g2First, "G2 should also be first because G1 expired")
}

func TestThrottler_RunOnce_DifferentKeys(t *testing.T) {
	throttler := MakeThrottler(1 * time.Second)
	var wg sync.WaitGroup
	wg.Add(2)

	start := time.Now()

	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key1", func(first bool) (struct{}, error) {
			time.Sleep(100 * time.Millisecond)
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key2", func(first bool) (struct{}, error) {
			time.Sleep(100 * time.Millisecond)
			return struct{}{}, nil
		})
		require.NoError(t, err)
	}()

	wg.Wait()
	duration := time.Since(start)

	// Should run in parallel, so duration should be around 100ms, not 200ms
	require.True(t, duration < 180*time.Millisecond, "Different keys should run in parallel")
}

func TestThrottler_Stress(t *testing.T) {
	throttler := MakeThrottler(100 * time.Millisecond)
	var wg sync.WaitGroup
	count := 100
	wg.Add(count)

	var firstCount atomic.Int32

	for range count {
		go func() {
			defer wg.Done()
			_, err := throttler.RunOnce("key", func(first bool) (struct{}, error) {
				if first {
					firstCount.Add(1)
					time.Sleep(10 * time.Millisecond)
				}
				return struct{}{}, nil
			})
			require.NoError(t, err)
		}()
	}

	wg.Wait()
	// We can't assert exact count because of timing, but it shouldn't be 100
	// and it shouldn't be 0.
	fc := firstCount.Load()
	require.True(t, fc > 0)
	require.True(t, fc < int32(count))
}

// TestThrottler_RunOnce_FollowerTimeout pins the follower path: a caller that
// arrives while the leader's entry is live and outwaits the TTL gets the zero
// value and an error, and never runs its callback. Runs in a synctest bubble
// (no wall-clock sleeps), so the Throttler is built directly: MakeThrottler
// would start expiryService on a minute ticker, a durably-blocked goroutine
// that the bubble refuses to leave behind.
func TestThrottler_RunOnce_FollowerTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		th := &Throttler{locks: make(map[string]*entry), ttl: time.Second}

		leaderStarted := make(chan struct{})
		leaderRelease := make(chan struct{})
		go func() {
			_, _ = th.RunOnce("key", func(first bool) (int, error) {
				close(leaderStarted)
				<-leaderRelease
				return 1, nil
			})
		}()
		<-leaderStarted

		// The entry is younger than the TTL, so this caller is a follower; the
		// leader never releases within the TTL, so the follower must time out.
		followerRan := false
		got, err := th.RunOnce("key", func(first bool) (int, error) {
			followerRan = true
			return 2, nil
		})
		require.Error(t, err)
		require.Zero(t, got)
		require.False(t, followerRan, "a timed-out follower must not run its callback")

		close(leaderRelease)
		synctest.Wait()
	})
}
