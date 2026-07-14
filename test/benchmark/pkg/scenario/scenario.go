// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package scenario is the benchmark catalog. Each scenario provisions resources
// in its own harness.Scope, drives load, collects client- and server-side
// metrics, and returns a report.ScenarioResult. Scenarios are constructed from
// Params (config/flags) by BuildAll, filtered by Select, and executed by Run.
package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// Scenario is one benchmark measurement. Run owns its resource lifecycle via the
// provided Scope (the runner calls Scope.Cleanup afterwards regardless of
// outcome).
type Scenario interface {
	Name() string
	Tags() []string
	Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error)
}

// errSkip signals a scenario opted out (e.g. a required image is unset); the
// runner records it as skipped rather than failed.
type errSkip struct{ reason string }

func (e errSkip) Error() string { return e.reason }

// skip returns a sentinel skip error.
func skip(reason string) error { return errSkip{reason: reason} }

// Duration is a time.Duration that unmarshals from a human-friendly string
// ("60s") as well as a raw nanosecond number, so Params can be loaded straight
// from YAML without a mirror config struct.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalJSON accepts either a duration string or a raw number of nanoseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*d = Duration(n)
	return nil
}

// Params holds the tunable knobs for the built-in scenarios. Zero values fall
// back to DefaultParams via normalize. Field tags let it load directly from the
// scenarios YAML.
type Params struct {
	Poolsize       int `json:"poolsize"`
	ColdIterations int `json:"coldIterations"`
	// Repetitions re-runs every selected scenario N times (each in a fresh
	// Scope) and reports the per-metric median plus min..max spread, so a
	// single noisy cluster sample can't masquerade as a regression or a win.
	Repetitions int `json:"repetitions"`
	// BurstSize is the number of simultaneous first-requests the cold-burst
	// scenarios fire; sized above Poolsize so the burst forces pool exhaustion
	// and refill rather than being absorbed by warm pods.
	BurstSize         int                `json:"burstSize"`
	WarmDuration      Duration           `json:"warmDuration"`
	WarmWarmup        Duration           `json:"warmWarmup"`
	WarmConcurrency   int                `json:"warmConcurrency"`
	ConcurrencyLevels []int              `json:"concurrencyLevels"`
	RPSLevels         []int              `json:"rpsLevels"`
	PayloadSizes      []int              `json:"payloadSizes"` // bytes
	Executors         []fv1.ExecutorType `json:"executors"`

	// Number of Secrets/ConfigMaps the cold-start-poolmgr-configdeps scenario's
	// function references, sizing the per-reference specialization-time lookups.
	ConfigDepsSecrets    int `json:"configDepsSecrets"`
	ConfigDepsConfigMaps int `json:"configDepsConfigmaps"`

	AutoscaleMaxScale int      `json:"autoscaleMaxScale"`
	AutoscaleObserve  Duration `json:"autoscaleObserve"`
	IndexScaleCount   int      `json:"indexScaleCount"`
	RouteChurnCount   int      `json:"routeChurnCount"`
	BuildTimeout      Duration `json:"buildTimeout"`
}

// DefaultParams returns the standard full-run parameters.
func DefaultParams() Params {
	return Params{
		Poolsize:             3,
		ColdIterations:       20,
		Repetitions:          1,
		BurstSize:            10,
		WarmDuration:         Duration(60 * time.Second),
		WarmWarmup:           Duration(10 * time.Second),
		WarmConcurrency:      50,
		ConcurrencyLevels:    []int{10, 50, 100, 250, 500},
		RPSLevels:            []int{100, 250, 500, 1000},
		PayloadSizes:         []int{1 << 10, 10 << 10, 100 << 10, 1 << 20},
		Executors:            []fv1.ExecutorType{fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy},
		ConfigDepsSecrets:    5,
		ConfigDepsConfigMaps: 5,
		AutoscaleMaxScale:    5,
		AutoscaleObserve:     Duration(3 * time.Minute),
		IndexScaleCount:      1000,
		RouteChurnCount:      500,
		BuildTimeout:         Duration(5 * time.Minute),
	}
}

