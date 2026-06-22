// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ferror "github.com/fission/fission/pkg/error"
)

func TestCache(t *testing.T) {
	c := MakeCache[string, string](100*time.Millisecond, 100*time.Millisecond)

	_, err := c.Set("a", "b")
	require.NoError(t, err)
	_, err = c.Set("p", "q")
	require.NoError(t, err)

	val, err := c.Get("a")
	require.NoError(t, err)
	require.Equal(t, val, "b")

	cc := c.Copy()
	require.Len(t, cc, 2)

	c.Delete("a")

	_, err = c.Get("a")
	require.Error(t, err)

	_, err = c.Set("expires", "42")
	require.NoError(t, err)
	time.Sleep(150 * time.Millisecond)
	_, err = c.Get("expires")
	require.Error(t, err)
}

func TestCacheSetExisting(t *testing.T) {
	c := MakeCache[string, string](0, 0)
	_, err := c.Set("key", "val1")
	require.NoError(t, err)

	_, err = c.Set("key", "val2")
	require.Error(t, err)
	var err2 ferror.Error
	require.ErrorAs(t, err, &err2)
	require.Equal(t, err2.Description(), "Resource exists")

	val, err := c.Get("key")
	require.NoError(t, err)
	require.Equal(t, val, "val1")
}

// This test will fail to compile until Upsert is implemented
func TestCacheUpsert(t *testing.T) {
	c := MakeCache[string, string](0, 0)
	_, err := c.Set("key", "val1")
	require.NoError(t, err)

	c.Upsert("key", "val2")
	new, err := c.Get("key")
	require.NoError(t, err)
	require.Equal(t, new, "val2")
}

// TestCacheConcurrentAccess hammers the cache from many goroutines so the race
// detector validates that all exported operations are safe under contention.
// It passes on both the channel-actor and the mutex implementations.
func TestCacheConcurrentAccess(t *testing.T) {
	c := MakeCache[string, int](0, 0)
	const keys = 8
	const workers = 32
	const iters = 200

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iters {
				k := fmt.Sprintf("k%d", (w+i)%keys)
				switch i % 5 {
				case 0:
					_, _ = c.Set(k, i)
				case 1:
					_, _ = c.Get(k)
				case 2:
					c.Upsert(k, i)
				case 3:
					c.Delete(k)
				case 4:
					_ = c.Copy()
				}
			}
		}(w)
	}
	wg.Wait()
}

func TestCacheExpiryService(t *testing.T) {
	// expiry 200ms. interval should be 200ms (clamped to 100ms min).
	c := MakeCache[string, string](200*time.Millisecond, 0)
	_, err := c.Set("key", "val")
	require.NoError(t, err)

	// Sleep enough for expiry service to run.
	// Interval is 200ms. Sleep 500ms.
	time.Sleep(500 * time.Millisecond)

	// Use Copy to check if item is still there without triggering lazy expiry
	m := c.Copy()
	require.Len(t, m, 0)
}
