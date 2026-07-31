// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// provisionedWarm measures RFC-0026 invariant P5: one function's eager
// provisioned-concurrency warm-up burst must not regress another function's
// on-demand cold-start latency beyond an agreed bound. It runs two phases —
// baseline (function B cold-starts alone) then treatment (function A's
// warm-up burst running concurrently with the same B measurement) — and
// gates the ratio between them, not either absolute number, since absolute
// cold-start latency on a shared runner is too noisy to gate directly.
// Weekly-full-suite only: not tagged smoke, not gated per-PR (see
// ~/claude-plans/rfc-0026-pr2-missing-invariants.md Stage 3).
type provisionedWarm struct {
	burstTarget int // function A's provisioned target during the treatment phase
	poolsize    int // shared environment pool size; must absorb both A's burst and B's draws
	iterations  int // function B cold-start samples per phase
}

func (p *provisionedWarm) Name() string   { return "provisioned-warm-starvation" }
func (p *provisionedWarm) Tags() []string { return []string{"latency", "coldstart", "provisioned"} }

func (p *provisionedWarm) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	res.SetMeta("poolsize", strconv.Itoa(p.poolsize))
	res.SetMeta("iterations", strconv.Itoa(p.iterations))
	res.SetMeta("burstTarget", strconv.Itoa(p.burstTarget))
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

	// Baseline phase: measure B's cold start with function A not existing yet,
	// so this number is the no-contention number the treatment phase compares
	// against.
	var baselineSamples []time.Duration
	for i := range p.iterations {
		if d, ok := measureColdStart(ctx, sc, envName, "baseline-i", i, harness.FunctionOptions{
			Code: []byte(pythonHello), Entrypoint: "main", ExecutorType: fv1.ExecutorTypePoolmgr,
		}); ok {
			baselineSamples = append(baselineSamples, d)
		}
	}
	if len(baselineSamples) == 0 {
		return res, fmt.Errorf("no successful baseline samples")
	}
	baseline := percentile(baselineSamples, 50)
	baselineP95 := percentile(baselineSamples, 95)
	res.Add("baseline_p50", "ms", report.Lower, millis(baseline))
	res.Add("baseline_p95", "ms", report.Lower, millis(baselineP95))
	// Treatment phase: creating function A with a provisioned target is what
	// triggers the executor's eager warm-up burst in the background — nothing
	// else drives it. It lives in the outer scope (not a sub-scope) so it
	// persists for the whole treatment phase below.
	aName := sc.Name("fn-a")
	if err := sc.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name:                   aName,
		Env:                    envName,
		Code:                   []byte(pythonHello),
		Entrypoint:             "main",
		ExecutorType:           fv1.ExecutorTypePoolmgr,
		ProvisionedConcurrency: p.burstTarget,
	}); err != nil {
		return res, err
	}

	var treatedSamples []time.Duration
	overlapCount := 0
	failures := 0
	for i := range p.iterations {
		// Check A's status BEFORE measuring this B sample: if A hasn't reached
		// its target yet, this sample lands inside the contention window and
		// counts toward overlapCount. If the burst finishes before B ever
		// measures during it, overlapCount stays 0 and the run below fails
		// loudly instead of silently reporting a meaningless "no regression".
		fn, err := env.Clients.Fission.CoreV1().Functions(env.Namespace).Get(ctx, aName, metav1.GetOptions{})
		if err == nil && fn.Status.ProvisionedReady < fn.Status.ProvisionedTarget {
			overlapCount++
		}
		if d, ok := measureColdStart(ctx, sc, envName, "treatment-i", i, harness.FunctionOptions{
			Code: []byte(pythonHello), Entrypoint: "main", ExecutorType: fv1.ExecutorTypePoolmgr,
		}); ok {
			treatedSamples = append(treatedSamples, d)
		} else {
			failures++
		}
	}
	if len(treatedSamples) == 0 {
		return res, fmt.Errorf("no successful treatment samples")
	}
	if overlapCount == 0 {
		return res, fmt.Errorf("burst finished before any B sample overlapped it - measurement is meaningless")
	}
	contended := percentile(treatedSamples, 50)
	contendedP95 := percentile(treatedSamples, 95)
	res.Add("contended_p50", "ms", report.Lower, millis(contended))
	res.Add("contended_p95", "ms", report.Lower, millis(contendedP95))
	res.Add("contended_ratio", "x", report.Lower, float64(contended)/float64(baseline))
	res.Add("contended_ratio_p95", "x", report.Lower, float64(contendedP95)/float64(baselineP95))
	res.Add("overlapped_samples", "count", report.Higher, float64(overlapCount))
	res.Add("samples", "count", report.Higher, float64(len(treatedSamples)))
	res.Add("failures", "count", report.Lower, float64(failures))
	return res, nil
}
