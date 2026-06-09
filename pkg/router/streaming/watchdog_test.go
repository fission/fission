// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package streaming

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchdogFiresOnIdle(t *testing.T) {
	t.Parallel()
	cause := errors.New("idle")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	w := NewWatchdog(40*time.Millisecond, func() { cancel(cause) })
	w.Start()
	defer w.Stop()
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), cause) {
			t.Fatalf("cause=%v", context.Cause(ctx))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never fired")
	}
}

func TestWatchdogResetPreventsFire(t *testing.T) {
	t.Parallel()
	fired := make(chan struct{}, 1)
	w := NewWatchdog(60*time.Millisecond, func() { fired <- struct{}{} })
	w.Start()
	defer w.Stop()
	for range 5 { // keep resetting under the threshold
		time.Sleep(20 * time.Millisecond)
		w.Reset()
	}
	select {
	case <-fired:
		t.Fatal("watchdog fired despite resets")
	default:
	}
}

// TestWatchdogStopPreventsFire ensures a stopped watchdog never fires.
func TestWatchdogStopPreventsFire(t *testing.T) {
	t.Parallel()
	fired := make(chan struct{}, 1)
	w := NewWatchdog(30*time.Millisecond, func() { fired <- struct{}{} })
	w.Start()
	w.Stop()
	select {
	case <-fired:
		t.Fatal("stopped watchdog fired")
	case <-time.After(120 * time.Millisecond):
	}
}

// TestWatchdogZeroIdleNoOp ensures idle<=0 never arms.
func TestWatchdogZeroIdleNoOp(t *testing.T) {
	t.Parallel()
	fired := make(chan struct{}, 1)
	w := NewWatchdog(0, func() { fired <- struct{}{} })
	w.Start()
	defer w.Stop()
	w.Reset() // must be safe even though not armed
	select {
	case <-fired:
		t.Fatal("zero-idle watchdog fired")
	case <-time.After(80 * time.Millisecond):
	}
}
