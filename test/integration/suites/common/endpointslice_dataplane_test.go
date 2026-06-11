// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestPoolmgrFunctionEndpointSlice covers the executor side of RFC-0002: the
// first invocation of a poolmgr function creates a headless selector Service
// in the function namespace, the built-in EndpointSlice controller publishes
// the specialized (served-labeled) pod as a ready endpoint, and deleting the
// function removes the Service again.
func TestPoolmgrFunctionEndpointSlice(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	if !f.ExecutorFunctionServicesEnabled(t, ctx) {
		t.Skip("executor.functionServices.enabled is off; skipping EndpointSlice data plane test")
	}
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-eps-" + ns.ID
	fnName := "fn-eps-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// No Service before the first invocation: the object count stays
	// proportional to the invoked working set.
	svc, err := ns.GetFunctionService(ctx, fnName)
	require.NoError(t, err)
	require.Nil(t, svc, "function Service must not exist before the first invocation")

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// The Service ensure is async (off the cold-start path) — wait for it.
	svc = ns.WaitForFunctionService(t, ctx, fnName, time.Minute)
	assert.Equal(t, apiv1.ClusterIPNone, svc.Spec.ClusterIP, "function Service must be headless")
	assert.Equal(t, "true", svc.Spec.Selector[fv1.SERVED_LABEL], "selector must gate on the served label")
	assert.NotEmpty(t, svc.Spec.Selector[fv1.FUNCTION_UID])
	assert.NotEmpty(t, svc.Spec.Selector[fv1.FUNCTION_GENERATION], "selector must pin the function generation")

	// The specialized pod becomes a ready endpoint via the built-in
	// EndpointSlice controller (no custom slice writer).
	ready := ns.WaitForFunctionEndpointsReady(t, ctx, fnName, 1, time.Minute)
	assert.NotEmpty(t, ready)

	// Warm invocations keep working (from the slice index when the router gate
	// is on; via the executor otherwise — both must serve).
	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// Function deletion removes the Service (reconciler hook / owner ref).
	ns.CLI(t, ctx, "fn", "delete", "--name", fnName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got, err := ns.GetFunctionService(ctx, fnName)
		if !assert.NoError(c, err) {
			return
		}
		assert.Nilf(c, got, "function Service must be deleted with the function")
	}, time.Minute, 2*time.Second)
}
