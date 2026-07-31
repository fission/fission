// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// upgradeUnderLoad is the RFC-0028 phase-1 measurement scenario: seed fixtures
// on the running (N−1) control plane, drive constant-rate load against the
// public router, execute the provided upgrade command mid-load, and classify
// every request outcome so the upgrade's user-visible cost is a number rather
// than folklore.
//
// Classification is the load-bearing part. The RFC-0015 attribution header
// (X-Fission-Component) is written by the router's proxy error handler — but
// the worst case this scenario exists to catch, a window with NO Ready router
// endpoints, produces no response and no header at all. Transport-level
// failures (status 0: dial errors, resets, client timeouts) are therefore a
// first-class bucket, not folded into a generic error count. Responses that
// fail without an attribution header (e.g. a 404 from a rebuilt-but-empty
// route table) get their own bucket so nothing platform-caused can hide.
//
// The scenario itself is non-gating by design: it errors only on
// infrastructure failure (fixtures cannot be provisioned, the upgrade command
// fails, the rollout never completes). The counters it reports are evaluated
// against the published error budget by the CI workflow, so the budget can
// tighten to zero without a code change here.
//
// Fixture coverage in this phase: a poolmgr function, a newdeploy function,
// and an async enqueue batch. MQ topic pipelines and WorkflowRuns are named
// follow-ups — they need a broker install and an N−1 release that ships
// workflows, respectively.
type upgradeUnderLoad struct {
	cmd      string        // shell command executed mid-load (helm upgrade …)
	baseline time.Duration // pre-upgrade load window (steady-state reference)
	settle   time.Duration // post-rollout load window
	rps      int           // per-target request rate
	poolsize int
	timeout  time.Duration // upgrade command + rollout budget
}

func (u *upgradeUnderLoad) Name() string   { return "upgrade-under-load" }
func (u *upgradeUnderLoad) Tags() []string { return []string{"upgrade"} }

// upgradeCounters classifies request outcomes; safe for concurrent use.
// closing is set immediately before the load context is cancelled at the end
// of the settle window: requests still in flight at that moment fail with
// context.Canceled — a shutdown artifact of the measurement, not a platform
// failure — and must not pollute the transport-failure bucket the error
// budget gates on.
type upgradeCounters struct {
	total, transport, attributed, otherErr, ok atomic.Int64
	closing                                    atomic.Bool
}

func (c *upgradeCounters) observe(status int, headerVal string, err error) {
	if c.closing.Load() {
		return
	}
	c.total.Add(1)
	switch {
	case status == 0:
		c.transport.Add(1)
	case err == nil:
		c.ok.Add(1)
	case headerVal != "":
		c.attributed.Add(1)
	default:
		c.otherErr.Add(1)
	}
}

func (c *upgradeCounters) add(res *report.ScenarioResult, prefix string) {
	res.Add(prefix+"requests", "count", report.Higher, float64(c.total.Load()))
	res.Add(prefix+"ok", "count", report.Higher, float64(c.ok.Load()))
	// The three failure buckets the pass criteria are written against.
	res.Add(prefix+"transport_failures", "count", report.Lower, float64(c.transport.Load()))
	res.Add(prefix+"platform_attributed", "count", report.Lower, float64(c.attributed.Load()))
	res.Add(prefix+"unattributed_errors", "count", report.Lower, float64(c.otherErr.Load()))
}

