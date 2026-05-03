//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WaitForBuilderReady polls for the builder pod attached to envName to be
// Ready. The pod carries the `envName` label set by the buildermgr; this
// matches the bash `wait_for_builder` helper.
func (ns *TestNamespace) WaitForBuilderReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	waitForReadyPod(t, ctx, ns, "envName="+envName, "builder", envName, 3*time.Minute)
}

// WaitForRuntimePodReady polls until *every* runtime (poolmgr) pod for the
// env is Ready. Used by tests that don't go through CreateEnv (e.g. those
// that bypass the framework env helper). Defaults to a single-pod wait;
// when the env was created with a larger pool, prefer waitForRuntimePoolReady
// (called automatically by CreateEnv) which knows the expected count.
//
// Pods carry the `environmentName=<env>` label (separate from the legacy
// `envName=` label on builder pods).
func (ns *TestNamespace) WaitForRuntimePodReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	ns.waitForRuntimePoolReady(t, ctx, envName, 1)
}

// waitForRuntimePoolReady polls until at least `minPods` runtime pods for
// envName exist AND every one of them is Ready. The min-count guard matters
// because the buildermgr POSTs to the env's K8s Service on port 8000, which
// round-robins across all pool pods. If only one of poolsize pods is Ready
// when the build starts, the round-robin can land on a still-ContainerCreating
// pod and the fetcher call times out (`dial tcp ...:8000: i/o timeout`).
//
// After all pods are Ready we also wait until the env's Service Endpoints
// reflects them (kube-proxy can take a couple of polling intervals to
// notice a new Ready pod), otherwise the buildermgr's POST to the Service
// can still race a stale endpoints list.
func (ns *TestNamespace) waitForRuntimePoolReady(t *testing.T, ctx context.Context, envName string, minPods int) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "environmentName=" + envName,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.GreaterOrEqualf(c, len(pods.Items), minPods,
			"expected ≥%d runtime pods for env %q, got %d", minPods, envName, len(pods.Items)) {
			return
		}
		for _, p := range pods.Items {
			if !isPodReady(&p) {
				assert.Failf(c, "runtime pod not ready",
					"env %q pod %s/%s phase=%s", envName, ns.Name, p.Name, p.Status.Phase)
				return
			}
		}
	}, 3*time.Minute, 2*time.Second)

	// Wait for the env's Service to have ≥ minPods Ready endpoint addresses.
	// The Service is named after the env (e.g. "python-v2-abcdef"); its
	// Endpoints object lists the pod IPs whose readiness gates have passed
	// from kube-proxy's perspective. Useful belt-and-suspenders against the
	// "pod Ready but Service hasn't propagated" race.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ep, err := ns.f.kubeClient.CoreV1().Endpoints(ns.Name).Get(ctx, envName, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get endpoints %q in ns %q", envName, ns.Name) {
			return
		}
		var ready int
		for _, sub := range ep.Subsets {
			ready += len(sub.Addresses)
		}
		assert.GreaterOrEqualf(c, ready, minPods,
			"env %q service has %d ready endpoints; need ≥%d", envName, ready, minPods)
	}, 90*time.Second, 1*time.Second)
}

// WaitForEnvReady waits for both the builder and runtime pods of envName.
// Combines WaitForBuilderReady + WaitForRuntimePodReady. Use this in tests
// that immediately follow with a source-archive package build.
func (ns *TestNamespace) WaitForEnvReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	ns.WaitForBuilderReady(t, ctx, envName)
	ns.WaitForRuntimePodReady(t, ctx, envName)
}

func waitForReadyPod(t *testing.T, ctx context.Context, ns *TestNamespace, selector, kind, envName string, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if !assert.NoError(c, err) {
			return
		}
		for _, p := range pods.Items {
			if isPodReady(&p) {
				return
			}
		}
		assert.Failf(c, "no Ready "+kind+" pod yet", "env %q in namespace %q (selector %q)", envName, ns.Name, selector)
	}, timeout, 2*time.Second)
}

func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
