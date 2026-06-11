// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoDeduplicatesPerKey(t *testing.T) {
	t.Parallel()
	d := New[string](logr.Discard(), 0)

	var creations atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	const waiters = 10

	// The creator runs first (confirmed via started) so the in-flight entry
	// exists before any waiter calls Do.
	var wg sync.WaitGroup
	results := make([]string, waiters)
	errs := make([]error, waiters)
	wg.Go(func() {
		results[0], errs[0] = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
			creations.Add(1)
			close(started)
			<-release
			return "addr", nil
		})
	})
	<-started
	for i := 1; i < waiters; i++ {
		wg.Go(func() {
			results[i], errs[i] = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
				creations.Add(1)
				return "late-create", nil
			})
		})
	}
	// Give the waiters time to enter Do and join the in-flight call (between
	// Do entry and joining there is only a mutex, no blocking).
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int64(1), creations.Load(), "exactly one creation per key")
	for i := range waiters {
		require.NoError(t, errs[i])
		assert.Equal(t, "addr", results[i], "every waiter shares the creator's result")
	}

	// The key is forgotten after completion: a later call creates again.
	_, err := d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
		creations.Add(1)
		return "addr2", nil
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), creations.Load())
}

func TestDoErrorFansOutToAllWaiters(t *testing.T) {
	t.Parallel()
	d := New[string](logr.Discard(), 0)

	boom := errors.New("specialization failed")
	release := make(chan struct{})
	started := make(chan struct{})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	// Creator first (confirmed via started), then the waiters join.
	wg.Go(func() {
		_, errs[0] = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
			close(started)
			<-release
			return "", boom
		})
	})
	<-started
	for i := 1; i < len(errs); i++ {
		wg.Go(func() {
			_, errs[i] = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
				return "", boom
			})
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := range errs {
		assert.ErrorIs(t, errs[i], boom)
	}
}

func TestDoWaiterCancellationDoesNotAffectCreator(t *testing.T) {
	t.Parallel()
	d := New[string](logr.Discard(), 0)

	release := make(chan struct{})
	started := make(chan struct{})

	// Creator: blocks until release, then succeeds.
	creatorDone := make(chan struct{})
	var creatorVal string
	var creatorErr error
	go func() {
		defer close(creatorDone)
		creatorVal, creatorErr = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
			close(started)
			<-release
			return "addr", nil
		})
	}()
	<-started

	// Waiter with a canceled context returns immediately with ctx.Err...
	waiterCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := d.Do(waiterCtx, "fn-a", func(context.Context) (string, error) {
		t.Error("waiter must not create")
		return "", nil
	})
	require.ErrorIs(t, err, context.Canceled)

	// ...while the creator is unaffected and completes normally.
	close(release)
	<-creatorDone
	require.NoError(t, creatorErr)
	assert.Equal(t, "addr", creatorVal)
}

func TestConcurrencyBound(t *testing.T) {
	t.Parallel()
	const bound = 2
	d := New[int](logr.Discard(), bound)

	var running, maxRunning atomic.Int64
	release := make(chan struct{})

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			_, err := d.DoEach(t.Context(), func(context.Context) (int, error) {
				cur := running.Add(1)
				for {
					prev := maxRunning.Load()
					if cur <= prev || maxRunning.CompareAndSwap(prev, cur) {
						break
					}
				}
				<-release
				running.Add(-1)
				return i, nil
			})
			assert.NoError(t, err)
		})
	}
	// Give the pool time to admit as many as it ever will.
	require.Eventually(t, func() bool { return running.Load() == bound }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int64(bound), maxRunning.Load(), "no more than N create funcs run at once")
	close(release)
	wg.Wait()
	assert.LessOrEqual(t, maxRunning.Load(), int64(bound))
}

func TestBoundedQueuedCallerCancels(t *testing.T) {
	t.Parallel()
	d := New[int](logr.Discard(), 1)

	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = d.DoEach(t.Context(), func(context.Context) (int, error) {
			close(started)
			<-release
			return 0, nil
		})
	}()
	<-started

	// A queued caller whose context cancels leaves the queue with ctx.Err.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := d.DoEach(ctx, func(context.Context) (int, error) {
		t.Error("canceled caller must not run")
		return 0, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	close(release)
}

func TestUnboundedIsDefault(t *testing.T) {
	t.Parallel()
	d := New[int](logr.Discard(), 0)

	var running atomic.Int64
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, _ = d.DoEach(t.Context(), func(context.Context) (int, error) {
				running.Add(1)
				<-release
				return 0, nil
			})
		})
	}
	require.Eventually(t, func() bool { return running.Load() == 20 }, time.Second, time.Millisecond,
		"0 = unbounded: all calls run concurrently (historical behavior)")
	close(release)
	wg.Wait()
}

// TestDoPanicDoesNotWedgeKey locks the panic-safety contract: a panicking
// create must (a) hand waiters a real error, (b) re-panic so the crash stays
// visible, and (c) leave the key free so the next Do starts a fresh creation
// instead of waiting forever on a wedged in-flight entry.
func TestDoPanicDoesNotWedgeKey(t *testing.T) {
	t.Parallel()
	d := New[string](logr.Discard(), 0)

	started := make(chan struct{})
	finish := make(chan struct{})

	// Waiter joins the panicking creator's in-flight call.
	var waiterErr error
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		<-started
		_, waiterErr = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
			t.Error("waiter must join the in-flight call, not create")
			return "", nil
		})
	}()

	// Creator panics; the re-panic is confirmed by recovering it here.
	creatorDone := make(chan struct{})
	go func() {
		defer close(creatorDone)
		defer func() {
			r := recover()
			assert.NotNil(t, r, "Do must re-panic after completing the call")
		}()
		_, _ = d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
			close(started)
			<-finish
			panic("specialization blew up")
		})
	}()

	// Give the waiter time to join the in-flight call before releasing the
	// creator (same grace the other Do tests use: only a mutex sits between
	// Do entry and joining).
	<-started
	time.Sleep(100 * time.Millisecond)
	close(finish)
	<-creatorDone
	<-waiterDone
	require.Error(t, waiterErr, "waiters must receive an error, not a zero value")
	assert.Contains(t, waiterErr.Error(), "specialization blew up")

	// The key is free again: a fresh Do runs a new creation.
	got, err := d.Do(t.Context(), "fn-a", func(context.Context) (string, error) {
		return "recovered", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", got)
}
