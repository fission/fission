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
// env is Ready. Waiting for "any one" pod isn't enough because the buildermgr
// POSTs to the env's Kubernetes Service on port 8000, which round-robins
// across all pool pods. If even one pod is still in ContainerCreating the
// fetch can pick it and time out (`dial tcp ...:8000: i/o timeout`).
//
// Pods carry the `environmentName=<env>` label (separate from the legacy
// `envName=` label on builder pods).
func (ns *TestNamespace) WaitForRuntimePodReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "environmentName=" + envName,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotEmptyf(c, pods.Items, "no runtime pods yet for env %q", envName) {
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
