// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"

	"github.com/fission/fission/test/integration/framework"
)

// TestHPAScaleUnderLoad proves the full HPA pipeline end to end: the executor
// emits a ContainerResource cpu metric scoped to the function container
// (pod-wide Resource metrics break when any container lacks a cpu request),
// metrics-server feeds KCM, and sustained load scales the deployment above
// MinScale. Guards against regressions in both the metric shape and the CI
// metrics pipeline.
func TestHPAScaleUnderLoad(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-hpa-" + ns.ID
	fnName := "fn-hpa-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	// MinCPU=50 sets a 50m cpu request on the function container — required so
	// the ContainerResource cpu metric has a denominator. TargetCPU=20 is low
	// so light sustained load trips the autoscaler.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 3,
		TargetCPU: 20,
		MinCPU:    50, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// Function serves traffic.
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("world"))

	// Metric-shape assertion: the executor must emit a ContainerResource cpu
	// metric scoped to the function container (named after the env), not a
	// pod-wide Resource metric. This fails fast on a regression of the
	// ContainerResource rewrite, before we depend on actual scaling behavior.
	ns.WaitForFunctionHPA(t, ctx, fnName, func(h *autoscalingv2.HorizontalPodAutoscaler) bool {
		for _, m := range h.Spec.Metrics {
			if m.Type == autoscalingv2.ContainerResourceMetricSourceType &&
				m.ContainerResource != nil && m.ContainerResource.Container == envName {
				return true
			}
		}
		return false
	}, "hpa carries a ContainerResource cpu metric scoped to the function container", 90*time.Second)

	// Sustained load: three concurrent loops (~30 rps total) until the test
	// finishes, so the autoscaler keeps observing elevated cpu across its
	// stabilization window.
	loadCtx, stopLoad := context.WithCancel(ctx)
	t.Cleanup(stopLoad)
	for i := 0; i < 3; i++ {
		go f.Router(t).LoadLoop(loadCtx, routePath)
	}

	// Intermediate checkpoint for failure attribution: the metrics pipeline
	// (metrics-server -> KCM) must surface a current cpu reading on the HPA
	// before scaling can possibly happen.
	ns.WaitForFunctionHPA(t, ctx, fnName, func(h *autoscalingv2.HorizontalPodAutoscaler) bool {
		return len(h.Status.CurrentMetrics) > 0
	}, "metrics pipeline reports container cpu", 3*time.Minute)

	// Scale assertion: never assert an exact replica count — only that the
	// deployment scaled above MinScale under sustained load.
	ns.WaitForFunctionDeployment(t, ctx, fnName, func(d *appsv1.Deployment) bool {
		return d.Status.ReadyReplicas >= 2
	}, "scaled above minscale under sustained load", 4*time.Minute)
}
