// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package streaming

import (
	"sync"
	"time"
)

// Watchdog cancels (via the onIdle callback) when no Reset is observed within
// the idle window. Used by the streaming proxy to enforce an idle timeout that
// is re-armed on each chunk read, without an un-extendable context deadline.
// Safe for concurrent Reset/Stop. A non-positive idle window makes every method
// a no-op (the watchdog never fires).
type Watchdog struct {
	idle   time.Duration
	onIdle func()
	mu     sync.Mutex
	timer  *time.Timer
	dead   bool
}

// NewWatchdog returns a watchdog that calls onIdle once if Reset is not called
// within idle of the last Start/Reset.
func NewWatchdog(idle time.Duration, onIdle func()) *Watchdog {
	return &Watchdog{idle: idle, onIdle: onIdle}
}

// Start arms the watchdog. No-op if idle <= 0 or already started/stopped.
func (w *Watchdog) Start() {
	if w.idle <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(w.idle, w.fire)
}

// Reset re-arms the idle window. Call on each chunk from the upstream.
func (w *Watchdog) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead || w.timer == nil {
		return
	}
	w.timer.Reset(w.idle)
}

// Stop disarms permanently (idempotent). Call from the body Close / handler defer.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dead = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *Watchdog) fire() {
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return
	}
	w.dead = true
	w.mu.Unlock()
	if w.onIdle != nil {
		w.onIdle()
	}
}
