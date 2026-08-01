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

type provisionedIdle struct {
	floor         int
	poolsize      int
	withinBurst   int
	overflowBurst int
	idleDuration  time.Duration
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
	// (pythonHello) is unchanged — v1's plain `def main():` signature is valid
	// at every environment version, confirmed against fission.io/docs.
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 3, Poolsize: p.poolsize}); err != nil {
		return res, err
	}
	// Start from a fully warm pool so the measurement is specialization +
	// refill, not initial pool creation.
	if err := env.WaitForPoolReady(ctx, envName, p.poolsize, 3*time.Minute); err != nil {
		return res, fmt.Errorf("pool warm-up: %w", err)
	}
	iter := sc.SubScope("baseline")
	defer iter.CleanupDetached(ctx, time.Minute)
	fnName := "provisionedIdleBenchmarkFn"
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
		return res, nil
	}
	env.WaitForProvisionedFloor(ctx, fnName, p.floor, 3*time.Minute)
	env.WaitForRoutable(ctx, route, time.Minute)
	baselineSamples, failures := fireBurst(ctx, env.RouterURL()+route, p.withinBurst, 3*time.Minute)
	baselineP99 := percentile(baselineSamples, 99)
	baselineP50 := percentile(baselineSamples, 50)
	res.Add("baseline_p99", "ms", report.Lower, float64(baselineP99))
	res.Add("baseline_p50", "ms", report.Lower, float64(baselineP50))
	res.Add("baseline_failures", "count", report.Higher, float64(failures))

	// idle
	select {
	case <-time.After(p.idleDuration):

	case <-ctx.Done():
		return res, ctx.Err()
	}

	withinFloorBurst, failures := fireBurst(ctx, env.RouterURL()+route, p.withinBurst, 3*time.Minute)
	withinFloorP50 := percentile(withinFloorBurst, 50)
	withinFloorP99 := percentile(withinFloorBurst, 99)
	res.Add("within_floor_p99", "ms", report.Lower, float64(withinFloorP99))
	res.Add("within_floor_p50", "ms", report.Lower, float64(withinFloorP50))
	res.Add("within_floor_failures", "count", report.Higher, float64(failures))
	res.Add("within_floor_sample", "count", report.Higher, float64(len(withinFloorBurst)))
	res.Add("within_floor_ratio", "x", report.Lower, float64(withinFloorP99)/float64(baselineP99))
	// idle
	select {
	case <-time.After(p.idleDuration):

	case <-ctx.Done():
		return res, ctx.Err()
	}
	overflowBurst, failures := fireBurst(ctx, env.RouterURL()+route, p.overflowBurst, 3*time.Minute)
	overFlowBurstP50 := percentile(overflowBurst, 50)
	overFlowBurstP99 := percentile(overflowBurst, 99)
	res.Add("overflow_burst_p99", "ms", report.Lower, float64(overFlowBurstP99))
	res.Add("overflow_burst_p50", "ms", report.Lower, float64(overFlowBurstP50))
	res.Add("overflow_burst_failures", "count", report.Higher, float64(failures))
	res.Add("overflow_burst_sample", "count", report.Higher, float64(len(overflowBurst)))
	res.Add("overflow_burst_ratio", "x", report.Lower, float64(overFlowBurstP99)/float64(baselineP99))
	return res, nil
}
