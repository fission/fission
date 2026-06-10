// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package dispatch provides the executor's specialization dispatcher: per-key
// deduplication with context-aware waiters, plus an optional global bound on
// concurrent work. It replaces the request-channel multiplexer whose waiters
// rode a sync.WaitGroup and could not honor context cancellation.
package dispatch

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
)

type (
	// Dispatcher runs "create a function service" work with two disciplines:
	// Do deduplicates concurrent calls per key (one creation runs; the rest
	// wait for its result or their own context, whichever ends first), and
	// DoEach runs every call independently. Both honor the optional global
	// concurrency bound.
	Dispatcher[T any] struct {
		logger logr.Logger
		// sem bounds concurrently running create funcs; nil means unbounded
		// (the historical behavior, kept as the default).
		sem chan struct{}

		mu       sync.Mutex
		inflight map[string]*call[T]
	}

	call[T any] struct {
		done chan struct{} // closed when val/err are final
		val  T
		err  error
	}
)

// New returns a Dispatcher bounding concurrent create funcs to maxConcurrent;
// 0 (or negative) means unbounded.
func New[T any](logger logr.Logger, maxConcurrent int) *Dispatcher[T] {
	d := &Dispatcher[T]{
		logger:   logger.WithName("dispatch"),
		inflight: make(map[string]*call[T]),
	}
	if maxConcurrent > 0 {
		d.sem = make(chan struct{}, maxConcurrent)
	}
	return d
}

// acquire takes a concurrency slot, honoring ctx. A nil semaphore (unbounded)
// always succeeds.
func (d *Dispatcher[T]) acquire(ctx context.Context) error {
	if d.sem == nil {
		return nil
	}
	select {
	case d.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher[T]) release() {
	if d.sem != nil {
		<-d.sem
	}
}

// DoEach runs create without deduplication (every caller gets its own run),
// subject only to the concurrency bound. Used for poolmgr, where concurrent
// cache-miss requests each specialize their own pod by design.
func (d *Dispatcher[T]) DoEach(ctx context.Context, create func(context.Context) (T, error)) (T, error) {
	if err := d.acquire(ctx); err != nil {
		var zero T
		return zero, err
	}
	defer d.release()
	return create(ctx)
}

// Do runs create at most once per key among concurrent callers. The first
// caller (the creator) runs create with its own context; everyone else waits
// for the shared result — or returns early with its own context's error,
// leaving the creation running for the remaining waiters. After the call
// completes the key is forgotten, so a later call starts fresh.
func (d *Dispatcher[T]) Do(ctx context.Context, key string, create func(context.Context) (T, error)) (T, error) {
	d.mu.Lock()
	if c, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		var zero T
		select {
		case <-c.done:
			return c.val, c.err
		case <-ctx.Done():
			d.logger.V(1).Info("waiter canceled while concurrent request for the same function runs", "key", key)
			return zero, ctx.Err()
		}
	}
	c := &call[T]{done: make(chan struct{})}
	d.inflight[key] = c
	d.mu.Unlock()

	// Completion runs in a defer so a panicking create (recovered upstream by
	// net/http's per-connection handler) cannot wedge the key forever with the
	// done channel never closing — waiters get an error and the next call
	// starts fresh; the panic is then re-raised to preserve crash visibility.
	defer func() {
		r := recover()
		if r != nil {
			c.err = fmt.Errorf("panic during function service creation for %q: %v", key, r)
		}
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
		close(c.done)
		if r != nil {
			panic(r)
		}
	}()

	c.val, c.err = d.DoEach(ctx, create)
	return c.val, c.err
}
