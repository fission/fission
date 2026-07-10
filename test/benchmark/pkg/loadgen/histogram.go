// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
)

// Latencies are recorded in microseconds: a 1µs..5min range at 3 significant
// figures covers everything from a same-node warm hit to a stuck cold start
// without losing meaningful precision.
const (
	minLatencyUS = int64(1)
	maxLatencyUS = int64(5 * time.Minute / time.Microsecond)
	sigFigs      = 3
)

// recorder accumulates samples into an HDR histogram plus plain counters.
// An HDR histogram is not safe for concurrent use, so each load goroutine owns
// a recorder and the driver merges them once the run finishes.
type recorder struct {
	hist   *hdr.Histogram
	total  int64
	errors int64
	bytes  int64
}

func newRecorder() *recorder {
	return &recorder{hist: hdr.New(minLatencyUS, maxLatencyUS, sigFigs)}
}

func (r *recorder) record(s Sample) {
	r.total++
	if s.Err {
		r.errors++
		return
	}
	r.bytes += s.Bytes
	us := min(max(s.Latency.Microseconds(), minLatencyUS), maxLatencyUS)
	_ = r.hist.RecordValue(us)
}

// merge folds another recorder's data into r.
func (r *recorder) merge(o *recorder) {
	r.hist.Merge(o.hist)
	r.total += o.total
	r.errors += o.errors
	r.bytes += o.bytes
}

// result finalizes the aggregate over the measured window.
func (r *recorder) result(window time.Duration) Result {
	res := Result{
		Total:  r.total,
		Errors: r.errors,
		Bytes:  r.bytes,
		Window: window,
	}
	// Throughput is successful requests per second: counting errors would let an
	// all-error run report a healthy-looking rate.
	if window > 0 {
		res.RPS = float64(r.total-r.errors) / window.Seconds()
	}
	if r.total > 0 {
		res.ErrorRate = float64(r.errors) / float64(r.total)
	}
	us := func(q float64) time.Duration {
		return time.Duration(r.hist.ValueAtQuantile(q)) * time.Microsecond
	}
	res.Min = time.Duration(r.hist.Min()) * time.Microsecond
	res.Mean = time.Duration(int64(r.hist.Mean())) * time.Microsecond
	res.P50 = us(50)
	res.P90 = us(90)
	res.P95 = us(95)
	res.P99 = us(99)
	res.P999 = us(99.9)
	res.Max = time.Duration(r.hist.Max()) * time.Microsecond
	res.StdDev = time.Duration(int64(r.hist.StdDev())) * time.Microsecond
	return res
}
