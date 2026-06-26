// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"context"
	"sync"
	"time"
)

// ClosedLoopConfig configures a fixed-concurrency (closed-loop) run: N workers,
// each looping request -> record -> repeat. This models "N virtual users" and
// measures latency under a fixed in-flight count.
type ClosedLoopConfig struct {
	Doer        Doer
	Concurrency int           // number of concurrent workers (>=1)
	Duration    time.Duration // measured window after warm-up
	WarmUp      time.Duration // initial window whose samples are discarded
}

// RunClosedLoop drives Concurrency workers for WarmUp+Duration and returns the
// aggregate over the measured window. It stops early if ctx is cancelled.
func RunClosedLoop(ctx context.Context, cfg ClosedLoopConfig) Result {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	start := time.Now()
	warmEnd := start.Add(cfg.WarmUp)
	deadline := warmEnd.Add(cfg.Duration)

	recorders := make([]*recorder, cfg.Concurrency)
	var wg sync.WaitGroup
	for i := range cfg.Concurrency {
		rec := newRecorder()
		recorders[i] = rec
		wg.Go(func() {
			for {
				if ctx.Err() != nil || !time.Now().Before(deadline) {
					return
				}
				t0 := time.Now()
				b, err := cfg.Doer(ctx)
				lat := time.Since(t0)
				if time.Now().After(warmEnd) {
					rec.record(Sample{Latency: lat, Err: err != nil, Bytes: b})
				}
			}
		})
	}
	wg.Wait()

	agg := newRecorder()
	for _, r := range recorders {
		agg.merge(r)
	}
	return agg.result(cfg.Duration)
}
