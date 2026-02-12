package throttler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestThrottler_RunOnce_Sequential(t *testing.T) {
	throttler := MakeThrottler(1 * time.Second)
	var executed bool
	_, err := throttler.RunOnce("key1", func(first bool) (any, error) {
		executed = true
		return "result", nil
	})
	require.NoError(t, err)
	require.True(t, executed)
}

func TestThrottler_RunOnce_Concurrent(t *testing.T) {
	throttler := MakeThrottler(1 * time.Second)
	var wg sync.WaitGroup
	wg.Add(2)

	var g1First, g2First bool

	// G1
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (any, error) {
			g1First = first
			time.Sleep(100 * time.Millisecond)
			return nil, nil
		})
		require.NoError(t, err)
	}()

	// Ensure G1 starts first
	time.Sleep(10 * time.Millisecond)

	// G2
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (any, error) {
			g2First = first
			return nil, nil
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
		_, err := throttler.RunOnce("key", func(first bool) (any, error) {
			g1First = first
			time.Sleep(100 * time.Millisecond)
			return nil, nil
		})
		require.NoError(t, err)
	}()

	time.Sleep(60 * time.Millisecond) // Wait for expiry

	// G2 should see it as expired and take over
	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key", func(first bool) (any, error) {
			g2First = first
			return nil, nil
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
		_, err := throttler.RunOnce("key1", func(first bool) (any, error) {
			time.Sleep(100 * time.Millisecond)
			return nil, nil
		})
		require.NoError(t, err)
	}()

	go func() {
		defer wg.Done()
		_, err := throttler.RunOnce("key2", func(first bool) (any, error) {
			time.Sleep(100 * time.Millisecond)
			return nil, nil
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

	var firstCount int32

	for range count {
		go func() {
			defer wg.Done()
			_, err := throttler.RunOnce("key", func(first bool) (any, error) {
				if first {
					atomic.AddInt32(&firstCount, 1)
					time.Sleep(10 * time.Millisecond)
				}
				return nil, nil
			})
			require.NoError(t, err)
		}()
	}

	wg.Wait()
	// We can't assert exact count because of timing, but it shouldn't be 100
	// and it shouldn't be 0.
	fc := atomic.LoadInt32(&firstCount)
	require.True(t, fc > 0)
	require.True(t, fc < int32(count))
}
