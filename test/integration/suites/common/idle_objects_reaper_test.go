//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestIdleObjectsReaper is the Go port of test_fn_update/test_idle_objects_reaper.sh.
//
// Two functions on the same env: one newdeploy (minScale=0) and one
// poolmgr. Drive a single request through each, then wait long enough for
// the executor's idle-fsvc reaper to scale them down — newdeploy's
// Deployment.Spec.Replicas should drop to 0 and the poolmgr's running pods
// for the function should drop to 0. After re-invoking, both should scale
// back up: newdeploy to 1 (minScale=0 → bump to 1 on demand), poolmgr to
// 1 specialized runtime pod.
//
// The bash version sleeps 300s; the executor's LIST_OLD threshold is ~2 min,
// so 5 min is the worst-case wait for both reap-and-observe.
func TestIdleObjectsReaper(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-idle-" + ns.ID
	fnND := "fn-idle-nd-" + ns.ID
	fnGPM := "fn-idle-gpm-" + ns.ID

	// --period 5: faster reconciliation so the env-pod-side bookkeeping
	// runs more often. The reaper itself is driven by the executor's
	// own ticker; this just tightens the controller-side loop.
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime, Period: 5})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnND, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 0, MaxScale: 2,
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnGPM, Env: envName, Code: codePath,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "GET"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnGPM, URL: "/" + fnGPM, Method: "GET"})

	f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("world"))
	f.Router(t).GetEventually(t, ctx, "/"+fnGPM, framework.BodyContains("world"))

	t.Logf("waiting for idle-pod reaper (~5 min)")
	select {
	case <-time.After(5 * time.Minute):
	case <-ctx.Done():
		t.Fatalf("ctx cancelled while waiting for reaper: %v", ctx.Err())
	}

	// newdeploy: Deployment.Spec.Replicas should be 0.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		deps, err := f.KubeClient().AppsV1().Deployments(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "functionName=" + fnND,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotEmptyf(c, deps.Items, "no Deployment found for fn %q", fnND) {
			return
		}
		got := deps.Items[0].Spec.Replicas
		if assert.NotNilf(c, got, "Deployment %q has nil Spec.Replicas", deps.Items[0].Name) {
			assert.EqualValuesf(c, 0, *got,
				"newdeploy fn %q expected 0 replicas after reap, got %d", fnND, *got)
		}
	}, 3*time.Minute, 5*time.Second)

	// poolmgr: no Running pods for the function should remain.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "functionName=" + fnGPM,
		})
		if !assert.NoError(c, err) {
			return
		}
		var running int
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning {
				running++
			}
		}
		assert.Equalf(c, 0, running,
			"poolmgr fn %q expected 0 Running pods after reap, got %d", fnGPM, running)
	}, 3*time.Minute, 5*time.Second)

	// Re-invoke triggers scale-up. newdeploy goes 0→1, poolmgr specializes
	// a fresh pod from the warm pool.
	f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("world"))
	f.Router(t).GetEventually(t, ctx, "/"+fnGPM, framework.BodyContains("world"))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		deps, err := f.KubeClient().AppsV1().Deployments(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "functionName=" + fnND,
		})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotEmptyf(c, deps.Items, "no Deployment found for fn %q", fnND) {
			return
		}
		got := deps.Items[0].Spec.Replicas
		if assert.NotNilf(c, got, "Deployment %q has nil Spec.Replicas", deps.Items[0].Name) {
			assert.EqualValuesf(c, 1, *got,
				"newdeploy fn %q expected 1 replica after re-invoke, got %d", fnND, *got)
		}
	}, 3*time.Minute, 5*time.Second)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "functionName=" + fnGPM,
		})
		if !assert.NoError(c, err) {
			return
		}
		var running int
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning {
				running++
			}
		}
		assert.GreaterOrEqualf(c, running, 1,
			"poolmgr fn %q expected ≥1 Running pod after re-invoke, got %d", fnGPM, running)
	}, 3*time.Minute, 5*time.Second)
}
