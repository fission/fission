// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestWarmInvokeWithExecutorDown is the headline resilience win of RFC-0002:
// with the EndpointSlice data plane on, warm traffic to an already-specialized
// poolmgr function keeps flowing while the executor is down (scaled to zero) —
// the router serves it from its slice-fed endpoint index with zero executor
// RPCs. A never-invoked function still needs the executor (cold starts are its
// job by design) and fails cleanly instead.
//
// Lives in the serial suite: scaling the shared executor breaks every cold
// start in the cluster for the duration.
func TestWarmInvokeWithExecutorDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	if f.RouterEndpointSliceMode(t, ctx) != "on" {
		t.Skip("router.endpointSliceCache.mode is not 'on'; warm-path survival needs the slice-fed index")
	}
	if !f.ExecutorFunctionServicesEnabled(t, ctx) {
		t.Skip("executor.functionServices.enabled is off; poolmgr functions have no EndpointSlices")
	}
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-down-" + ns.ID
	warmFn := "fn-warm-" + ns.ID
	coldFn := "fn-cold-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	for _, fn := range []string{warmFn, coldFn} {
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fn, Env: envName, Code: codePath, ExecutorType: "poolmgr",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fn, URL: "/" + fn, Method: "GET"})
	}

	// Warm exactly one function and wait until its specialized pod is a ready
	// endpoint in the slices — that is what the router's index serves from.
	// Keep invoking while waiting: the Service ensure is async and self-heals
	// on the warm RPC path (e.g. when the preceding serial test's executor
	// rollout killed the first ensure mid-flight), so slices are guaranteed
	// only under traffic.
	body := f.Router(t).GetEventually(t, ctx, "/"+warmFn, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _, err := f.Router(t).Get(ctx, "/"+warmFn)
		if assert.NoError(c, err) {
			assert.Equal(c, http.StatusOK, status)
		}
		ready, err := ns.ReadyFunctionEndpoints(ctx, warmFn)
		if !assert.NoError(c, err) {
			return
		}
		assert.NotEmptyf(c, ready, "function %s has no ready slice endpoints yet", warmFn)
	}, 2*time.Minute, 2*time.Second)

	restore := f.ScaleExecutor(t, ctx, 0)
	defer restore()

	// The warm function keeps serving with the executor gone: five CONSECUTIVE
	// successful invokes. The Eventually wrapper tolerates a transient at the
	// boundary (e.g. the index clearing a quarantine or releasing a slot on
	// the next slice event) without weakening the consecutive-success
	// requirement itself.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		for i := range 5 {
			status, body, err := f.Router(t).Get(ctx, "/"+warmFn)
			if !assert.NoErrorf(c, err, "warm invoke %d with executor down", i) {
				return
			}
			if !assert.Equalf(c, http.StatusOK, status, "warm invoke %d with executor down (body: %s)", i, body) {
				return
			}
			assert.Contains(c, body, "hello")
		}
	}, 90*time.Second, 3*time.Second)

	// The never-invoked function needs a cold start, which needs the executor:
	// it must fail cleanly (a non-2xx from the router), not hang past the
	// request context.
	coldCtx, coldCancel := context.WithTimeout(ctx, 90*time.Second)
	defer coldCancel()
	status, _, err := f.Router(t).Get(coldCtx, "/"+coldFn)
	if err == nil {
		require.NotEqual(t, http.StatusOK, status, "a cold start cannot succeed with the executor down")
	}

	// Recovery: with the executor back, the cold function specializes and
	// serves.
	restore()
	body = f.Router(t).GetEventually(t, ctx, "/"+coldFn, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
}

// TestMixedGateUpgradeOrder proves upgrade-order safety: with the router's
// slice-fed index on but the executor's function-Services flag off (an
// executor older than RFC-0002, or the flag rolled back), poolmgr functions
// have no slices — and every request degrades to the legacy executor RPC,
// which keeps working.
func TestMixedGateUpgradeOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	if f.RouterEndpointSliceMode(t, ctx) != "on" {
		t.Skip("router.endpointSliceCache.mode is not 'on'; mixed-gate ordering is moot")
	}
	image := f.Images().RequireNode(t)

	generation, restoreEnv := f.SetExecutorEnv(t, ctx, "ENABLE_FUNCTION_SERVICES", "false")
	defer restoreEnv()
	f.WaitForExecutorRollout(t, ctx, generation, 3*time.Minute)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-mixed-" + ns.ID
	fnName := "fn-mixed-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	// Cold and warm invokes both work via the RPC fallback.
	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
	status, body, err := f.Router(t).Get(ctx, "/"+fnName)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "warm invoke via RPC fallback (body: %s)", body)

	// And no function Service was created while the executor flag was off.
	svc, err := ns.GetFunctionService(ctx, fnName)
	require.NoError(t, err)
	assert.Nil(t, svc, "no function Service may be created while ENABLE_FUNCTION_SERVICES=false")
}
