// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"strconv"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// warmPath measures steady-state latency and throughput against a pre-warmed
// function at a fixed concurrency — the router/proxy hot path. The poolmgr
// variant keeps the historical "warm-path" name (trend-series continuity);
// other executors get a suffixed name so each data plane has its own
// steady-state number.
type warmPath struct {
	executor    fv1.ExecutorType
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	poolsize    int
}

func (w *warmPath) Name() string {
	if w.executor == fv1.ExecutorTypePoolmgr {
		return "warm-path"
	}
	return "warm-path-" + string(w.executor)
}

func (w *warmPath) Tags() []string {
	tags := []string{"latency", "warmpath", "throughput"}
	if w.executor == fv1.ExecutorTypePoolmgr {
		tags = append(tags, "smoke")
	}
	return tags
}

func (w *warmPath) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// requestsPerPod high so all concurrent requests serialize through one warm
	// pod, isolating router/proxy overhead from pod fan-out.
	route, _, err := provisionWarmFunction(ctx, sc, w.executor, runtimePython, w.poolsize, w.concurrency+10, nil)
	if err != nil {
		return res, err
	}
	res.SetMeta("executor", string(w.executor))
	res.SetMeta("concurrency", strconv.Itoa(w.concurrency))

	snapshotPprof(ctx, env, w.Name(), "before")
	start := time.Now()
	r := loadgen.RunClosedLoop(ctx, loadgen.ClosedLoopConfig{
		Doer:        env.PublicTarget(route, w.concurrency, true).Do,
		Concurrency: w.concurrency,
		WarmUp:      w.warmup,
		Duration:    w.duration,
	})
	end := time.Now()
	snapshotPprof(ctx, env, w.Name(), "after")

	latencyMetrics(&res, "", r)
	addServerMetrics(ctx, env, &res)
	dumpPromRange(ctx, env, w.Name(), "router_rss", routerRSSBytes, start, end)
	return res, nil
}
