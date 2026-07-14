// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"net/http"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// PromQL for the RFC-0024 async signals (summed across router replicas).
const (
	asyncQueueDepthQuery = `sum(fission_async_queue_depth)`
	asyncDLQTotalQuery   = `sum(fission_async_dlq_total)`
)

// asyncInvoke measures the RFC-0024 asynchronous invocation path: the router
// accepts each request with a durable 202 (X-Fission-Invoke-Mode: async) and an
// in-router dispatcher drains the statestore queue out-of-band. It reports three
// distinct metric families so they never collide on the trend series:
//   - enqueue_*  : the 202-accept latency/throughput under closed-loop load
//   - drain_*    : how fast the dispatcher clears the backlog after load stops
//   - dlq_total  : dead-letters accrued during the run
//
// It skips cleanly when async invocation is not enabled on the cluster (a 501),
// and the drain/dlq families are best-effort — emitted only when Prometheus is
// wired. It is deliberately OFF the smoke subset (it needs a warm drain window),
// so it runs in the weekly/dispatch suite, not per-PR.
type asyncInvoke struct {
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	poolsize    int
}

func (a *asyncInvoke) Name() string   { return "async-invoke" }
func (a *asyncInvoke) Tags() []string { return []string{"async", "throughput", "queue"} }

func (a *asyncInvoke) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// Node (multi-request-per-pod) on a POST route — async invocation runs on the
	// public listener and the enqueue path is what we measure, not pod fan-out.
	route, fnName, err := provisionWarmFunction(ctx, sc, fv1.ExecutorTypePoolmgr, runtimeNode, a.poolsize, a.concurrency+10, []string{http.MethodPost})
	if err != nil {
		return res, err
	}
	res.SetMeta("function", fnName)

	asyncHeaders := http.Header{}
	asyncHeaders.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)

	// Probe once: a 501 means async invocation is not enabled — skip rather than
	// record a run of errors.
	switch code, perr := probeStatus(ctx, env.RouterURL()+route, asyncHeaders); {
	case perr != nil:
		return res, perr
	case code == http.StatusNotImplemented:
		return res, skip("async invocation not enabled (X-Fission-Invoke-Mode returns 501)")
	}

	// Baseline the DLQ counter so dlq_total reflects only this run.
	dlqBefore := asyncQueryFloat(ctx, env, asyncDLQTotalQuery)

	target := loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
		URL:         env.RouterURL() + route,
		Method:      http.MethodPost,
		Headers:     asyncHeaders,
		Concurrency: a.concurrency,
		KeepAlive:   true,
		Timeout:     60 * time.Second,
	})
	r := loadgen.RunClosedLoop(ctx, loadgen.ClosedLoopConfig{
		Doer:        target.Do,
		Concurrency: a.concurrency,
		WarmUp:      a.warmup,
		Duration:    a.duration,
	})
	// enqueue_* is the durable-accept latency: the 202 is returned once the message
	// is persisted, so this is the caller-visible async overhead.
	latencyMetrics(&res, "enqueue_", r)

	// drain_* : after the load stops, time the backlog to zero and derive the
	// dispatcher's drain throughput. Best-effort — needs the queue-depth gauge.
	if env.Capturer.PrometheusEnabled() {
		accepted := r.Total - r.Errors
		if secs, ok := asyncDrainSeconds(ctx, env, 3*time.Minute); ok {
			res.Add("drain_seconds", "s", report.Lower, secs)
			if secs > 0 {
				res.Add("drain_throughput", "rps", report.Higher, float64(accepted)/secs)
			}
		}
		res.Add("dlq_total", "count", report.Lower, asyncQueryFloat(ctx, env, asyncDLQTotalQuery)-dlqBefore)
	}
	return res, nil
}

// probeStatus fires one request and returns its HTTP status, so a scenario can
// detect a disabled feature (501) before running a full load.
func probeStatus(ctx context.Context, url string, headers http.Header) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	req.Header = headers.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// asyncQueryFloat reads an instant PromQL scalar, returning 0 when Prometheus is
// disabled or the sample is absent (best-effort, never fails the scenario).
func asyncQueryFloat(ctx context.Context, env *harness.Env, query string) float64 {
	if !env.Capturer.PrometheusEnabled() {
		return 0
	}
	if v, found, err := env.Capturer.QueryInstant(ctx, query); err == nil && found {
		return v
	}
	return 0
}

// asyncDrainSeconds polls the async queue-depth gauge until it reaches zero,
// returning the elapsed time. It concludes "drained" ONLY on a successful read of a
// non-positive depth; a query error or an absent/not-yet-scraped sample is treated
// as "not yet drained" (keep polling), never as drained — so a transient Prometheus
// hiccup can't fabricate a ~0s drain. ok is false when the deadline elapses without
// a confirmed zero (a stuck dispatcher, or Prometheus never answering), so the
// caller records no misleading drain number.
func asyncDrainSeconds(ctx context.Context, env *harness.Env, timeout time.Duration) (float64, bool) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		if v, found, err := env.Capturer.QueryInstant(ctx, asyncQueueDepthQuery); err == nil && found && v <= 0 {
			return time.Since(start).Seconds(), true
		}
		if time.Now().After(deadline) {
			return 0, false
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-time.After(2 * time.Second):
		}
	}
}
