// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"

	"github.com/fission/fission/test/integration/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestProvisionedConcurrencyCLIRoundTrip verifies the --provisioned-concurrency
// CLI flag persists to the Function CR's Spec.ProvisionedConcurrency.Target
// field across create and update. No traffic, no provisioner gate — pure
// CLI→CR round-trip. (fn get renders no provisioned-concurrency field, so
// assertions are on the CR via GetFunction, concern #9.)
func TestProvisionedConcurrencyCLIRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pcrt-" + ns.ID
	fnName := "fn-pcrt-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ProvisionedConcurrency: 3,
		ExecutorType:           "poolmgr",
	})
	ns.WaitForFunction(t, ctx, fnName)

	// Create: CR field reflects --provisioned-concurrency 3
	fn := ns.GetFunction(t, ctx, fnName)
	require.NotNil(t, fn.Spec.ProvisionedConcurrency, "ProvisionedConcurrency config not set")
	require.Equal(t, 3, fn.Spec.ProvisionedConcurrency.Target, "ProvisionedConcurrency.Target mismatch after create")

	// update to 5 via cli
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--provisioned-concurrency", "5")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn := ns.GetFunction(t, ctx, fnName)
		assert.NotNil(c, fn.Spec.ProvisionedConcurrency)
		if fn.Spec.ProvisionedConcurrency == nil {
			return
		}
		assert.Equal(c, 5, fn.Spec.ProvisionedConcurrency.Target, "ProvisionedConcurrency.Target mismatch after update")
	}, 3*time.Minute, 500*time.Millisecond)
}

// TestProvisionedConcurrencyRejectsNewdeploy verifies the CEL validation
// rejects --provisioned-concurrency on a non-poolmgr executor. The CRD
// schema enforces "provisionedConcurrency is only supported with
// executortype poolmgr" (crds/v1/fission.io_functions.yaml).
func TestProvisionedConcurrencyRejectsNewdeploy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pcrj-" + ns.ID
	fnName := "fn-pcrj-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	// CLI should reject: provisionedConcurrency + newdeploy.
	out, err := ns.CLIExpectError(
		t, ctx,
		"fn", "create", "--name", fnName, "--env", envName, "--code", codePath,
		"--executortype", "newdeploy",
		"--provisioned-concurrency", "2",
		"--minscale", "1", "--maxscale", "2",
	)
	require.Error(t, err, "expected CEL rejection, got success")
	assert.Contains(t, out+err.Error(), "provisionedConcurrency is only supported with executortype")
}

// TestProvisionedConcurrencyRejectsWindows verifies the admission webhook
// rejects scheduled windows on a provisioned-concurrency function. Windows
// are RFC-0026 PR 2; the webhook returns "scheduled windows are not yet
// supported" (validation.go). No CLI flag for windows, so the update is
// done via the typed clientset.
func TestProvisionedConcurrencyRejectsWindows(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pcrw-" + ns.ID
	fnName := "fn-pcrw-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType:           "poolmgr",
		ProvisionedConcurrency: 2,
	})
	ns.WaitForFunction(t, ctx, fnName)

	// Fetch the live CR, add a scheduled window, attempt update — webhook
	// must reject. Retry on conflict: the provisioner may write status
	// (bumping resourceVersion) between our Get and Update, causing a
	// stale-version conflict that masks the webhook error.
	var err error
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn := ns.GetFunction(t, ctx, fnName)
		fn.Spec.ProvisionedConcurrency.Windows = []fv1.ProvisionedWindow{{
			Name:     "w1",
			Start:    "0 0 * * *",
			Duration: "1h",
			Target:   1,
		}}
		_, err = f.FissionClient().CoreV1().Functions(ns.Name).Update(ctx, fn, metav1.UpdateOptions{})
		// Conflict = retry (provisioner raced us). Webhook error = done.
		if apierrors.IsConflict(err) {
			return // keep polling
		}
		assert.Error(c, err, "expected webhook rejection of scheduled windows")
		assert.Contains(c, err.Error(), "scheduled windows are not yet supported")
	}, 2*time.Minute, 500*time.Millisecond)
}

