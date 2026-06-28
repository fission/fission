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

// coldStartConfigDeps measures poolmgr cold-start latency for a function that
// references several Secrets and ConfigMaps. During specialization the executor
// runs a pre-flight existence check for each referenced Secret/ConfigMap in the
// function namespace, so cold start pays one lookup per reference. The plain
// cold-start scenario uses a dependency-free function and never exercises that
// path, so a change to how those lookups are served — e.g. serving them from the
// executor's informer cache instead of the API server — is invisible to it but
// shows up here. Poolmgr only: the check lives in poolmgr's specialization path
// (newdeploy handles its references on a different path).
type coldStartConfigDeps struct {
	iterations int
	poolsize   int
	secrets    int
	configMaps int
}

func (c *coldStartConfigDeps) Name() string { return "cold-start-poolmgr-configdeps" }

// Not tagged "smoke": the per-reference lookup delta is a few-ms structural
// change that the single-sample smoke run can't resolve above its noise floor,
// so this belongs in the repeated full/dispatch runs that feed the trend.
func (c *coldStartConfigDeps) Tags() []string { return []string{"latency", "coldstart"} }

func (c *coldStartConfigDeps) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	res.SetMeta("secrets", strconv.Itoa(c.secrets))
	res.SetMeta("configmaps", strconv.Itoa(c.configMaps))
	env := sc.Env()

	image := env.Images.Python
	if image == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE unset")
	}
	if c.secrets == 0 && c.configMaps == 0 {
		return res, skip("no secret/configmap references configured")
	}

	envName := sc.Name("cd-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 1, Poolsize: c.poolsize}); err != nil {
		return res, err
	}

	// Create the referenced Secrets/ConfigMaps once at the scenario scope; every
	// iteration's function references the same set, so each specialization
	// re-runs the per-reference existence checks against them.
	secrets := make([]string, c.secrets)
	for i := range secrets {
		name := sc.Name("cd-secret-" + strconv.Itoa(i))
		if err := sc.CreateSecret(ctx, name, map[string]string{"key": "value"}); err != nil {
			return res, err
		}
		secrets[i] = name
	}
	configMaps := make([]string, c.configMaps)
	for i := range configMaps {
		name := sc.Name("cd-cm-" + strconv.Itoa(i))
		if err := sc.CreateConfigMap(ctx, name, map[string]string{"key": "value"}); err != nil {
			return res, err
		}
		configMaps[i] = name
	}

	if err := env.WaitForPoolReady(ctx, envName, 1, 3*time.Minute); err != nil {
		return res, fmt.Errorf("pool warm-up: %w", err)
	}

	var samples []time.Duration
	failures := 0
	for i := range c.iterations {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// Let the pool refill so each iteration measures a cold pod.
		_ = env.WaitForPoolReady(ctx, envName, 1, 2*time.Minute)
		if d, ok := c.measureOne(ctx, env, envName, secrets, configMaps, i); ok {
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

// measureOne creates one function (referencing all the scenario's Secrets and
// ConfigMaps) + route, measures the first successful request, and tears the pair
// down (its own Scope) regardless of outcome.
func (c *coldStartConfigDeps) measureOne(ctx context.Context, env *harness.Env, envName string, secrets, configMaps []string, i int) (time.Duration, bool) {
	iter := env.NewScope(fmt.Sprintf("%s-i%d", c.Name(), i))
	defer iter.CleanupDetached(ctx, time.Minute)

	fnName := iter.Name("fn")
	route := "/" + fnName
	if err := iter.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name: fnName, Env: envName, Code: []byte(pythonHello), Entrypoint: "main",
		ExecutorType: fv1.ExecutorTypePoolmgr, MinScale: 0, MaxScale: 1,
		Secrets: secrets, ConfigMaps: configMaps,
	}); err != nil {
		return 0, false
	}
	if err := iter.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route}); err != nil {
		return 0, false
	}
	return measureFirstSuccess(ctx, env.RouterURL()+route, 3*time.Minute)
}
