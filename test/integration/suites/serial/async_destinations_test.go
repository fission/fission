// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/test/integration/framework"
)

// setInvocation sets a function's InvocationConfig (destinations), which the CLI
// cannot express, retrying on the reconciler's resourceVersion churn.
func setInvocation(t *testing.T, ctx context.Context, f *framework.Framework, ns *framework.TestNamespace, fnName string, ic *fv1.InvocationConfig) {
	t.Helper()
	fc := f.FissionClient().CoreV1().Functions(ns.Name)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn, err := fc.Get(ctx, fnName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		fn.Spec.Invocation = ic
		_, err = fc.Update(ctx, fn, metav1.UpdateOptions{})
		assert.NoError(c, err) // retry on conflict
	}, 30*time.Second, 1*time.Second)
}

// warmRoute drives a synchronous POST through the trigger until it 2xxes, so the
// function is specialized and the route is live (keeps the later async path off
// the cold-start path).
func warmRoute(t *testing.T, ctx context.Context, f *framework.Framework, route string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Router(t).BaseURL()+route, strings.NewReader("warm"))
		if !assert.NoError(c, err) {
			return
		}
		resp, err := f.HTTPClient().Do(req)
		if !assert.NoError(c, err) {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		assert.Less(c, resp.StatusCode, 400, "route live and function ready")
	}, 2*time.Minute, 2*time.Second)
}

// warmInternal specializes a route-less destination function by invoking it on the
// internal listener (where the dispatcher delivers), so a destination fire is not
// itself a cold start.
func warmInternal(t *testing.T, ctx context.Context, f *framework.Framework, fnName string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _, err := f.Router(t).Post(ctx, "/fission-function/"+fnName, "text/plain", []byte("warm"))
		if !assert.NoError(c, err) {
			return
		}
		assert.Less(c, status, 400, "destination function specialized")
	}, 2*time.Minute, 2*time.Second)
}

// TestAsyncInvocationOnSuccessFunctionDestination: an async invocation with an
// onSuccess function destination runs the destination function out-of-band with
// the result envelope (the fn→fn chain).
func TestAsyncInvocationOnSuccessFunctionDestination(t *testing.T) {
	f := framework.Connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	requireAsyncEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-dest-" + ns.ID
	srcFn := "async-src-" + ns.ID
	dstFn := "async-dst-" + ns.ID
	route := "/async-src-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	code := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: dstFn, Env: env, Code: code, ExecutorType: "poolmgr"})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: srcFn, Env: env, Code: code, ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: srcFn, URL: route, Method: http.MethodPost})

	setInvocation(t, ctx, f, ns, srcFn, &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: dstFn}},
	})

	warmRoute(t, ctx, f, route)
	warmInternal(t, ctx, f, dstFn)
	baseline := strings.Count(ns.FunctionLogs(t, ctx, dstFn), logMarker)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, id := asyncPost(t, ctx, f, route, "dest-once-"+ns.ID)
		assert.Equal(c, http.StatusAccepted, status)
		assert.NotEmpty(c, id)
	}, 2*time.Minute, 2*time.Second)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := strings.Count(ns.FunctionLogs(t, ctx, dstFn), logMarker)
		assert.Greater(c, got, baseline, "onSuccess destination function must execute out-of-band")
	}, 3*time.Minute, 3*time.Second)
}

// TestAsyncInvocationDepthCapBounds: a function whose onSuccess points back at
// itself chains a few hops then STOPS at the depth cap (invariant A6 — no runaway).
func TestAsyncInvocationDepthCapBounds(t *testing.T) {
	f := framework.Connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	requireAsyncEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-loop-" + ns.ID
	fnName := "async-loop-" + ns.ID
	route := "/async-loop-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	code := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: env, Code: code, ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: route, Method: http.MethodPost})

	// Self-loop: every success fires the same function again.
	setInvocation(t, ctx, f, ns, fnName, &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName}},
	})

	warmRoute(t, ctx, f, route)
	baseline := strings.Count(ns.FunctionLogs(t, ctx, fnName), logMarker)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _ := asyncPost(t, ctx, f, route, "loop-once-"+ns.ID)
		assert.Equal(c, http.StatusAccepted, status)
	}, 2*time.Minute, 2*time.Second)

	// The chain ran several hops (destinations chained), proving the loop works.
	require.Eventually(t, func() bool {
		return strings.Count(ns.FunctionLogs(t, ctx, fnName), logMarker)-baseline >= asyncinvoke.MaxChainDepth
	}, 3*time.Minute, 3*time.Second)

	// The depth cap must BOUND the chain: over a settle window the execution count
	// must NEVER exceed the bound. require.Never fails the instant a runaway breaches
	// it (rather than a single end-of-sleep check that could miss a late hop). A
	// self-loop invoked once executes at depths 0..MaxChainDepth (the depth+1 enqueue
	// past the cap is dropped); the slack tolerates at-least-once redelivery.
	require.Neverf(t, func() bool {
		return strings.Count(ns.FunctionLogs(t, ctx, fnName), logMarker)-baseline > asyncinvoke.MaxChainDepth+3
	}, 20*time.Second, 2*time.Second, "depth cap must bound the chain (runaway past %d executions)", asyncinvoke.MaxChainDepth+3)
}