func (p Params) normalize() Params {
	d := DefaultParams()
	if p.Poolsize == 0 {
		p.Poolsize = d.Poolsize
	}
	if p.ColdIterations == 0 {
		p.ColdIterations = d.ColdIterations
	}
	if p.Repetitions == 0 {
		p.Repetitions = d.Repetitions
	}
	if p.BurstSize == 0 {
		p.BurstSize = d.BurstSize
	}
	if p.WarmDuration == 0 {
		p.WarmDuration = d.WarmDuration
	}
	if p.WarmWarmup == 0 {
		p.WarmWarmup = d.WarmWarmup
	}
	if p.WarmConcurrency == 0 {
		p.WarmConcurrency = d.WarmConcurrency
	}
	if len(p.ConcurrencyLevels) == 0 {
		p.ConcurrencyLevels = d.ConcurrencyLevels
	}
	if len(p.RPSLevels) == 0 {
		p.RPSLevels = d.RPSLevels
	}
	if len(p.PayloadSizes) == 0 {
		p.PayloadSizes = d.PayloadSizes
	}
	if len(p.Executors) == 0 {
		p.Executors = d.Executors
	}
	if p.ConfigDepsSecrets == 0 {
		p.ConfigDepsSecrets = d.ConfigDepsSecrets
	}
	if p.ConfigDepsConfigMaps == 0 {
		p.ConfigDepsConfigMaps = d.ConfigDepsConfigMaps
	}
	if p.AutoscaleMaxScale == 0 {
		p.AutoscaleMaxScale = d.AutoscaleMaxScale
	}
	if p.AutoscaleObserve == 0 {
		p.AutoscaleObserve = d.AutoscaleObserve
	}
	if p.IndexScaleCount == 0 {
		p.IndexScaleCount = d.IndexScaleCount
	}
	if p.RouteChurnCount == 0 {
		p.RouteChurnCount = d.RouteChurnCount
	}
	if p.BuildTimeout == 0 {
		p.BuildTimeout = d.BuildTimeout
	}
	return p
}

// BuildAll constructs every built-in scenario from p.
func BuildAll(p Params) []Scenario {
	p = p.normalize()
	var out []Scenario
	for _, ex := range p.Executors {
		out = append(out, &coldStart{executor: ex, iterations: p.ColdIterations, poolsize: p.Poolsize})
	}
	out = append(out, &coldStartConfigDeps{iterations: p.ColdIterations, poolsize: p.Poolsize, secrets: p.ConfigDepsSecrets, configMaps: p.ConfigDepsConfigMaps})
	out = append(out, &coldBurst{distinct: false, burst: p.BurstSize, poolsize: p.Poolsize})
	out = append(out, &coldBurst{distinct: true, burst: p.BurstSize, poolsize: p.Poolsize})
	for _, ex := range p.Executors {
		out = append(out, &warmPath{executor: ex, duration: p.WarmDuration.D(), warmup: p.WarmWarmup.D(), concurrency: p.WarmConcurrency, poolsize: p.Poolsize})
	}
	out = append(out, &concurrencySweep{levels: p.ConcurrencyLevels, duration: p.WarmDuration.D(), warmup: p.WarmWarmup.D(), poolsize: p.Poolsize})
	out = append(out, &rpsSweep{levels: p.RPSLevels, duration: p.WarmDuration.D(), warmup: p.WarmWarmup.D(), poolsize: p.Poolsize})
	out = append(out, &payloadSweep{sizes: p.PayloadSizes, duration: p.WarmDuration.D(), warmup: p.WarmWarmup.D(), concurrency: p.WarmConcurrency, poolsize: p.Poolsize})
	out = append(out, &autoscaleNewdeploy{maxScale: p.AutoscaleMaxScale, concurrency: p.WarmConcurrency, observe: p.AutoscaleObserve.D()})
	out = append(out, &buildTime{timeout: p.BuildTimeout.D()})
	out = append(out, &routerIndexScale{count: p.IndexScaleCount})
	out = append(out, &routeChurn{count: p.RouteChurnCount})
	out = append(out, &asyncInvoke{duration: p.WarmDuration.D(), warmup: p.WarmWarmup.D(), concurrency: p.WarmConcurrency, poolsize: p.Poolsize})
	return out
}

