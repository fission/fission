// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// coldStart measures the latency of the first request to a fresh function: for
// poolmgr this is specialization of a warm generic pod; for newdeploy it is
// scale-from-zero of a per-function Deployment. It repeats create -> measure ->
// delete to gather a distribution, mirroring the legacy runbook methodology.
type coldStart struct {
	executor   fv1.ExecutorType
	iterations int
	poolsize   int
}

func (c *coldStart) Name() string { return "cold-start-" + string(c.executor) }

func (c *coldStart) Tags() []string {
	tags := []string{"latency", "coldstart"}
	if c.executor == fv1.ExecutorTypePoolmgr {
		tags = append(tags, "smoke")
	}
	return tags
}

func (c *coldStart) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	res.SetMeta("executor", string(c.executor))
	env := sc.Env()

	image := env.Images.Python
	if image == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}

	envName := sc.Name("cold-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 1, Poolsize: c.poolsize}); err != nil {
		return res, err
	}
	// For poolmgr the first request measures specialization, so wait for the
	// generic pool to warm first. newdeploy has no pool (scale-from-zero).
	if c.executor == fv1.ExecutorTypePoolmgr {
		if err := env.WaitForPoolReady(ctx, envName, 1, 3*time.Minute); err != nil {
			return res, fmt.Errorf("pool warm-up: %w", err)
		}
	}

	var samples []time.Duration
	failures := 0
	for i := range c.iterations {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if c.executor == fv1.ExecutorTypePoolmgr {
			// Let the pool refill between iterations so each measures a cold pod.
			_ = env.WaitForPoolReady(ctx, envName, 1, 2*time.Minute)
		}
		if d, ok := c.measureOne(ctx, env, envName, i); ok {
			samples = append(samples, d)
		} else {
			failures++
		}
	}

	if len(samples) == 0 {
		return res, fmt.Errorf("no successful cold-start samples (%d failures)", failures)
	}
	res.Add("cold_p50", "ms", report.Lower, millis(percentile(samples, 50)))
	res.Add("cold_p95", "ms", report.Lower, millis(percentile(samples, 95)))
	res.Add("cold_max", "ms", report.Lower, millis(percentile(samples, 100)))
	res.Add("samples", "count", report.Higher, float64(len(samples)))
	res.Add("failures", "count", report.Lower, float64(failures))
	return res, nil
}

// measureOne creates one function+route, measures the first successful request,
// and tears the pair down (its own Scope) regardless of outcome.
func (c *coldStart) measureOne(ctx context.Context, env *harness.Env, envName string, i int) (time.Duration, bool) {
	iter := env.NewScope(fmt.Sprintf("%s-i%d", c.Name(), i))
	defer iter.CleanupDetached(ctx, time.Minute)

	fnName := iter.Name("fn")
	route := "/" + fnName
	if err := iter.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name: fnName, Env: envName, Code: []byte(pythonHello), Entrypoint: "main",
		ExecutorType: c.executor, MinScale: 0, MaxScale: 1,
	}); err != nil {
		return 0, false
	}
	if err := iter.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route}); err != nil {
		return 0, false
	}
	return measureFirstSuccess(ctx, env.RouterURL()+route, 3*time.Minute)
}

// measureFirstSuccess polls url and returns the cold-start latency. 404s mean
// the route has not propagated yet and are discarded. The clock starts on the
// first non-404 (route live, provisioning begins) and the returned latency runs
// to the first 2xx — so a newdeploy scale-from-zero that surfaces transient
// 5xx/timeouts while provisioning is measured in full, not deflated to the
// post-warm request. For poolmgr (router blocks through specialization and
// returns 2xx directly) this collapses to that single request's latency.
func measureFirstSuccess(ctx context.Context, url string, timeout time.Duration) (time.Duration, bool) {
	client := &http.Client{Timeout: timeout}
	var coldStart time.Time
	var started bool
	var latency time.Duration
	err := harness.Poll(ctx, timeout, 200*time.Millisecond, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		t0 := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return false, nil // not up yet; keep polling
		}
		status := resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if status == http.StatusNotFound {
			return false, nil // route not propagated yet
		}
		// First non-404 means the route is live and provisioning has begun —
		// anchor the clock there, then return at the first 2xx so the latency
		// spans the full provisioning (incl. any transient 5xx).
		if !started {
			coldStart = t0
			started = true
		}
		if status >= 200 && status < 300 {
			latency = time.Since(coldStart)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return 0, false
	}
	return latency, true
}
