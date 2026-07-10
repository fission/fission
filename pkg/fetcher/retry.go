// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"context"
	"time"
)

// Retry policy for the specialization cold path: per-error-class sleep
// schedules for the Package fetch, and the bounded wait for the environment
// container's server to start accepting connections. Kept apart from the
// fetch mechanics in fetcher.go; the schedules are data so tests can pin the
// delay sequences and total-budget invariants.

// pkgNotFoundRetrySchedule is the sleep sequence for the apiserver
// create-then-get race (a Package created moments ago can still 404). The
// first hits are cheap (the race usually resolves within tens of ms) while
// the summed window (≥550ms) covers at least the previous schedule's 500ms.
var pkgNotFoundRetrySchedule = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond,
	80 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond,
	100 * time.Millisecond, 100 * time.Millisecond,
}

// pkgDialRetrySchedule is the sleep sequence for connection errors on the
// package Get. It exists for istio: envoy blocks all outbound requests during
// the pod's first seconds, so these waits stay deliberately coarse.
// For details, see https://github.com/istio/istio/issues/12187
var pkgDialRetrySchedule = []time.Duration{
	500 * time.Millisecond, 1000 * time.Millisecond,
	1500 * time.Millisecond, 2000 * time.Millisecond,
}

// pkgTransientRetrySchedule is the sleep sequence for every other Get error
// (apiserver timeouts, connection resets, 429/5xx). The pre-reshape loop
// retried any error up to 4 more times back-to-back; keep that resilience but
// space the attempts out instead of hot-looping.
var pkgTransientRetrySchedule = []time.Duration{
	50 * time.Millisecond, 100 * time.Millisecond,
	200 * time.Millisecond, 400 * time.Millisecond,
}

// envSpecializeWaitBudget bounds the total wall-clock time SpecializePod waits
// for the environment container's server to start accepting connections. It
// covers (with margin) the previous schedule's ~406s worst case, so slow-image
// environments that survived before still do.
const envSpecializeWaitBudget = 7 * time.Minute

// envSpecializeRetryDelay returns the sleep before conn-refused retry attempt
// i (0-based) of the env-specialize wait: 25ms doubling to a 500ms cap. Env
// servers typically bind tens of milliseconds after the fetcher's first POST;
// the previous 500*2i ms ramp made that common case cost a full second.
func envSpecializeRetryDelay(i int) time.Duration {
	const base, maxDelay = 25 * time.Millisecond, 500 * time.Millisecond
	if i >= 5 {
		return maxDelay // 25ms<<5 already exceeds the cap (and huge i would overflow the shift)
	}
	return base << i
}

// sleepCtx sleeps for d unless ctx ends first; it reports whether the full
// sleep elapsed (false means the caller should stop retrying).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