// Select filters scenarios by explicit names and/or tags. Empty names and tags
// returns all. A scenario matches if its name is in names OR it carries any of
// the tags.
func Select(all []Scenario, names, tags []string) []Scenario {
	if len(names) == 0 && len(tags) == 0 {
		return all
	}
	var out []Scenario
	for _, s := range all {
		if slices.Contains(names, s.Name()) {
			out = append(out, s)
			continue
		}
		for _, tag := range s.Tags() {
			if slices.Contains(tags, tag) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// Names returns the scenario names, sorted.
func Names(all []Scenario) []string {
	names := make([]string, 0, len(all))
	for _, s := range all {
		names = append(names, s.Name())
	}
	sort.Strings(names)
	return names
}

// Run executes the scenarios against env, isolating each in its own Scope and
// always cleaning up. repetitions > 1 re-runs each scenario in fresh scopes and
// folds the results via report.Aggregate. A scenario error or skip is recorded
// in the result and the run continues (failure budget).
func Run(ctx context.Context, env *harness.Env, scenarios []Scenario, repetitions int) report.Run {
	if repetitions < 1 {
		repetitions = 1
	}
	run := report.Run{RunID: env.RunID, StartedAt: time.Now()}
	for _, s := range scenarios {
		reps := make([]report.ScenarioResult, 0, repetitions)
		for rep := range repetitions {
			label := s.Name()
			if repetitions > 1 {
				// The rep index keeps resource names distinct across reps: the
				// previous rep's cleanup is detached and may still be deleting
				// same-named resources when the next rep provisions.
				label = fmt.Sprintf("%s-r%d", s.Name(), rep)
			}
			res := runOne(ctx, env, s, label)
			reps = append(reps, res)
			if res.Skipped || res.Error != "" {
				break // a skip is deterministic; an error already fails the gate
			}
		}
		run.Scenarios = append(run.Scenarios, report.Aggregate(reps))
	}
	run.FinishedAt = time.Now()
	return run
}

func runOne(ctx context.Context, env *harness.Env, s Scenario, scopeLabel string) (res report.ScenarioResult) {
	sc := env.NewScope(scopeLabel)
	res.Name = s.Name()
	res.Tags = s.Tags()
	defer func() {
		// Honor the failure budget: a panic in one scenario becomes its error,
		// not a process crash that loses every other scenario's results.
		if r := recover(); r != nil {
			res.Error = fmt.Sprintf("panic: %v", r)
		}
		_ = sc.CleanupDetached(ctx, 2*time.Minute)
	}()

	before, beforeOK := apiserverCalls(ctx, env)
	out, err := s.Run(ctx, sc)
	out.Name = s.Name()
	out.Tags = s.Tags()
	if isSkip(err) {
		out.Skipped = true
		out.Skip = err.Error()
	} else if err != nil {
		out.Error = err.Error()
	} else if beforeOK {
		// after < before means a counter reset (component restart) — drop the
		// sample rather than report a bogus delta.
		if after, afterOK := apiserverCalls(ctx, env); afterOK && after >= before {
			out.Add("apiserver_calls", "count", report.Lower, after-before)
		}
	}
	res = out
	return res
}

// apiserverCalls sums the control-plane components' client-side apiserver
// request counters. rest_client_requests_total is registered by
// controller-runtime's metrics registry in every fission binary, so a
// before/after delta attributes a scenario's apiserver traffic to Fission —
// something apiserver_request_total (which has no user-agent label) cannot.
// Prometheus scrapes on an interval, so the delta trails by up to one scrape;
// treat it as an attribution signal for A/B comparisons, not an exact count.
func apiserverCalls(ctx context.Context, env *harness.Env) (float64, bool) {
	if !env.Capturer.PrometheusEnabled() {
		return 0, false
	}
	q := fmt.Sprintf(`sum(rest_client_requests_total{namespace=%q})`, env.FissionNamespace())
	v, found, err := env.Capturer.QueryInstant(ctx, q)
	if err != nil || !found {
		return 0, false
	}
	return v, true
}

func isSkip(err error) bool {
	var s errSkip
	return errors.As(err, &s)
}

// millis converts a duration to fractional milliseconds.
func millis(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// sizeLabel renders a byte count compactly (e.g. 1KiB, 1MiB) for metric names.
// It only abbreviates exact multiples, so distinct sizes never collapse to the
// same label (which would emit colliding metric names).
func sizeLabel(bytes int) string {
	switch {
	case bytes != 0 && bytes%(1<<20) == 0:
		return strconv.Itoa(bytes>>20) + "MiB"
	case bytes != 0 && bytes%(1<<10) == 0:
		return strconv.Itoa(bytes>>10) + "KiB"
	default:
		return strconv.Itoa(bytes) + "B"
	}
}

// latencyMetrics appends the standard latency/throughput metrics from a loadgen
// result to res. Latency percentiles come from successful requests only, so they
// are omitted when there were none — emitting the empty-histogram zeros would
// read as a latency improvement when the run actually failed. Throughput and
// error rate are always reported.
func latencyMetrics(res *report.ScenarioResult, prefix string, r loadgen.Result) {
	if r.Total > r.Errors {
		res.Add(prefix+"p50", "ms", report.Lower, millis(r.P50))
		res.Add(prefix+"p95", "ms", report.Lower, millis(r.P95))
		res.Add(prefix+"p99", "ms", report.Lower, millis(r.P99))
		res.Add(prefix+"p99.9", "ms", report.Lower, millis(r.P999))
		res.Add(prefix+"max", "ms", report.Lower, millis(r.Max))
	}
	res.Add(prefix+"throughput", "rps", report.Higher, r.RPS)
	res.Add(prefix+"error_rate", "ratio", report.Lower, r.ErrorRate)
}

// endpointCacheHitRatio is the RFC-0002 steady-state hit ratio: served from the
// slice-fed index vs all resolutions.
const endpointCacheHitRatio = `sum(rate(fission_router_endpointcache_hits_total[1m])) / ` +
	`(sum(rate(fission_router_endpointcache_hits_total[1m])) + ` +
	`sum(rate(fission_router_endpointcache_misses_total[1m])) + ` +
	`sum(rate(fission_router_endpointcache_fallbacks_total[1m])))`

// addServerMetrics best-effort augments res with server-side signals when a
// Prometheus endpoint is configured.
func addServerMetrics(ctx context.Context, env *harness.Env, res *report.ScenarioResult) {
	if !env.Capturer.PrometheusEnabled() {
		return
	}
	// Emit whenever a sample exists (even 0) so a genuine hit-ratio collapse is
	// visible rather than silently dropped.
	if v, found, err := env.Capturer.QueryInstant(ctx, endpointCacheHitRatio); err == nil && found {
		res.Add("endpointcache_hit_ratio", "ratio", report.Higher, v)
	}
}

// warmRuntime selects the language runtime for a warm-path/sweep function.
// Python's default server is single-threaded (bjoern), so it caps effective
// pod concurrency at 1; Node's event loop serves many in-flight requests per
// pod. warm-path stays python for trend continuity; the sweeps use node so
// concurrency levels above 1 measure Fission, not the runtime.
type warmRuntime int

const (
	runtimePython warmRuntime = iota
	runtimeNode
)

// provisionWarmFunction creates an env + code function + route sized to serve
// concurrent requests from a single warm pod (high requestsPerPod, so the
// measurement isolates router/proxy overhead), and waits until it is routable —
// which also warms it. For newdeploy/container the function pins minScale=1 so
// a backing pod exists before load starts (poolmgr warms via its generic
// pool). methods controls the route's allowed HTTP methods. The returned
// fnName feeds per-function PromQL (e.g. the cold-start counter delta).
func provisionWarmFunction(ctx context.Context, sc *harness.Scope, executor fv1.ExecutorType, runtime warmRuntime, poolsize, requestsPerPod int, methods []string) (route, fnName string, err error) {
	env := sc.Env()
	image, code, entrypoint := env.Images.Python, pythonHello, "main"
	if runtime == runtimeNode {
		// Node v1 loads a single-file module with no entrypoint.
		image, code, entrypoint = env.Images.Node, nodeHello, ""
	}
	if image == "" {
		return "", "", skip("runtime image unset (PYTHON_RUNTIME_IMAGE / NODE_RUNTIME_IMAGE)")
	}
	envName := sc.Name("env")
	if err = sc.CreateEnv(ctx, harness.EnvOptions{Name: envName, Image: image, Version: 1, Poolsize: poolsize}); err != nil {
		return "", "", err
	}
	minScale := 0
	if executor != fv1.ExecutorTypePoolmgr {
		minScale = 1
	}
	fnName = sc.Name("fn")
	route = "/" + fnName
	if err = sc.CreateCodeFunction(ctx, harness.FunctionOptions{
		Name: fnName, Env: envName, Code: []byte(code), Entrypoint: entrypoint,
		ExecutorType: executor, MinScale: minScale, RequestsPerPod: requestsPerPod,
	}); err != nil {
		return "", "", err
	}
	if err = sc.CreateRoute(ctx, harness.RouteOptions{Function: fnName, URL: route, Methods: methods}); err != nil {
		return "", "", err
	}
	if err = env.WaitForRoutable(ctx, route, 3*time.Minute); err != nil {
		return "", "", fmt.Errorf("warm-up: %w", err)
	}
	return route, fnName, nil
}

// functionColdStarts reads the executor's per-function specialization counter
// (fission_function_cold_starts_total). An absent series reads as 0 with
// ok=true when Prometheus is reachable — a warm function that never
// specialized simply has no samples yet.
func functionColdStarts(ctx context.Context, env *harness.Env, fnName string) (float64, bool) {
	if !env.Capturer.PrometheusEnabled() {
		return 0, false
	}
	q := fmt.Sprintf(`sum(fission_function_cold_starts_total{function_name=%q,function_namespace=%q})`, fnName, env.Namespace)
	v, found, err := env.Capturer.QueryInstant(ctx, q)
	if err != nil {
		return 0, false
	}
	if !found {
		return 0, true
	}
	return v, true
}

// snapshotPprof writes a labelled pprof snapshot for the scenario when an
// artifact dir and pprof targets are configured (best-effort).
func snapshotPprof(ctx context.Context, env *harness.Env, scenarioName, label string) {
	if env.ArtifactDir == "" {
		return
	}
	_ = env.Capturer.SnapshotPprof(ctx, label, filepath.Join(env.ArtifactDir, "pprof", scenarioName))
}

// dumpPromRange best-effort writes a Prometheus range-query result for the load
// window to the artifact dir, for offline analysis (no-op without an artifact
// dir / Prometheus).
func dumpPromRange(ctx context.Context, env *harness.Env, scenarioName, metricName, query string, start, end time.Time) {
	if env.ArtifactDir == "" || !env.Capturer.PrometheusEnabled() {
		return
	}
	data, err := env.Capturer.QueryRangeRaw(ctx, query, start, end, 5*time.Second)
	if err != nil || len(data) == 0 {
		return
	}
	dir := filepath.Join(env.ArtifactDir, "prometheus", scenarioName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, metricName+".json"), data, 0o644)
}

// percentile returns the q-quantile (0..100) of a sorted-or-unsorted slice.
func percentile(samples []time.Duration, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	s := slices.Clone(samples)
	slices.Sort(s)
	idx := int(float64(len(s)-1) * q / 100.0)
	return s[idx]
}
