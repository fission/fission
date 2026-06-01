// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Introspection helpers for the Kubernetes objects the newdeploy and container
// executors create per function. These let tests assert on what the executor
// *actually produced* (the Deployment pod template, the HPA bounds) rather than
// only on the Function CR spec the CLI wrote — so a bug where updateFunction
// failed to reconcile the live object would be caught.
//
// Both executors label their Deployment, Service, HPA and pods with
// functionName / functionNamespace (newdeploy getDeployLabels,
// container getDeployLabels), and the HPA inherits the same deployLabels
// (pkg/executor/util/hpa/hpa.go). The functionNamespace *label* is always the
// Function CR's namespace; it is distinct from the executor's optional
// namespace *override* (FISSION_FUNCTION_NAMESPACE / NamespaceResolver), which
// can place the physical objects in a different namespace. So we list across
// all namespaces and select purely by the labels — that works whether or not a
// function namespace override is configured. Poolmgr functions have no
// per-function Deployment/HPA; do not use these helpers for poolmgr-backed
// functions.

// functionResourceSelector matches the per-function objects created by the
// newdeploy/container executors.
func (ns *TestNamespace) functionResourceSelector(fnName string) string {
	return fv1.FUNCTION_NAME + "=" + fnName + "," + fv1.FUNCTION_NAMESPACE + "=" + ns.Name
}

// FunctionDeployment returns the single Deployment backing a newdeploy- or
// container-executor function. Fails unless exactly one matches — call it only
// once the deployment is known to exist (e.g. after WaitForFunctionDeployment
// or after the function has served traffic).
func (ns *TestNamespace) FunctionDeployment(t *testing.T, ctx context.Context, fnName string) *appsv1.Deployment {
	t.Helper()
	items, err := ns.listFunctionDeployments(ctx, fnName)
	require.NoErrorf(t, err, "FunctionDeployment: list deployments for %q", fnName)
	require.Lenf(t, items, 1, "FunctionDeployment: expected exactly one deployment for %q (got %d)", fnName, len(items))
	return &items[0]
}

// FunctionHPA returns the HorizontalPodAutoscaler backing a newdeploy- or
// container-executor function. Fails unless exactly one matches.
func (ns *TestNamespace) FunctionHPA(t *testing.T, ctx context.Context, fnName string) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	items, err := ns.listFunctionHPAs(ctx, fnName)
	require.NoErrorf(t, err, "FunctionHPA: list HPAs for %q", fnName)
	require.Lenf(t, items, 1, "FunctionHPA: expected exactly one HPA for %q (got %d)", fnName, len(items))
	return &items[0]
}

// WaitForFunctionDeployment polls until exactly one Deployment for the function
// exists and satisfies check, then returns the final object. reason is included
// in the failure message.
func (ns *TestNamespace) WaitForFunctionDeployment(t *testing.T, ctx context.Context, fnName string, check func(*appsv1.Deployment) bool, reason string, timeout time.Duration) *appsv1.Deployment {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		items, err := ns.listFunctionDeployments(ctx, fnName)
		if !assert.NoErrorf(c, err, "list deployments for %q", fnName) {
			return
		}
		if !assert.Lenf(c, items, 1, "expected one deployment for %q (got %d)", fnName, len(items)) {
			return
		}
		assert.Truef(c, check(&items[0]), "deployment for %q: %s", fnName, reason)
	}, timeout, 2*time.Second)
	return ns.FunctionDeployment(t, ctx, fnName)
}

// WaitForFunctionHPA polls until exactly one HPA for the function exists and
// satisfies check, then returns the final object.
func (ns *TestNamespace) WaitForFunctionHPA(t *testing.T, ctx context.Context, fnName string, check func(*autoscalingv2.HorizontalPodAutoscaler) bool, reason string, timeout time.Duration) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		items, err := ns.listFunctionHPAs(ctx, fnName)
		if !assert.NoErrorf(c, err, "list HPAs for %q", fnName) {
			return
		}
		if !assert.Lenf(c, items, 1, "expected one HPA for %q (got %d)", fnName, len(items)) {
			return
		}
		assert.Truef(c, check(&items[0]), "HPA for %q: %s", fnName, reason)
	}, timeout, 2*time.Second)
	return ns.FunctionHPA(t, ctx, fnName)
}

// listFunctionDeployments lists across all namespaces and filters by the
// function labels, so it works regardless of any function-namespace override.
func (ns *TestNamespace) listFunctionDeployments(ctx context.Context, fnName string) ([]appsv1.Deployment, error) {
	list, err := ns.f.kubeClient.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: ns.functionResourceSelector(fnName),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (ns *TestNamespace) listFunctionHPAs(ctx context.Context, fnName string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	list, err := ns.f.kubeClient.AutoscalingV2().HorizontalPodAutoscalers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: ns.functionResourceSelector(fnName),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// poolDeploymentSelector matches the single warm-pool Deployment the poolmgr
// executor maintains per environment. Unlike newdeploy/container, poolmgr has
// no per-function Deployment — its pods come from one env-scoped pool — so the
// selector keys on environmentName + executorType (gp.go getEnvironmentPoolLabels).
func poolDeploymentSelector(envName string) string {
	return fv1.ENVIRONMENT_NAME + "=" + envName + "," + fv1.EXECUTOR_TYPE + "=" + string(fv1.ExecutorTypePoolmgr)
}

// PoolDeployment returns the poolmgr warm-pool Deployment for an environment.
// Fails unless exactly one matches — call it only once the pool is known to
// exist (e.g. after a poolmgr function on the env has served traffic, or after
// WaitForPoolDeployment).
func (ns *TestNamespace) PoolDeployment(t *testing.T, ctx context.Context, envName string) *appsv1.Deployment {
	t.Helper()
	items, err := ns.listPoolDeployments(ctx, envName)
	require.NoErrorf(t, err, "PoolDeployment: list pool deployments for env %q", envName)
	require.Lenf(t, items, 1, "PoolDeployment: expected exactly one pool deployment for env %q (got %d)", envName, len(items))
	return &items[0]
}

// WaitForPoolDeployment polls until exactly one pool Deployment for the
// environment exists and satisfies check, then returns the final object.
func (ns *TestNamespace) WaitForPoolDeployment(t *testing.T, ctx context.Context, envName string, check func(*appsv1.Deployment) bool, reason string, timeout time.Duration) *appsv1.Deployment {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		items, err := ns.listPoolDeployments(ctx, envName)
		if !assert.NoErrorf(c, err, "list pool deployments for env %q", envName) {
			return
		}
		if !assert.Lenf(c, items, 1, "expected one pool deployment for env %q (got %d)", envName, len(items)) {
			return
		}
		assert.Truef(c, check(&items[0]), "pool deployment for env %q: %s", envName, reason)
	}, timeout, 2*time.Second)
	return ns.PoolDeployment(t, ctx, envName)
}

// listPoolDeployments lists across all namespaces and filters by the env's
// poolmgr labels, so it works regardless of any function-namespace override.
func (ns *TestNamespace) listPoolDeployments(ctx context.Context, envName string) ([]appsv1.Deployment, error) {
	list, err := ns.f.kubeClient.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: poolDeploymentSelector(envName),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
