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
	"k8s.io/client-go/kubernetes"

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

// upgradeCounters tallies request outcomes for the error budget; safe for
// concurrent use. It holds a bucketCounts — the same shape the recorder's
// timeline cells use — and both consume classifyOutcome, so the numbers the
// budget gates on and the timeline that explains them agree by construction
// rather than by a parity test.
//
// closing is set immediately before the load context is cancelled at the end
// of the settle window: requests still in flight at that moment fail with
// context.Canceled — a shutdown artifact of the measurement, not a platform
// failure — and must not pollute the transport-failure bucket the error
// budget gates on.
type upgradeCounters struct {
	closing atomic.Bool

	mu     sync.Mutex
	counts bucketCounts
}

// observe tallies one request outcome and reports whether it counted. A false
// return means the measurement is already closing, and the caller must not
// record the observation anywhere else either — one closing decision owns
// every sink.
func (c *upgradeCounters) observe(o loadgen.Observation) bool {
	if c.closing.Load() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts.add(classifyOutcome(o))
	return true
}

func (c *upgradeCounters) addTo(res *report.ScenarioResult, prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// requests is derived, not separately incremented: every counted
	// observation adds exactly 1 to exactly one bucket.
	res.Add(prefix+"requests", "count", report.Higher, float64(c.counts.OK+c.counts.failures()))
	res.Add(prefix+"ok", "count", report.Higher, float64(c.counts.OK))
	// The three failure buckets the pass criteria are written against.
	res.Add(prefix+"transport_failures", "count", report.Lower, float64(c.counts.Transport))
	res.Add(prefix+"platform_attributed", "count", report.Lower, float64(c.counts.Attributed))
	res.Add(prefix+"unattributed_errors", "count", report.Lower, float64(c.counts.Unattributed))
}

