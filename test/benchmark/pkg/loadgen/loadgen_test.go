// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedLatencyDoer returns a Doer that sleeps for d and then succeeds, counting
// every invocation.
func fixedLatencyDoer(d time.Duration, calls *atomic.Int64) Doer {
	return func(ctx context.Context) (int64, error) {
		if calls != nil {
			calls.Add(1)
		}
		select {
		case <-time.After(d):
			return 16, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func TestClosedLoopMeasuresLatency(t *testing.T) {
	t.Parallel()
	const sleep = 20 * time.Millisecond
	res := RunClosedLoop(t.Context(), ClosedLoopConfig{
		Doer:        fixedLatencyDoer(sleep, nil),
		Concurrency: 4,
		WarmUp:      40 * time.Millisecond,
		Duration:    300 * time.Millisecond,
	})

	require.Positive(t, res.Total, "expected recorded requests")
	assert.Zero(t, res.Errors)
	assert.Zero(t, res.ErrorRate)
	assert.Positive(t, res.RPS)
	// p50 should sit near the fixed server latency; allow a wide band so the
	// test survives parallel CPU contention.
	assert.Greater(t, res.P50, 15*time.Millisecond)
	assert.Less(t, res.P50, 150*time.Millisecond)
}

func TestClosedLoopCountsErrors(t *testing.T) {
	t.Parallel()
	failing := func(ctx context.Context) (int64, error) {
		time.Sleep(time.Millisecond)
		return 0, errors.New("boom")
	}
	res := RunClosedLoop(t.Context(), ClosedLoopConfig{
		Doer:        failing,
		Concurrency: 2,
		Duration:    100 * time.Millisecond,
	})
	require.Positive(t, res.Total)
	assert.Equal(t, res.Total, res.Errors, "every request should be an error")
	assert.Equal(t, float64(1), res.ErrorRate)
	// Throughput counts successes only, so an all-error run reports 0.
	assert.Zero(t, res.RPS)
}

func TestWarmUpSamplesDiscarded(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	res := RunClosedLoop(t.Context(), ClosedLoopConfig{
		Doer:        fixedLatencyDoer(10*time.Millisecond, &calls),
		Concurrency: 1,
		WarmUp:      150 * time.Millisecond,
		Duration:    150 * time.Millisecond,
	})
	// Roughly half the window is warm-up, so total invocations must exceed the
	// number actually recorded.
	assert.Greater(t, calls.Load(), res.Total, "warm-up samples should be discarded")
}

func TestOpenLoopApproxRate(t *testing.T) {
	t.Parallel()
	const rps = 200
	res := RunOpenLoop(t.Context(), OpenLoopConfig{
		Doer:     fixedLatencyDoer(2*time.Millisecond, nil),
		RPS:      rps,
		WarmUp:   100 * time.Millisecond,
		Duration: 500 * time.Millisecond,
	})
	assert.Zero(t, res.Errors)
	// Achieved rate should be in the ballpark of the target; CI schedulers are
	// noisy, so accept a generous band.
	assert.InDelta(t, rps, res.RPS, rps*0.5, "achieved RPS far from target")
}

func TestContextCancelStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already cancelled
	res := RunClosedLoop(ctx, ClosedLoopConfig{
		Doer:        fixedLatencyDoer(10*time.Millisecond, nil),
		Concurrency: 2,
		Duration:    5 * time.Second,
	})
	assert.Zero(t, res.Total, "cancelled ctx should record no work")
}

func TestHTTPTargetSuccessAndError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		wantError bool
		wantBytes bool
	}{
		{
			name:      "2xx",
			handler:   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) },
			wantError: false,
			wantBytes: true,
		},
		{
			name:      "5xx counts as error",
			handler:   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			res := RunClosedLoop(t.Context(), ClosedLoopConfig{
				Doer:        NewHTTPTarget(HTTPTargetConfig{URL: srv.URL, Concurrency: 4, KeepAlive: true}).Do,
				Concurrency: 4,
				Duration:    150 * time.Millisecond,
			})
			require.Positive(t, res.Total)
			if tc.wantError {
				assert.Equal(t, float64(1), res.ErrorRate)
			} else {
				assert.Zero(t, res.Errors)
			}
			if tc.wantBytes {
				assert.Positive(t, res.Bytes, "response bytes should be counted")
			}
		})
	}
}

// TestHTTPTargetObserve pins the per-request observation contract the upgrade
// scoring depends on: a transport-level failure (no response at all) reports
// status 0, while an HTTP response reports its status and the captured header —
// so "no Ready endpoints" (connection refused, no attribution header) is
// distinguishable from a platform-attributed error response.
func TestHTTPTargetObserve(t *testing.T) {
	t.Parallel()

	type obs struct {
		status     int
		headerVal  string
		err        bool
		hasLatency bool
	}
	var got []obs
	var mu sync.Mutex
	observe := func(o Observation) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, obs{status: o.Status, headerVal: o.HeaderVal, err: o.Err != nil, hasLatency: o.Latency > 0})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.Header().Set("X-Attribution", "router")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	mk := func(url string) *HTTPTarget {
		return NewHTTPTarget(HTTPTargetConfig{
			URL:           url,
			ObserveHeader: "X-Attribution",
			Observe:       observe,
			Timeout:       2 * time.Second,
		})
	}

	_, err := mk(srv.URL + "/ok").Do(t.Context())
	require.NoError(t, err)
	_, err = mk(srv.URL + "/fail").Do(t.Context())
	require.Error(t, err)

	// A server that no longer exists: transport error, no response.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	_, err = mk(deadURL).Do(t.Context())
	require.Error(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 3)
	assert.Equal(t, obs{status: http.StatusOK, headerVal: "", err: false, hasLatency: true}, got[0])
	assert.Equal(t, obs{status: http.StatusBadGateway, headerVal: "router", err: true, hasLatency: true}, got[1])
	assert.Equal(t, obs{status: 0, headerVal: "", err: true, hasLatency: true}, got[2],
		"a request that produced no response must observe status 0 — and still carry a latency, "+
			"because a 30s client timeout reads very differently from a fast connection refusal")
}
