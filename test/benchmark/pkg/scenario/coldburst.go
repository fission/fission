// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"sync"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// coldBurst measures poolmgr cold-start latency under N *simultaneous* first
// requests — the dimension the sequential cold-start scenario cannot see. With
// the default requestsPerPod=1 every in-flight request wants its own
// specialized pod, so a burst larger than the pool exercises pool exhaustion
// and refill (ready-pod queue latency), and concurrent specialization
// queueing in the executor:
//
//   - same-fn: one function, N concurrent first requests — specialization
//     fan-out for a single function (executor per-function serialization,
//     pool drain, refill).
//   - distinct-fn: N functions, one first request each — cross-function pool
//     contention on a shared environment.
type coldBurst struct {
	distinct bool
	burst    int
	poolsize int
}

func (c *coldBurst) Name() string {
	if c.distinct {
		return "cold-burst-distinct-fn"
	}
	return "cold-burst-same-fn"
}

func (c *coldBurst) Tags() []string { return []string{"latency", "coldstart", "burst"} }

func (c *coldBurst) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	res.SetMeta("burst", fmt.Sprintf("%d", c.burst))
	res.SetMeta("poolsize", fmt.Sprintf("%d", c.poolsize))
	env := sc.Env()

	image := env.Images.Python
	if image == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}

	envName := sc.Name("burst-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 1, Poolsize: c.poolsize}); err != nil {
		return res, err
	}
	// Start from a fully warm pool so the measurement is specialization +
	// refill, not initial pool creation.
	if err := env.WaitForPoolReady(ctx, envName, c.poolsize, 3*time.Minute); err != nil {
		return res, fmt.Errorf("pool warm-up: %w", err)
	}

	// Provision the target function(s) and route(s) before firing anything:
	// creation must not overlap the measured burst.
	routes := make([]string, 0, c.burst)
	fnCount := 1
	if c.distinct {
		fnCount = c.burst
	}
	for i := range fnCount {
		fnName := sc.Name(fmt.Sprintf("fn%d", i))
		route := "/" + fnName
		if err := sc.CreateCodeFunction(ctx, harness.FunctionOptions{
			Name: fnName, Env: envName, Code: []byte(pythonHello), Entrypoint: "main",
			ExecutorType: fv1.ExecutorTypePoolmgr, MaxScale: 1,
		}); err != nil {
			return res, err
		}
		if err := sc.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route}); err != nil {
			return res, err
		}
	}
	for i := range c.burst {
		fnIdx := 0
		if c.distinct {
			fnIdx = i
		}
		routes = append(routes, "/"+sc.Name(fmt.Sprintf("fn%d", fnIdx)))
	}

	// Fire the burst: one goroutine per request, each anchored by
	// measureFirstSuccess's own 404-gated clock (route propagation is excluded
	// from the measured latency; provisioning 5xx is included).
	var (
		mu       sync.Mutex
		samples  []time.Duration
		failures int
	)
	burstStart := time.Now()
	var wg sync.WaitGroup
	for _, route := range routes {
		wg.Go(func() {
			d, ok := measureFirstSuccess(ctx, env.RouterURL()+route, 3*time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				samples = append(samples, d)
			} else {
				failures++
			}
		})
	}
	wg.Wait()
	makespan := time.Since(burstStart)

	if len(samples) == 0 {
		return res, fmt.Errorf("no successful burst samples (%d failures)", failures)
	}
	res.Add("burst_p50", "ms", report.Lower, millis(percentile(samples, 50)))
	res.Add("burst_p95", "ms", report.Lower, millis(percentile(samples, 95)))
	res.Add("burst_max", "ms", report.Lower, millis(percentile(samples, 100)))
	// Makespan is the wall time until the whole burst is served — the number a
	// user staring at a traffic spike experiences; it captures refill/queueing
	// serialization that per-request percentiles smear.
	res.Add("burst_makespan", "ms", report.Lower, millis(makespan))
	res.Add("samples", "count", report.Higher, float64(len(samples)))
	res.Add("failures", "count", report.Lower, float64(failures))
	return res, nil
}
