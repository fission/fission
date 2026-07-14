// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestProvisionedConcurrencyExecutorRestart proves a fresh executor re-derives
// the provisioned-concurrency floor after both warm pods are deleted and the
// executor is restarted. Provisioned pods are independent of the executor
// rollout (chart default RollingUpdate), so the test must DELETE them to
// force re-derivation — restart alone would leave them running.
//
// Lives in the serial suite because restarting the shared executor is
// incompatible with the parallel common suite.
func TestProvisionedConcurrencyExecutorRestart(t *testing.T) {
	// Deliberately NOT t.Parallel(): restarts the shared executor.

	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	if !f.ExecutorEnvEnabled(t, ctx, "EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED") {
		t.Skip("EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED not set on executor")
	}
	if !f.ExecutorFunctionServicesEnabled(t, ctx) {
		t.Skip("ENABLE_FUNCTION_SERVICES not set on executor")
	}

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pcrs-" + ns.ID
	fnName := "fn-pcrs-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
		Poolsize: 4,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType:           "poolmgr",
		ProvisionedConcurrency: 2,
		IdleTimeout:            30,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	ns.WaitForPoolDeployment(t, ctx, envName, func(d *appsv1.Deployment) bool {
		return d.Status.ReadyReplicas == 4
	}, "4 ready replicas", 2*time.Minute)

	// Floor established before restart.
	t.Log("establishing provisioned floor before restart")
	ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 3*time.Minute)
	ns.WaitForProvisionedStatus(t, ctx, fnName, 2, 2, 90*time.Second)

	// Delete BOTH provisioned pods. Restart alone doesn't touch them
	// (they're independent of the executor rollout), so deletion is what
	// forces the fresh executor to re-derive.
	t.Log("deleting both provisioned pods")
	pods := ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 30*time.Second)
	require.Len(t, pods, 2, "expected exactly 2 provisioned pods to delete")
	for _, p := range pods {
		require.NoErrorf(t, f.KubeClient().CoreV1().Pods(ns.Name).Delete(ctx, p.Name, metav1.DeleteOptions{}),
			"delete provisioned pod %q", p.Name)
	}

	// Restart the executor with a throwaway nonce env var. SetExecutorEnv
	// force-patches Recreate strategy so the new pod starts only after the
	// old one terminates — clean single-instance restart.
	t.Log("restarting executor")
	gen, restore := f.SetExecutorEnv(t, ctx, "PROVISIONER_RESTART_NONCE", fmt.Sprintf("%d", time.Now().UnixNano()))
	t.Cleanup(restore)
	f.WaitForExecutorRollout(t, ctx, gen, 3*time.Minute)

	// Fresh executor re-derives the floor via its periodic reconcile tick.
	t.Log("waiting for fresh executor to re-derive provisioned floor")
	ns.WaitForProvisionedPodsAtLeast(t, ctx, fnName, 2, 3*time.Minute)
	ns.WaitForProvisionedStatus(t, ctx, fnName, 2, 2, 2*time.Minute)
}
