//go:build integration

package framework

import (
	"context"
	"strings"
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

// WaitForRuntimePodReady polls until at least one runtime (poolmgr) pod for
// the env is Ready. Used by tests that need to know the warm pool has
// started — note this is NOT a prerequisite for source builds; the
// buildermgr only talks to the builder pod, not the runtime pool.
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
		for _, p := range pods.Items {
			if isPodReady(&p) {
				return
			}
		}
		assert.Failf(c, "no Ready runtime pod yet",
			"env %q in ns %q (selector environmentName=%s)", envName, ns.Name, envName)
	}, 3*time.Minute, 2*time.Second)
}

// waitForBuilderEndpointReady polls until the env's builder Service has at
// least one Ready endpoint published to its EndpointSlice. The builder
// Service is named `<env>-<env.ResourceVersion>` (see
// pkg/buildermgr/envwatcher.go createBuilderService) and selects builder
// pods via envName=<env>. The buildermgr POSTs to this Service on port 8000
// (fetcher) and 8001 (builder); if the EndpointSlice hasn't been published
// yet — kube-proxy lags pod readiness by a few hundred ms to seconds — the
// dial fails with "dial tcp ...:8000: i/o timeout".
//
// We don't know the env.ResourceVersion at this layer, so we list slices
// in the namespace and match by `kubernetes.io/service-name` starting with
// `<envName>-`. Test ID suffixes guarantee uniqueness so this won't
// false-match a sibling test's env.
func (ns *TestNamespace) waitForBuilderEndpointReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		slices, err := ns.f.kubeClient.DiscoveryV1().EndpointSlices(ns.Name).List(ctx, metav1.ListOptions{})
		if !assert.NoErrorf(c, err, "list endpointslices in ns %q", ns.Name) {
			return
		}
		var ready, matched int
		for _, sl := range slices.Items {
			svc := sl.Labels["kubernetes.io/service-name"]
			if !strings.HasPrefix(svc, envName+"-") {
				continue
			}
			matched++
			for _, ep := range sl.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					ready += len(ep.Addresses)
				}
			}
		}
		if !assert.Greaterf(c, matched, 0,
			"no EndpointSlice yet for env %q builder service in ns %q", envName, ns.Name) {
			return
		}
		assert.GreaterOrEqualf(c, ready, 1,
			"env %q builder service has %d ready endpoints across %d slices; need ≥1",
			envName, ready, matched)
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
