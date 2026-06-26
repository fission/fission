// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package loadgen is a pure-Go HTTP load generator with HDR-histogram
// percentiles. It has no Fission dependencies so it can be unit-tested against
// an httptest.Server and reused outside this module.
package loadgen

import (
	"context"
	"time"
)

// Doer performs a single request and returns the number of response bytes read
// and an error (non-nil counts as a failed request). Drivers are written
// against this interface so the HTTP/signing details live elsewhere and the
// drivers stay testable with a fake Doer.
type Doer func(ctx context.Context) (bytes int64, err error)

// Sample is one recorded request outcome.
type Sample struct {
	Latency time.Duration
	Err     bool
	Bytes   int64
}

// Result is the aggregated outcome of a single load run, computed over the
// measured window (warm-up samples excluded).
type Result struct {
	// Counts over the measured window.
	Total  int64 `json:"total"`
	Errors int64 `json:"errors"`
	Bytes  int64 `json:"bytes"`

	// Window is the wall-clock duration of the measured (post-warm-up) phase.
	Window time.Duration `json:"windowNs"`

	// Throughput and error fraction.
	RPS       float64 `json:"rps"`
	ErrorRate float64 `json:"errorRate"`

	// Latency distribution of successful requests.
	Min    time.Duration `json:"minNs"`
	Mean   time.Duration `json:"meanNs"`
	P50    time.Duration `json:"p50Ns"`
	P90    time.Duration `json:"p90Ns"`
	P95    time.Duration `json:"p95Ns"`
	P99    time.Duration `json:"p99Ns"`
	P999   time.Duration `json:"p999Ns"`
	Max    time.Duration `json:"maxNs"`
	StdDev time.Duration `json:"stddevNs"`
}
