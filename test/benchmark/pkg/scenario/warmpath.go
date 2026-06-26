// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"strconv"
	"time"

	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// warmPath measures steady-state latency and throughput against a pre-warmed
// function at a fixed concurrency — the router/proxy hot path.
type warmPath struct {
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	poolsize    int
}

func (w *warmPath) Name() string   { return "warm-path" }
func (w *warmPath) Tags() []string { return []string{"latency", "warmpath", "throughput", "smoke"} }

func (w *warmPath) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// requestsPerPod high so all concurrent requests serialize through one warm
	// pod, isolating router/proxy overhead from pod fan-out.
	route, err := provisionWarmFunction(ctx, sc, w.poolsize, w.concurrency+10, nil)
	if err != nil {
		return res, err
	}
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
