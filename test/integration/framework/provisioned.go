// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ReadyProvisionedPods lists ready+running pods for fnName that carry the
// provisioned label (fission.io/provisioned=true). These are the pods the
// RFC-0026 provisioner keeps warm and the idle reaper exempts. Returns the
// raw pod list — no t.Fatal, callers (waiters) decide what to assert.
func (ns *TestNamespace) ReadyProvisionedPods(ctx context.Context, fnName string) ([]corev1.Pod, error) {
	selector := fv1.FUNCTION_NAME + "=" + fnName + "," + fv1.PROVISIONED_LABEL + "=" + fv1.PROVISIONED_VALUE + "," + fv1.SERVED_LABEL + "=" + fv1.SERVED_VALUE
	pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	filteredPods := utils.ReadyAndRunningPodsFilter(pods)
	return filteredPods, nil
}

// WaitForProvisionedPodsAtLeast polls until fnName has at least want
// ready+running provisioned pods, then returns them. FLOOR (>=) check —
// use when asserting the provisioner keeps N pods warm (concern #4).
// Polls every 2s.
func (ns *TestNamespace) WaitForProvisionedPodsAtLeast(t *testing.T,
	ctx context.Context, fnName string, want int, timeout time.Duration) []corev1.Pod {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.ReadyProvisionedPods(ctx, fnName)
		if !assert.NoErrorf(c, err, "list provisioned pods for %q", fnName) {
			return
		}
		assert.GreaterOrEqualf(c, len(pods), want, "function %q: want >=%d provisioned pods, got %d", fnName, want, len(pods))
	}, timeout, 2*time.Second)
	// final fetch to return the pods (we know there are at least want)
	pods, err := ns.ReadyProvisionedPods(ctx, fnName)
	require.NoErrorf(t, err, "list provisioned pods for %q", fnName)
	return pods
}

// WaitForNoProvisionedPods polls until fnName has zero ready+running
// provisioned pods. Use after disabling provisioned concurrency
// (--provisioned-concurrency 0) to assert labels cleared + pods gone.
// Polls every 2s.
func (ns *TestNamespace) WaitForNoProvisionedPods(t *testing.T,
	ctx context.Context, fnName string, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.ReadyProvisionedPods(ctx, fnName)
		if !assert.NoErrorf(c, err, "list provisioned pods for %q", fnName) {
			return
		}
		assert.Zerof(c, len(pods), "function %q: want 0 provisioned pods, got %d", fnName, len(pods))
	}, timeout, 2*time.Second)
}

// WaitForProvisionedStatus polls until the Function's status reports
// ProvisionedReady == wantReady AND ProvisionedTarget == wantTarget.
// Exact match (concern #4) — proves the provisioner converged on the
// desired target, not just that pods exist. Polls every 2s.
func (ns *TestNamespace) WaitForProvisionedStatus(t *testing.T,
	ctx context.Context, fnName string, wantReady, wantTarget int, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get function %q", fnName) {
			return
		}
		assert.Equalf(c, wantReady, fn.Status.ProvisionedReady, "function %q: want ProvisionedReady=%d, got %d", fnName, wantReady, fn.Status.ProvisionedReady)
		assert.Equalf(c, wantTarget, fn.Status.ProvisionedTarget, "function %q: want ProvisionedTarget=%d, got %d", fnName, wantTarget, fn.Status.ProvisionedTarget)
	}, timeout, 2*time.Second)
}

// RunningFunctionPodCount returns the count of ready+running pods backing
// fnName (any specialized pod, provisioned or not). Use for reaper-liveness
// checks (control function reaped → count 0) and total-pod convergence after
// a target drop or disable. Returns (count, error) — no t.Fatal so callers
// can wrap in EventuallyWithT.
func (ns *TestNamespace) RunningFunctionPodCount(ctx context.Context, fnName string) (int, error) {
	selector := fv1.FUNCTION_NAME + "=" + fnName + "," + fv1.SERVED_LABEL + "=" + fv1.SERVED_VALUE
	pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return 0, err
	}
	filteredPods := utils.ReadyAndRunningPodsFilter(pods)
	return len(filteredPods), nil
}
