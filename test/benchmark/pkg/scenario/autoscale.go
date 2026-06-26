// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// autoscaleNewdeploy drives a CPU-bound newdeploy function under load and
// observes HPA scale-up: time-to-first-new-replica and the peak replica count.
type autoscaleNewdeploy struct {
	maxScale    int
	concurrency int
	observe     time.Duration // how long to hold load while watching replicas
}

func (a *autoscaleNewdeploy) Name() string   { return "autoscale-newdeploy" }
func (a *autoscaleNewdeploy) Tags() []string { return []string{"autoscale", "elasticity"} }

func (a *autoscaleNewdeploy) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()
	if env.Images.Python == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}

	envName := sc.Name("as-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: env.Images.Python, Version: 1, Poolsize: 1}); err != nil {
		return res, err
	}
	fnName := sc.Name("as-fn")
	route := "/" + fnName
	// Small CPU request + 50% target so the cpuburn load pushes utilization
	// over the threshold and the HPA scales out.
	if err := sc.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name: fnName, Env: envName, Code: []byte(pythonCPUBurn), Entrypoint: "main",
		ExecutorType: fv1.ExecutorTypeNewdeploy, MinScale: 1, MaxScale: a.maxScale,
		TargetCPUPercent: 50, MinCPU: 50, MaxCPU: 200,
	}); err != nil {
		return res, err
	}
	if err := sc.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route}); err != nil {
		return res, err
	}
	if err := env.WaitForRoutable(ctx, route, 3*time.Minute); err != nil {
		return res, err
	}

	// Hold load in the background while we sample the ready replica count, and
	// join it before returning so teardown doesn't race in-flight requests.
	loadCtx, stop := context.WithCancel(ctx)
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		loadgen.RunClosedLoop(loadCtx, loadgen.ClosedLoopConfig{
			Doer:        env.PublicTarget(route, a.concurrency, true).Do,
			Concurrency: a.concurrency,
			Duration:    a.observe + time.Minute, // outlive the observation window
		})
	}()
	defer func() {
		stop()
		<-loadDone
	}()

	deadline := time.Now().Add(a.observe)
	maxReplicas := 1
	var scaleUp time.Duration
	start := time.Now()
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		n, err := env.CountReadyFunctionPods(ctx, fnName)
		if err == nil {
			if n > maxReplicas {
				maxReplicas = n
			}
			if scaleUp == 0 && n > 1 {
				scaleUp = time.Since(start)
			}
		}
		time.Sleep(5 * time.Second)
	}

	scaled := 0.0
	if scaleUp > 0 {
		scaled = 1
		res.Add("scale_up_seconds", "s", report.Lower, scaleUp.Seconds())
	}
	res.Add("max_replicas", "count", report.Higher, float64(maxReplicas))
	res.Add("scaled", "ratio", report.Higher, scaled)
	return res, nil
}
