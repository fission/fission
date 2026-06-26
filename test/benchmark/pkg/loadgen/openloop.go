// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"context"
	"sync"
	"time"
)

// OpenLoopConfig configures a constant-rate (open-loop) run: requests are issued
// at a fixed target RPS regardless of how many are in flight, so a slowing
// server shows up as growing latency/in-flight rather than a throttled send
// rate. This is the coordinated-omission-resistant driver.
type OpenLoopConfig struct {
	Doer        Doer
	RPS         int           // target requests per second (>=1)
	Duration    time.Duration // measured window after warm-up
	WarmUp      time.Duration // initial window whose samples are discarded
	MaxInflight int           // safety cap on concurrent requests; 0 -> default
}

const defaultMaxInflight = 10000

// RunOpenLoop issues requests at cfg.RPS for WarmUp+Duration and returns the
// aggregate over the measured window. When the in-flight cap is hit, the
// would-be request is recorded as an error (saturation) rather than dropped
// silently. It stops early if ctx is cancelled.
func RunOpenLoop(ctx context.Context, cfg OpenLoopConfig) Result {
	if cfg.RPS < 1 {
		cfg.RPS = 1
	}
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = defaultMaxInflight
	}
	interval := time.Second / time.Duration(cfg.RPS)
	start := time.Now()
	warmEnd := start.Add(cfg.WarmUp)
	deadline := warmEnd.Add(cfg.Duration)

	// One collector goroutine owns the recorder; workers funnel samples to it,
	// which keeps the non-thread-safe histogram single-writer.
	samples := make(chan Sample, cfg.RPS+1)
	rec := newRecorder()
	collectorDone := make(chan struct{})
	go func() {
		for s := range samples {
			rec.record(s)
		}
		close(collectorDone)
	}()

	sem := make(chan struct{}, cfg.MaxInflight)
	var wg sync.WaitGroup
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case now := <-ticker.C:
			if !now.Before(deadline) {
				break loop
			}
			record := now.After(warmEnd)
			select {
			case sem <- struct{}{}:
			default:
				// In-flight cap reached: count as a saturation error.
				if record {
					samples <- Sample{Err: true}
				}
				continue
			}
			wg.Add(1)
			// Measure latency from the scheduled tick (`now`), not from when the
			// goroutine starts: including the send-side scheduling/queueing delay
			// is what makes this driver coordinated-omission resistant.
			scheduled := now
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				b, err := cfg.Doer(ctx)
				if record {
					samples <- Sample{Latency: time.Since(scheduled), Err: err != nil, Bytes: b}
				}
			}()
		}
	}

	wg.Wait()
	close(samples)
	<-collectorDone
	return rec.result(cfg.Duration)
}