// TestProvisionedConcurrencyLifecycle exercises the RFC-0026 PR1 provisioner
// end-to-end: floor establishment, reaper exemption, self-heal after pod
// delete, target drop via CLI (generation refresh), and clean disable.
// One function, five sequential phases. t.Parallel-safe (own namespace ID).
func TestProvisionedConcurrencyLifecycle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	if !f.ExecutorEnvEnabled(t, ctx, "EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED") {
		t.Skip("EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED not set on executor")
	}
	if !f.ExecutorFunctionServicesEnabled(t, ctx) {
		t.Skip("EXECUTOR_FUNCTION_SERVICES not set on executor")
	}

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pcl-" + ns.ID
	fnName := "fn-pcl-" + ns.ID
	ctlName := "fn-pcl-ctl-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
		Poolsize: 4, // >= target +2 so provisioner does not starve the pool
	})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")

	// provisioned function: poolmgr, target=2, idle timeout 30s.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType:           "poolmgr",
		ProvisionedConcurrency: 2,
		IdleTimeout:            30,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{
		Function: fnName,
		URL:      "/" + fnName,
		Method:   "GET",
	})
	// Control function: poolmgr, NO provisioned concurrency, idle timeout 30s.
	// Used in Phase 2 to prove the reaper is live (control gets reaped,
	// provisioned fn does not).
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: ctlName, Env: envName, Code: codePath,
		ExecutorType: "poolmgr",
		IdleTimeout:  30,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: ctlName, URL: "/" + ctlName, Method: "GET"})
	// Wait for env pool to be ready (4 pods).
	ns.WaitForPoolDeployment(t, ctx, envName, func(d *appsv1.Deployment) bool {
		return d.Status.ReadyReplicas == 4
	}, "4 ready replicas", 5*time.Minute)

	// Phase 1: floor established with no invocations.
	t.Log("Phase 1: floor@0 — provisioner warms 2 pods without traffic")
	ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 5*time.Minute)
	ns.WaitForProvisionedStatus(t, ctx, fnName, 2, 2, 5*time.Minute)
	ns.WaitForFunctionConditionTrue(t, ctx, fnName, fv1.FunctionConditionProvisioned, 30*time.Second)
	// Warm smoke: the function serves.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))

	// Phase 2: reaper is live (control reaped) but provisioned floor holds.
	t.Log("Phase 2: reaper liveness — control reaped, provisioned floor holds")
	// Warm control once so it has a specialized pod to reap.
	f.Router(t).GetEventually(t, ctx, "/"+ctlName, framework.BodyContains("hello"))
	// Control reaped after idleTimeout(30s) + reaperTick(≤5s) ≈ 35s under
	// normal load. Under parallel CI (-parallel 6) the reaper can lag
	// several minutes (see TestIdleObjectsReaper skip note), so use a
	// generous 5 min window.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		count, err := ns.RunningFunctionPodCount(ctx, ctlName)
		if !assert.NoErrorf(c, err, "count control pods for %q", ctlName) {
			return
		}
		assert.Zerof(c, count, "control %q: want 0 pods (reaped), got %d", ctlName, count)
	}, 5*time.Minute, 5*time.Second)
	// Provisioned floor holds while reaper is active.
	ns.WaitForProvisionedStatus(t, ctx, fnName, 2, 2, 5*time.Minute)
	ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 5*time.Minute)

	// Phase 3: self-heal — delete provisioned pods until below target, then
	// verify the provisioner replaces them. We wait for convergence to target
	// first, then delete enough pods to drop below target (usually just one,
	// but handle transient overshoot by deleting all but target-1).
	t.Log("Phase 3: self-heal after pod delete")
	ns.WaitForProvisionedStatus(t, ctx, fnName, 2, 2, 5*time.Minute)
	beforePods := ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 5*time.Minute)
	require.GreaterOrEqual(t, len(beforePods), 2, "expected at least 2 provisioned pods before delete")
	// Delete pods until only target-1 remain, guaranteeing self-heal fires.
	toDelete := len(beforePods) - 1 // leave 1 pod, target is 2 → 1 < 2 triggers re-warm
	for i := 0; i < toDelete; i++ {
		require.NoErrorf(t, f.KubeClient().CoreV1().Pods(ns.Name).Delete(ctx, beforePods[i].Name, metav1.DeleteOptions{}),
			"delete provisioned pod %q", beforePods[i].Name)
	}
	// Provisioner re-warms via periodic tick (10s reconcile). Generous timeout
	// absorbs tick + specialization + pool refill (concern #8).
	afterPods := ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 5*time.Minute)
	require.GreaterOrEqual(t, len(afterPods), 2, "expected at least 2 provisioned pods after self-heal")
	// Replacement name differs — proves new pod, not resurrected.
	names := map[string]bool{}
	for _, p := range beforePods {
		names[p.Name] = true
	}
	var newName string
	for _, p := range afterPods {
		if !names[p.Name] {
			newName = p.Name
			break
		}
	}
	require.NotEmptyf(t, newName, "no new pod after self-heal (before=%v after=%v)",
		podNames(beforePods), podNames(afterPods))

	// Phase 4: target drop 2→1 via CLI = generation refresh.
	// A --provisioned-concurrency change bumps Generation; reconciler.go
	// refreshFuncPods recycles ALL old-gen specialized pods and the provisioner
	// re-warms fresh to the new target. CLI target drops converge via
	// generation refresh, not via the idle reaper.
	t.Log("Phase 4: target drop 2→1 via CLI (generation refresh)")
	oldPods := ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 5*time.Minute)
	require.NotEmpty(t, oldPods, "need a pod to read old generation")
	oldGen := oldPods[0].Labels[fv1.FUNCTION_GENERATION]
	require.NotEmptyf(t, oldGen, "old pod %q has no generation label", oldPods[0].Name)

	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--provisioned-concurrency", "1")
	ns.WaitForProvisionedStatus(t, ctx, fnName, 1, 1, 5*time.Minute)

	// converge to 1 total specialized pod (old-gen recycled, new-gen warmed)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		count, err := ns.RunningFunctionPodCount(ctx, fnName)
		if !assert.NoErrorf(c, err, "count specialized pods for %q", fnName) {
			return
		}
		assert.Equalf(c, 1, count, "function %q: want 1 specialized pod (new-gen), got %d", fnName, count)
	}, 90*time.Second, 5*time.Second)

	onePod := ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 1, 5*time.Minute)
	require.Len(t, onePod, 1, "expected exactly 1 provisioned pod after target drop")
	newGen := onePod[0].Labels[fv1.FUNCTION_GENERATION]
	require.NotEmptyf(t, newGen, "new pod %q has no generation label", onePod[0].Name)
	require.NotEqualf(t, oldGen, newGen, "generation did not change after target drop (old=%q new=%q)",
		oldGen, newGen)

	// Phase 5: disable (config→nil). The same update bumps Generation →
	// refreshFuncPods deletes the specialized pods, and config-nil means no
	// re-warm, so they disappear via generation refresh (not reaper timing).
	t.Log("Phase 5: disable — provisioned-concurrency 0")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--provisioned-concurrency", "0")

	ns.WaitForProvisionedStatus(t, ctx, fnName, 0, 0, 5*time.Minute)
	ns.WaitForNoProvisionedPods(t, ctx, fnName, 5*time.Minute)

	// Condition False with reason ProvisionedDisabled.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		conds := ns.GetFunctionConditions(t, ctx, fnName)
		cond := conditions.Find(conds, string(fv1.FunctionConditionProvisioned))
		if !assert.NotNilf(c, cond, "Provisioned condition not present") {
			return
		}
		assert.Equalf(c, metav1.ConditionFalse, cond.Status,
			"Provisioned condition: want False, got %s", cond.Status)
		assert.Equalf(c, fv1.FunctionReasonProvisionedDisabled, cond.Reason,
			"Provisioned condition reason: want %s, got %s", fv1.FunctionReasonProvisionedDisabled, cond.Reason)
	}, 5*time.Minute, 2*time.Second)

	// All specialized pods gone (generation refresh deleted, no re-warm).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		count, err := ns.RunningFunctionPodCount(ctx, fnName)
		if !assert.NoErrorf(c, err, "count pods for %q", fnName) {
			return
		}
		assert.Zerof(c, count, "function %q: want 0 pods after disable, got %d", fnName, count)
	}, 5*time.Minute, 5*time.Second)
}

func podNames(pods []corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, p := range pods {
		names = append(names, p.Name)
	}
	return names
}
