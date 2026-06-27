// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

// TestRunDetached covers the helper that runs the best-effort post-specialization
// writes off the cold-start path: it must (a) hand fn a context that survives the
// caller's cancellation but still carries a deadline, and (b) contain a panic so
// a faulty write can never crash the executor.
func TestRunDetached(t *testing.T) {
	t.Parallel()
	gp := &GenericPool{logger: logr.Discard()}

	t.Run("detaches cancellation but keeps a deadline", func(t *testing.T) {
		t.Parallel()
		// A parent that is already cancelled — mirrors the RPC ctx after getFuncSvc
		// returns. runDetached must not propagate that cancellation to fn.
		parent, cancel := context.WithCancel(context.Background())
		cancel()

		// Capture the context state INSIDE fn: runDetached cancels dctx via a
		// deferred cancel the moment fn returns, so reading it after the fact would
		// race that cancel.
		type state struct {
			err         error
			hasDeadline bool
		}
		got := make(chan state, 1)
		gp.runDetached(parent, "test-op", func(dctx context.Context) {
			_, hasDeadline := dctx.Deadline()
			got <- state{err: dctx.Err(), hasDeadline: hasDeadline}
		})

		select {
		case s := <-got:
			require.NoError(t, s.err, "detached context must not inherit the parent's cancellation")
			require.True(t, s.hasDeadline, "detached context must carry a bounding deadline")
		case <-time.After(2 * time.Second):
			require.FailNow(t, "runDetached never invoked fn")
		}
	})

	t.Run("recovers a panic in fn", func(t *testing.T) {
		t.Parallel()
		ran := make(chan struct{})
		// If runDetached did not recover, this panic would crash the whole test
		// binary; reaching the assertion below proves it was contained.
		gp.runDetached(context.Background(), "panic-op", func(_ context.Context) {
			defer close(ran)
			panic("boom")
		})
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "runDetached never invoked fn")
		}
		// Let the recover defer unwind; a broken recover would have aborted the
		// process before this returns.
		time.Sleep(50 * time.Millisecond)
	})
}
