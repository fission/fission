// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"strconv"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// provisionedIdle proves that RFC-0026 provisioned-concurrency "floor" pods
// stay genuinely warm after sitting idle, and that only requests beyond the
// floor pay a cold start. It measures one burst of withinBurst requests
// (baseline), lets the floor sit idle for idleDuration, then fires the same
// burst again (within_floor_*) plus a larger overflowBurst burst
// (overflow_burst_*). within_floor_ratio (post-idle vs baseline, same burst
// size) is the gated signal — overflow_burst_ratio is reported but
// intentionally ungated since exceeding the floor is expected to cold-start.
type provisionedIdle struct {
	floor         int           // provisioned target (the "floor") for the benchmark function
	poolsize      int           // shared environment pool size; must absorb floor + overflowBurst
	withinBurst   int           // concurrent requests for the baseline/within-floor bursts; must be <= floor
	overflowBurst int           // concurrent requests for the post-idle overflow burst; > floor by design
	idleDuration  time.Duration // how long the floor sits idle before each post-idle burst
}

func (p *provisionedIdle) Name() string   { return "provisioned-idle-warm" }
func (p *provisionedIdle) Tags() []string { return []string{"latency", "coldstart", "provisioned"} }

func (p *provisionedIdle) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	res.SetMeta("poolsize", strconv.Itoa(p.poolsize))
	res.SetMeta("floor", strconv.Itoa(p.floor))
	res.SetMeta("withinBurst", strconv.Itoa(p.withinBurst))
	res.SetMeta("overflowBurst", strconv.Itoa(p.overflowBurst))
	res.SetMeta("idleDuration", p.idleDuration.String())
	env := sc.Env()
	image := env.Images.Python
	if image == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}
	envName := sc.Name("burst-env")
	// Version 3, not the 1 every other scenario in this package uses: poolmgr's
	// getEnvPoolSize (pkg/executor/executortype/poolmgr/common.go) hardcodes a
	// pool of 3 for any env with Version < 3, ignoring Spec.Poolsize entirely.
	// Every other scenario's requested poolsize happens to already be 3, so
	// this never surfaced; this scenario is the first to need a real custom
	// pool size, which requires Version 3 to be honoured. The function itself
	// (pythonHelloSleep) is unchanged — v1's plain `def main():` signature is valid
	// at every environment version, confirmed against fission.io/docs.
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 3, Poolsize: p.poolsize}); err != nil {
		return res, err
	}
	// Start from a fully warm pool so the measurement is specialization +
	// refill, not initial pool creation.
	if err := env.WaitForPoolReady(ctx, envName, p.poolsize, 3*time.Minute); err != nil {
		return res, fmt.Errorf("pool warm-up: %w", err)
	}
	// Sub-scope lives for the whole scenario, not just one phase: the function
	// and route below must survive both idle windows and all three bursts.
	iter := sc.SubScope("provisionedBenchmark")
	defer iter.CleanupDetached(ctx, time.Minute)
	fnName := iter.Name("fn")
	if err := iter.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name:                   fnName,
		Env:                    envName,
		Code:                   []byte(pythonHelloSleep),
		Entrypoint:             "main",
		ExecutorType:           fv1.ExecutorTypePoolmgr,
		ProvisionedConcurrency: p.floor,
	}); err != nil {
		return res, err
	}

	route := "/" + fnName
	if err := iter.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route}); err != nil {
		return res, err
	}
	if err := env.WaitForProvisionedFloor(ctx, fnName, p.floor, 3*time.Minute); err != nil {
		return res, err
	}

	if err := env.WaitForRoutable(ctx, route, time.Minute); err != nil {
		return res, err
	}
	baselineSamples, failures := fireBurst(ctx, env.RouterURL()+route, p.withinBurst, 3*time.Minute)
	if len(baselineSamples) == 0 {
		err := fmt.Errorf("zero baseline samples were returned, pointless continuing")
		return res, err
	}
	baselineP99 := percentile(baselineSamples, 99)
	baselineP50 := percentile(baselineSamples, 50)
	res.Add("baseline_p99", "ms", report.Lower, float64(millis(baselineP99)))
	res.Add("baseline_p50", "ms", report.Lower, float64(millis(baselineP50)))
	res.Add("baseline_failures", "count", report.Lower, float64(failures))

	// Single idle window feeds both post-idle bursts below (within-floor, then
	// overflow) — no re-sleeping between them, so overflow measures recovery
	// from the same idle period the within-floor gate is judged against.
	select {
	case <-time.After(p.idleDuration):

	case <-ctx.Done():
		return res, ctx.Err()
	}

	withinFloorBurst, failures := fireBurst(ctx, env.RouterURL()+route, p.withinBurst, 3*time.Minute)
	if len(withinFloorBurst) == 0 {
		err := fmt.Errorf("zero within floor samples were returned, pointless continuing")
		return res, err
	}
	withinFloorP50 := percentile(withinFloorBurst, 50)
	withinFloorP99 := percentile(withinFloorBurst, 99)
	res.Add("within_floor_p99", "ms", report.Lower, float64(millis(withinFloorP99)))
	res.Add("within_floor_p50", "ms", report.Lower, float64(millis(withinFloorP50)))
	res.Add("within_floor_failures", "count", report.Lower, float64(failures))
	res.Add("within_floor_sample", "count", report.Higher, float64(len(withinFloorBurst)))
	// Ratio of raw time.Duration (ns) values, not the millis()-converted ones
	// above — the ns/ms unit cancels in the division either way, so this is
	// equivalent, just computed before conversion.
	res.Add("within_floor_ratio", "x", report.Lower, float64(withinFloorP99)/float64(baselineP99))

	overflowBurst, failures := fireBurst(ctx, env.RouterURL()+route, p.overflowBurst, 3*time.Minute)
	if len(overflowBurst) == 0 {
		err := fmt.Errorf("zero overflow burst samples were returned, pointless continuing")
		return res, err
	}
	overFlowBurstP50 := percentile(overflowBurst, 50)
	overFlowBurstP99 := percentile(overflowBurst, 99)
	res.Add("overflow_burst_p99", "ms", report.Lower, float64(millis(overFlowBurstP99)))
	res.Add("overflow_burst_p50", "ms", report.Lower, float64(millis(overFlowBurstP50)))
	res.Add("overflow_burst_failures", "count", report.Lower, float64(failures))
	res.Add("overflow_burst_sample", "count", report.Higher, float64(len(overflowBurst)))
	res.Add("overflow_burst_ratio", "x", report.Lower, float64(overFlowBurstP99)/float64(baselineP99))
	return res, nil
}