func (u *upgradeUnderLoad) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// Fixtures on the still-old control plane. The poolmgr route also takes
	// POST so the async batch can reuse it.
	methods := []string{http.MethodGet, http.MethodPost}
	poolRoute, poolFn, err := provisionWarmFunction(ctx, sc, fv1.ExecutorTypePoolmgr, runtimeNode, u.poolsize, 500, methods)
	if err != nil {
		return res, err
	}
	ndRoute, ndFn, err := provisionWarmFunction(ctx, sc, fv1.ExecutorTypeNewdeploy, runtimeNode, u.poolsize, 500, methods)
	if err != nil {
		return res, err
	}
	res.SetMeta("poolmgr_function", poolFn)
	res.SetMeta("newdeploy_function", ndFn)

	// WaitForRoutable specialized the poolmgr function; snapshot its
	// specialized (managed=false, not Deployment-owned) pod UIDs. Only these
	// pods are expected to survive the upgrade — generic pool pods are
	// Deployment-owned and roll with new UIDs by design (U2 vs U5).
	specializedBefore, err := specializedPodUIDs(ctx, env, poolFn)
	if err != nil {
		return res, fmt.Errorf("snapshot specialized pods: %w", err)
	}
	if len(specializedBefore) == 0 {
		return res, fmt.Errorf("no specialized pod for %s after warm-up; cannot measure warm survival", poolFn)
	}

	// Async: probe support (501 = disabled), then enqueue a pre-upgrade batch
	// whose completion after the roll is the queue-durability signal.
	// Only a 202 proves the async path exists: a pre-RFC-0024 (N−1) router
	// does not know the header and proxies synchronously (200), and a HEAD
	// router with async disabled answers 501. Anything but 202 means there
	// is no queue to measure.
	asyncHeaders := http.Header{}
	asyncHeaders.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	code, perr := probeStatus(ctx, env.RouterURL()+poolRoute, asyncHeaders)
	if perr != nil {
		return res, perr
	}
	asyncEnabled := code == http.StatusAccepted
	const asyncBatch = 20
	if asyncEnabled {
		accepted := 1 // the probe above already enqueued one
		for range asyncBatch - 1 {
			if code, perr := probeStatus(ctx, env.RouterURL()+poolRoute, asyncHeaders); perr == nil && code == http.StatusAccepted {
				accepted++
			}
		}
		res.Add("async_enqueued_pre_upgrade", "count", report.Higher, float64(accepted))
	}

	// Constant-rate load on both functions for the whole choreography. The
	// open-loop duration is a generous ceiling; the run is ended by cancel
	// once the settle window closes, so loadgen Result rate/window fields are
	// not meaningful here — the classification counters and the latency
	// percentiles (computed from samples) are what this scenario reports.
	loadCtx, stopLoad := context.WithCancel(ctx)
	defer stopLoad()
	var pool, nd upgradeCounters
	mkTarget := func(route string, c *upgradeCounters) *loadgen.HTTPTarget {
		return loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
			URL:           env.RouterURL() + route,
			Concurrency:   u.rps,
			KeepAlive:     true,
			Timeout:       30 * time.Second,
			ObserveHeader: correlation.HeaderComponent,
			Observe:       c.observe,
		})
	}
	var wg sync.WaitGroup
	var poolResult, ndResult loadgen.Result
	wg.Add(2)
	go func() {
		defer wg.Done()
		poolResult = loadgen.RunOpenLoop(loadCtx, loadgen.OpenLoopConfig{
			Doer: mkTarget(poolRoute, &pool).Do, RPS: u.rps, Duration: 2 * time.Hour,
		})
	}()
	go func() {
		defer wg.Done()
		ndResult = loadgen.RunOpenLoop(loadCtx, loadgen.OpenLoopConfig{
			Doer: mkTarget(ndRoute, &nd).Do, RPS: u.rps, Duration: 2 * time.Hour,
		})
	}()

	// Baseline window, then the upgrade itself, then rollout-complete, then
	// the settle window — load runs through all of it.
	if err := sleepCtx(ctx, u.baseline); err != nil {
		stopLoad()
		wg.Wait()
		return res, err
	}
	// Generations before the upgrade: the rollout wait asserts against them
	// that the upgrade actually changed something — a silently-ignored
	// values key would otherwise produce a perfect score for an upgrade
	// that never happened.
	gensBefore, err := deploymentGenerations(ctx, env)
	if err != nil {
		stopLoad()
		wg.Wait()
		return res, fmt.Errorf("snapshot deployment generations: %w", err)
	}
	upgradeStart := time.Now()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", u.cmd)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		stopLoad()
		wg.Wait()
		return res, fmt.Errorf("upgrade command failed: %w", err)
	}
	if err := waitControlPlaneRolled(ctx, env, gensBefore, u.timeout); err != nil {
		stopLoad()
		wg.Wait()
		return res, err
	}
	res.Add("upgrade_duration_seconds", "s", report.Lower, time.Since(upgradeStart).Seconds())
	if err := sleepCtx(ctx, u.settle); err != nil {
		stopLoad()
		wg.Wait()
		return res, err
	}
	// Stop counting before cancelling: requests in flight at cancel fail
	// with context.Canceled, which would otherwise read as transport
	// failures — the exact bucket the error budget gates on.
	pool.closing.Store(true)
	nd.closing.Store(true)
	stopLoad()
	wg.Wait()

	pool.add(&res, "poolmgr_")
	nd.add(&res, "newdeploy_")
	if poolResult.Total > poolResult.Errors {
		res.Add("poolmgr_p99", "ms", report.Lower, millis(poolResult.P99))
	}
	if ndResult.Total > ndResult.Errors {
		res.Add("newdeploy_p99", "ms", report.Lower, millis(ndResult.P99))
	}

	// Warm survival: every pre-upgrade specialized pod UID must still exist.
	specializedAfter, err := specializedPodUIDs(ctx, env, poolFn)
	if err != nil {
		return res, fmt.Errorf("re-list specialized pods: %w", err)
	}
	survived := 1.0
	for uid := range specializedBefore {
		if _, ok := specializedAfter[uid]; !ok {
			survived = 0
			break
		}
	}
	res.Add("warm_pods_before", "count", report.Higher, float64(len(specializedBefore)))
	res.Add("warm_pod_survival", "bool", report.Higher, survived)

	if asyncEnabled {
		// The async path must still accept work after the roll…
		if code, perr := probeStatus(ctx, env.RouterURL()+poolRoute, asyncHeaders); perr == nil && code == http.StatusAccepted {
			res.Add("async_accepts_post_upgrade", "bool", report.Higher, 1)
		} else {
			res.Add("async_accepts_post_upgrade", "bool", report.Higher, 0)
		}
		// …and the pre-upgrade batch must drain. Best-effort: needs the
		// queue-depth gauge, so it is attempted only when Prometheus is
		// wired (asyncDrainSeconds itself would otherwise poll its full
		// timeout against a capturer that always answers not-found).
		if env.Capturer.PrometheusEnabled() {
			if secs, ok := asyncDrainSeconds(ctx, env, 3*time.Minute); ok {
				res.Add("async_drain_after_upgrade_seconds", "s", report.Lower, secs)
			}
		}
	}
	return res, nil
}

