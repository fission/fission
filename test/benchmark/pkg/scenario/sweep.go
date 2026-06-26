// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// concurrencySweep drives a pre-warmed function at a range of concurrency levels
// to map the latency/throughput curve and find the knee.
type concurrencySweep struct {
	levels   []int
	duration time.Duration
	warmup   time.Duration
	poolsize int
}

func (c *concurrencySweep) Name() string   { return "concurrency-sweep" }
func (c *concurrencySweep) Tags() []string { return []string{"latency", "sweep", "throughput"} }

func (c *concurrencySweep) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	route, err := provisionWarmFunction(ctx, sc, c.poolsize, slices.Max(c.levels)+10, nil)
	if err != nil {
		return res, err
	}
	for _, level := range c.levels {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		r := loadgen.RunClosedLoop(ctx, loadgen.ClosedLoopConfig{
			Doer:        env.PublicTarget(route, level, true).Do,
			Concurrency: level,
			WarmUp:      c.warmup,
			Duration:    c.duration,
		})
		latencyMetrics(&res, fmt.Sprintf("c%d_", level), r)
	}
	return res, nil
}

// rpsSweep drives a pre-warmed function with the open-loop (constant-rate)
// driver at a range of target request rates, exposing the throughput knee and
// coordinated-omission-resistant tail latency.
type rpsSweep struct {
	levels   []int
	duration time.Duration
	warmup   time.Duration
	poolsize int
}

func (r *rpsSweep) Name() string   { return "rps-sweep" }
func (r *rpsSweep) Tags() []string { return []string{"latency", "sweep", "throughput", "openloop"} }

func (r *rpsSweep) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	route, err := provisionWarmFunction(ctx, sc, r.poolsize, slices.Max(r.levels)+10, nil)
	if err != nil {
		return res, err
	}
	for _, rps := range r.levels {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		out := loadgen.RunOpenLoop(ctx, loadgen.OpenLoopConfig{
			Doer:     env.PublicTarget(route, rps, true).Do,
			RPS:      rps,
			WarmUp:   r.warmup,
			Duration: r.duration,
		})
		latencyMetrics(&res, fmt.Sprintf("rps%d_", rps), out)
	}
	return res, nil
}

// payloadSweep drives a pre-warmed function with a range of request body sizes
// to expose the router's request-copy overhead.
type payloadSweep struct {
	sizes       []int
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	poolsize    int
}

func (p *payloadSweep) Name() string   { return "payload-sweep" }
func (p *payloadSweep) Tags() []string { return []string{"latency", "sweep"} }

func (p *payloadSweep) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	route, err := provisionWarmFunction(ctx, sc, p.poolsize, p.concurrency+10, []string{http.MethodGet, http.MethodPost})
	if err != nil {
		return res, err
	}
	res.SetMeta("concurrency", strconv.Itoa(p.concurrency))
	for _, size := range p.sizes {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		body := make([]byte, size)
		r := loadgen.RunClosedLoop(ctx, loadgen.ClosedLoopConfig{
			Doer:        env.PublicTargetFull(route, http.MethodPost, body, p.concurrency, true).Do,
			Concurrency: p.concurrency,
			WarmUp:      p.warmup,
			Duration:    p.duration,
		})
		latencyMetrics(&res, sizeLabel(size)+"_", r)
	}
	return res, nil
}
