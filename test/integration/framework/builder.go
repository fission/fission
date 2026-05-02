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
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "envName=" + envName,
		})
		if !assert.NoError(c, err) {
			return
		}
		for _, p := range pods.Items {
			if isPodReady(&p) {
				return
			}
		}
		assert.Failf(c, "no Ready builder pod yet", "env %q in namespace %q", envName, ns.Name)
	}, 3*time.Minute, 2*time.Second)
}

func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