// specializedPodUIDs returns the UID set of the function's specialized
// (managed=false) pods across namespaces.
func specializedPodUIDs(ctx context.Context, env *harness.Env, fnName string) (map[string]struct{}, error) {
	selector := labels.SelectorFromSet(labels.Set{
		fv1.FUNCTION_NAME: fnName,
		fv1.MANAGED:       "false",
	}).String()
	pods, err := env.Clients.Kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	uids := make(map[string]struct{}, len(pods.Items))
	for i := range pods.Items {
		uids[string(pods.Items[i].UID)] = struct{}{}
	}
	return uids, nil
}

// deploymentGenerations snapshots metadata.generation for every Deployment in
// the Fission namespace.
func deploymentGenerations(ctx context.Context, env *harness.Env) (map[string]int64, error) {
	deps, err := env.Clients.Kube.AppsV1().Deployments(env.FissionNamespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	gens := make(map[string]int64, len(deps.Items))
	for i := range deps.Items {
		gens[deps.Items[i].Name] = deps.Items[i].Generation
	}
	return gens, nil
}

// waitControlPlaneRolled polls every Deployment in the Fission namespace until
// its status has converged on the current generation with full availability —
// i.e. the helm upgrade's rollout is complete. It additionally requires that
// at least one Deployment's generation moved past its pre-upgrade snapshot:
// a converged-but-unchanged control plane means the upgrade was a no-op (for
// example a silently-ignored values key), and a perfect score for an upgrade
// that never happened is the one result this scenario must never report.
func waitControlPlaneRolled(ctx context.Context, env *harness.Env, gensBefore map[string]int64, timeout time.Duration) error {
	// Fast no-op detection first: helm has already applied the manifests by
	// the time this runs, so a genuine upgrade shows a moved generation (or
	// a new Deployment) almost immediately.
	err := harness.Poll(ctx, 30*time.Second, 2*time.Second, func(ctx context.Context) (bool, error) {
		deps, err := env.Clients.Kube.AppsV1().Deployments(env.FissionNamespace()).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		for i := range deps.Items {
			if before, ok := gensBefore[deps.Items[i].Name]; !ok || deps.Items[i].Generation > before {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("control plane never changed: no Deployment generation moved past its pre-upgrade value — the upgrade was a no-op (check the helm values actually reach the chart): %w", err)
	}
	return harness.Poll(ctx, timeout, 5*time.Second, func(ctx context.Context) (bool, error) {
		deps, err := env.Clients.Kube.AppsV1().Deployments(env.FissionNamespace()).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil // transient apiserver blips are part of the window
		}
		if len(deps.Items) == 0 {
			return false, nil
		}
		for i := range deps.Items {
			d := &deps.Items[i]
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			if d.Status.ObservedGeneration < d.Generation ||
				d.Status.UpdatedReplicas < replicas ||
				d.Status.AvailableReplicas < replicas ||
				d.Status.UnavailableReplicas > 0 {
				return false, nil
			}
		}
		return true, nil
	})
}

// sleepCtx blocks for d or until ctx is done (returning its error).
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