func (u *upgradeUnderLoad) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// Fixtures on the still-old control plane. The poolmgr route also takes
	// POST so the async batch can reuse it.
	methods := []string{http.MethodGet, http.MethodPost}
	poolRoute, poolFn, err := provisionWarmFunctionNamed(ctx, sc, "pm-", fv1.ExecutorTypePoolmgr, runtimeNode, u.poolsize, 500, methods)
	if err != nil {
		return res, err
	}
	ndRoute, ndFn, err := provisionWarmFunctionNamed(ctx, sc, "nd-", fv1.ExecutorTypeNewdeploy, runtimeNode, u.poolsize, 500, methods)
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
	// The recorder timestamps every failure and cluster transition relative
	// to load start, so a nonzero counter is attributable to a specific
	// window of the roll instead of being a guess.
	rec := newUpgradeRecorder(time.Now())
	waitPodWatchers := watchPodLifecycles(loadCtx, env.Clients.Kube, env.FissionNamespace(), rec)
	var pool, nd upgradeCounters
	mkTarget := func(route, name string, c *upgradeCounters) *loadgen.HTTPTarget {
		return loadgen.NewHTTPTarget(loadgen.HTTPTargetConfig{
			URL:           env.RouterURL() + route,
			Concurrency:   u.rps,
			KeepAlive:     true,
			Timeout:       30 * time.Second,
			ObserveHeader: correlation.HeaderComponent,
			// One closing check, one owner: a second independent load of
			// the flag could flip between the two sinks and leave the
			// timeline disagreeing with the counters about the last request.
			Observe: func(o loadgen.Observation) {
				if c.observe(o) {
					rec.observe(name, o)
				}
			},
		})
	}
	var wg sync.WaitGroup
	var poolResult, ndResult loadgen.Result
	wg.Add(2)
	go func() {
		defer wg.Done()
		poolResult = loadgen.RunOpenLoop(loadCtx, loadgen.OpenLoopConfig{
			Doer: mkTarget(poolRoute, "poolmgr", &pool).Do, RPS: u.rps, Duration: 2 * time.Hour,
		})
	}()
	go func() {
		defer wg.Done()
		ndResult = loadgen.RunOpenLoop(loadCtx, loadgen.OpenLoopConfig{
			Doer: mkTarget(ndRoute, "newdeploy", &nd).Do, RPS: u.rps, Duration: 2 * time.Hour,
		})
	}()
	rec.mark("load-start")
	// fail is the one early-exit teardown: stop the load, join the drivers,
	// return. Five hand-copied stop/wait/return triplets is how a sixth early
	// return forgets one.
	fail := func(err error) (report.ScenarioResult, error) {
		stopLoad()
		wg.Wait()
		return res, err
	}

	// Baseline window, then the upgrade itself, then rollout-complete, then
	// the settle window — load runs through all of it.
	if err := sleepCtx(ctx, u.baseline); err != nil {
		return fail(err)
	}
	// Generations before the upgrade: the rollout wait asserts against them
	// that the upgrade actually changed something — a silently-ignored
	// values key would otherwise produce a perfect score for an upgrade
	// that never happened.
	gensBefore, err := deploymentGenerations(ctx, env)
	if err != nil {
		return fail(fmt.Errorf("snapshot deployment generations: %w", err))
	}
	upgradeStart := time.Now()
	rec.mark("upgrade-cmd-start")
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", u.cmd)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fail(fmt.Errorf("upgrade command failed: %w", err))
	}
	rec.mark("upgrade-cmd-done")
	if err := waitControlPlaneRolled(ctx, env.Clients.Kube, env.FissionNamespace(), gensBefore, u.timeout, rec.deployEvent); err != nil {
		return fail(err)
	}
	rec.mark("rollout-converged")
	res.Add("upgrade_duration_seconds", "s", report.Lower, time.Since(upgradeStart).Seconds())
	if err := sleepCtx(ctx, u.settle); err != nil {
		return fail(err)
	}
	rec.mark("settle-end")
	// Stop counting before cancelling: requests in flight at cancel fail
	// with context.Canceled, which would otherwise read as transport
	// failures — the exact bucket the error budget gates on.
	pool.closing.Store(true)
	nd.closing.Store(true)
	stopLoad()
	wg.Wait()
	// Join the pod watchers before reading the recorder: a straggler past the
	// event cap writes the dropped map, and marshalling that map concurrently
	// is a fatal throw. loadCtx is already cancelled by stopLoad.
	waitPodWatchers()

	// The attribution outputs: sidecar artifact (when --artifact-dir is set),
	// phase markers in Meta, and the human timeline on the job log. All
	// best-effort — attribution must never fail the measurement.
	rec.writeArtifact(env.ArtifactDir)
	rec.logSummary(os.Stderr, []string{"poolmgr", "newdeploy"})
	rec.metaInto(&res)

	pool.addTo(&res, "poolmgr_")
	nd.addTo(&res, "newdeploy_")
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
//
// record, when non-nil, receives per-Deployment transitions from the existing
// poll: "rolled" (generation moved, once), then "converged"/"regressed" edges
// as availability flips — the rollout order and timing are what let a failure
// window in the timeline be pinned to the component that was rolling through
// it. converged must NOT latch: a Deployment that converges, loses a pod, and
// re-converges would otherwise have its failure window pinned to a control
// plane the timeline claims was already settled — the exact misattribution
// this exists to prevent. Deployments helm did not change never emit
// converged/regressed at all; they were available the whole time, and folding
// them in would dilute the rollout sequence with components that never moved.
func waitControlPlaneRolled(ctx context.Context, kube kubernetes.Interface, fissionNS string, gensBefore map[string]int64, timeout time.Duration, record func(dep, event string)) error {
	if record == nil {
		record = func(string, string) {}
	}
	rolled := map[string]bool{}
	converged := map[string]bool{}
	noteRolled := func(dep string) {
		if !rolled[dep] {
			rolled[dep] = true
			record(dep, "rolled")
		}
	}
	noteAvailability := func(dep string, isConverged bool) {
		if !rolled[dep] {
			return
		}
		if isConverged != converged[dep] {
			converged[dep] = isConverged
			if isConverged {
				record(dep, "converged")
			} else {
				record(dep, "regressed")
			}
		}
	}
	// Fast no-op detection first: helm has already applied the manifests by
	// the time this runs, so a genuine upgrade shows a moved generation (or
	// a new Deployment) almost immediately.
	err := harness.Poll(ctx, 30*time.Second, 2*time.Second, func(ctx context.Context) (bool, error) {
		deps, err := kube.AppsV1().Deployments(fissionNS).List(ctx, metav1.ListOptions{})
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
		deps, err := kube.AppsV1().Deployments(fissionNS).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil // transient apiserver blips are part of the window
		}
		if len(deps.Items) == 0 {
			return false, nil
		}
		allConverged := true
		for i := range deps.Items {
			d := &deps.Items[i]
			if before, ok := gensBefore[d.Name]; !ok || d.Generation > before {
				noteRolled(d.Name)
			}
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			depConverged := d.Status.ObservedGeneration >= d.Generation &&
				d.Status.UpdatedReplicas >= replicas &&
				d.Status.AvailableReplicas >= replicas &&
				d.Status.UnavailableReplicas == 0
			noteAvailability(d.Name, depConverged)
			if !depConverged {
				allConverged = false
			}
		}
		return allConverged, nil
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
