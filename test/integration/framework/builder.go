//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WaitForBuilderReady polls for the builder pod attached to envName to be
// Ready. The pod carries the `envName` label set by the buildermgr; this
// matches the bash `wait_for_builder` helper.
func (ns *TestNamespace) WaitForBuilderReady(t *testing.T, ctx context.Context, envName string) {
	t.Helper()
	Eventually(t, ctx, 3*time.Minute, 2*time.Second, func(c context.Context) (bool, error) {
		pods, err := ns.f.kubeClient.CoreV1().Pods(ns.Name).List(c, metav1.ListOptions{
			LabelSelector: "envName=" + envName,
		})
		if err != nil {
			return false, err
		}
		for _, p := range pods.Items {
			if isPodReady(&p) {
				return true, nil
			}
		}
		return false, nil
	}, "builder pod for env %q never became Ready in namespace %q", envName, ns.Name)
}

func isPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
