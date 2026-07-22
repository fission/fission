// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Helpers for the RFC-0002 EndpointSlice data plane: locating a function's
// headless Service and its EndpointSlices (both live in the function's
// namespace on default installs, which is what CI runs — strictly the executor
// creates them in the pool namespace; see gp_service.go), and pinning the
// router's cache mode / the executor's replica count for the serial
// resilience tests.

// functionServiceSelector matches the executor-created per-function Service
// and (mirrored by the EndpointSlice controller) its slices.
func functionServiceSelector(fnName string) string {
	return labels.Set(map[string]string{
		fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
		fv1.FUNCTION_NAME:    fnName,
	}).AsSelector().String()
}

// GetFunctionService returns the function's RFC-0002 Service, or nil when it
// does not (yet) exist.
func (ns *TestNamespace) GetFunctionService(ctx context.Context, fnName string) (*apiv1.Service, error) {
	svcs, err := ns.f.kubeClient.CoreV1().Services(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: functionServiceSelector(fnName),
	})
	if err != nil {
		return nil, err
	}
	if len(svcs.Items) == 0 {
		return nil, nil
	}
	return &svcs.Items[0], nil
}

// WaitForFunctionService waits until the function's headless Service exists
// and returns it.
func (ns *TestNamespace) WaitForFunctionService(t *testing.T, ctx context.Context, fnName string, timeout time.Duration) *apiv1.Service {
	t.Helper()
	var svc *apiv1.Service
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := ns.GetFunctionService(ctx, fnName)
		if !assert.NoErrorf(c, err, "list function services for %s", fnName) {
			return
		}
		if assert.NotNilf(c, got, "function service for %s not created yet", fnName) {
			svc = got
		}
	}, timeout, time.Second)
	return svc
}

// ReadyFunctionEndpoints returns the ready endpoint addresses across the
// function's EndpointSlices.
func (ns *TestNamespace) ReadyFunctionEndpoints(ctx context.Context, fnName string) ([]string, error) {
	slices, err := ns.f.kubeClient.DiscoveryV1().EndpointSlices(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: functionServiceSelector(fnName),
	})
	if err != nil {
		return nil, err
	}
	var ready []string
	for i := range slices.Items {
		for _, ep := range slices.Items[i].Endpoints {
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			ready = append(ready, ep.Addresses...)
		}
	}
	return ready, nil
}

// WaitForFunctionEndpointsReady waits until the function has at least minReady
// ready endpoints in its EndpointSlices and returns their addresses.
func (ns *TestNamespace) WaitForFunctionEndpointsReady(t *testing.T, ctx context.Context, fnName string, minReady int, timeout time.Duration) []string {
	t.Helper()
	var ready []string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := ns.ReadyFunctionEndpoints(ctx, fnName)
		if !assert.NoErrorf(c, err, "list endpointslices for %s", fnName) {
			return
		}
		if assert.GreaterOrEqualf(c, len(got), minReady,
			"function %s ready endpoints (have %v)", fnName, got) {
			ready = got
		}
	}, timeout, time.Second)
	return ready
}

// RouterEndpointSliceMode reports the router's ROUTER_ENDPOINTSLICE_CACHE_MODE
// ("off" when unset), so gate-dependent tests can skip cleanly on legs that
// pin the legacy path.
func (f *Framework) RouterEndpointSliceMode(t *testing.T, ctx context.Context) string {
	t.Helper()
	dep, err := f.kubeClient.AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "router", metav1.GetOptions{})
	require.NoError(t, err, "get router Deployment")
	for i := range dep.Spec.Template.Spec.Containers {
		for _, env := range dep.Spec.Template.Spec.Containers[i].Env {
			if env.Name == "ROUTER_ENDPOINTSLICE_CACHE_MODE" && env.Value != "" {
				return env.Value
			}
		}
	}
	return "off"
}

// ExecutorFunctionServicesEnabled reports the executor's
// ENABLE_FUNCTION_SERVICES gate.
func (f *Framework) ExecutorFunctionServicesEnabled(t *testing.T, ctx context.Context) bool {
	t.Helper()
	return f.ExecutorEnvEnabled(t, ctx, "ENABLE_FUNCTION_SERVICES")
}

// ScaleExecutor scales the executor Deployment to the given replica count and
// waits for the pods to reach it. The returned restore func scales back to the
// original count (and waits). Callers must live in the serial suite — scaling
// the shared executor disrupts every cold start in the cluster.
func (f *Framework) ScaleExecutor(t *testing.T, ctx context.Context, replicas int32) (restore func()) {
	t.Helper()
	ns := f.FissionNamespace()
	dep, err := f.kubeClient.AppsV1().Deployments(ns).Get(ctx, executorDeploymentName, metav1.GetOptions{})
	require.NoError(t, err, "ScaleExecutor: get executor Deployment")
	original := int32(1)
	if dep.Spec.Replicas != nil {
		original = *dep.Spec.Replicas
	}

	scaleTo := func(ctx context.Context, n int32) error {
		patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, n)
		_, err := f.kubeClient.AppsV1().Deployments(ns).Patch(ctx, executorDeploymentName,
			k8stypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
		return err
	}
	waitReplicas := func(ctx context.Context, n int32) {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			pods, err := f.kubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "svc=executor",
			})
			if !assert.NoError(c, err, "list executor pods") {
				return
			}
			running := 0
			for i := range pods.Items {
				if pods.Items[i].DeletionTimestamp == nil && pods.Items[i].Status.Phase == apiv1.PodRunning {
					running++
				}
			}
			assert.Equalf(c, int(n), running, "executor running pods")
		}, 2*time.Minute, 2*time.Second)
	}

	require.NoErrorf(t, scaleTo(ctx, replicas), "ScaleExecutor: scale to %d", replicas)
	waitReplicas(ctx, replicas)

	return func() {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := scaleTo(rctx, original); err != nil {
			t.Logf("ScaleExecutor: restore to %d failed: %v", original, err)
			return
		}
		waitReplicas(rctx, original)
	}
}
