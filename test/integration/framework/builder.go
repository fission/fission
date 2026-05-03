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
// We then also poll EndpointSlices for the env (label
// kubernetes.io/service-name selects them) until the union of Ready
// addresses reaches minPods. Pod readiness gates pass before kube-proxy
// publishes endpoints, and the buildermgr POSTs to the Service — which
// is `<envName>-<port>` not `<envName>` — so checking by Service-name is
// awkward. EndpointSlices carry the service-name as a label and let us
// match by label selector instead.
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

	// EndpointSlice readiness check. EndpointSlices for the env's Service
	// carry kubernetes.io/service-name=<svc-name> — but the svc name has a
	// port-hash suffix (e.g. "python-v2-abcdef-3317"). Easiest: list slices
	// in the namespace and pick those whose service-name *starts with*
	// envName + "-".
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		slices, err := ns.f.kubeClient.DiscoveryV1().EndpointSlices(ns.Name).List(ctx, metav1.ListOptions{})
		if !assert.NoErrorf(c, err, "list endpointslices in ns %q", ns.Name) {
			return
		}
		var ready int
		var matched int
		for _, sl := range slices.Items {
			svc := sl.Labels["kubernetes.io/service-name"]
			if !strings.HasPrefix(svc, envName+"-") && svc != envName {
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
			"no EndpointSlice yet for env %q service in ns %q", envName, ns.Name) {
			return
		}
		assert.GreaterOrEqualf(c, ready, minPods,
			"env %q service has %d ready endpoint addresses across %d slices; need ≥%d",
			envName, ready, matched, minPods)
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
