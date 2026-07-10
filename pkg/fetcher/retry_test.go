// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEnvSpecializeRetryDelay(t *testing.T) {
	t.Parallel()
	want := []time.Duration{
		25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
		200 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond,
		500 * time.Millisecond,
	}
	for i, w := range want {
		assert.Equalf(t, w, envSpecializeRetryDelay(i), "attempt %d", i)
	}
	// Large attempt indices must stay capped (no overflow back below the cap).
	assert.Equal(t, 500*time.Millisecond, envSpecializeRetryDelay(1000))
	// The wait budget must cover the previous schedule's worst case
	// (sum of 500*2i ms for i in [0,29) ≈ 406s) so no environment that
	// previously had time to start is now cut off.
	assert.GreaterOrEqual(t, envSpecializeWaitBudget, 406*time.Second)
}

func TestPkgRetrySchedules(t *testing.T) {
	t.Parallel()
	var notFoundTotal time.Duration
	for _, d := range pkgNotFoundRetrySchedule {
		notFoundTotal += d
	}
	// The summed not-found window guards the apiserver create-then-get race;
	// it must cover the previous schedule's 500ms (50+100+150+200).
	assert.GreaterOrEqual(t, notFoundTotal, 500*time.Millisecond)
	// First retry must stay cheap — that's the point of the reshape.
	assert.LessOrEqual(t, pkgNotFoundRetrySchedule[0], 25*time.Millisecond)

	var dialTotal time.Duration
	for _, d := range pkgDialRetrySchedule {
		dialTotal += d
	}
	// The dial schedule exists for istio/envoy warm-up; keep it coarse
	// (≥ the previous 5s total).
	assert.GreaterOrEqual(t, dialTotal, 5*time.Second)
}
