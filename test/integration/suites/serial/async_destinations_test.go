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
	}, asyncUpdateWindow, 1*time.Second)
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
	}, asyncWarmWindow, 2*time.Second)
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
	}, asyncWarmWindow, 2*time.Second)
}

// TestAsyncInvocationOnSuccessFunctionDestination: an async invocation with an
// onSuccess function destination runs the destination function out-of-band with
// the result envelope (the fn→fn chain).
func TestAsyncInvocationOnSuccessFunctionDestination(t *testing.T) {
	f := framework.Connect(t)
	ctx := asyncTestContext(t, asyncUpdateWindow, asyncWarmWindow, asyncWarmWindow, asyncAcceptWindow, asyncDeliverWindow)

	requireAsyncEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-dest-" + ns.ID
	srcFn := "async-src-" + ns.ID
	dstFn := "async-dst-" + ns.ID
	route := "/async-src-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	// The destination logs its request body: the result envelope's functionRef
	// ("<ns>/<srcFn>", unique per test run) is asserted with Contains — a
	// count-over-baseline assertion is sensitive to pod churn re-baselining the
	// visible logs (a CI flake this test used to have).
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: dstFn, Env: env, Code: framework.WriteTestData(t, "nodejs/log/logbody.js"), ExecutorType: "poolmgr"})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: srcFn, Env: env, Code: framework.WriteTestData(t, "nodejs/log/log.js"), ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: srcFn, URL: route, Method: http.MethodPost})

	setInvocation(t, ctx, f, ns, srcFn, &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: dstFn}},
	})

	warmRoute(t, ctx, f, route)
	warmInternal(t, ctx, f, dstFn)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, id := asyncPost(t, ctx, f, route, "dest-once-"+ns.ID)
		assert.Equal(c, http.StatusAccepted, status)
		assert.NotEmpty(c, id)
	}, asyncAcceptWindow, 2*time.Second)

	marker := ns.Name + "/" + srcFn
	// assert + Fatalf rather than require.Eventually: testify evaluates a failure
	// message's arguments eagerly, and the remaining context budget is only
	// meaningful once the wait has actually run out.
	if !assert.Eventually(t, func() bool {
		logs, err := ns.FunctionLogsE(t, ctx, dstFn)
		if err != nil {
			t.Logf("destination logs: %v (retrying)", err)
			return false
		}
		return strings.Contains(logs, marker)
	}, asyncDeliverWindow, 3*time.Second) {
		t.Fatalf("onSuccess destination function must execute out-of-band with the result envelope (marker %q); %s", marker, budgetLeft(ctx))
	}
}

// TestAsyncInvocationDepthCapBounds: a function whose onSuccess points back at
// itself chains a few hops then STOPS at the depth cap (invariant A6 — no runaway).
func TestAsyncInvocationDepthCapBounds(t *testing.T) {
	f := framework.Connect(t)
	// The self-loop chains MaxChainDepth hops, each one a full trip through the
	// shared queue, so it needs the delivery window more than the single-hop test
	// does — not less.
	ctx := asyncTestContext(t, asyncUpdateWindow, asyncWarmWindow, asyncAcceptWindow, asyncDeliverWindow)

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
	}, asyncAcceptWindow, 2*time.Second)

	// The chain runs several hops then the depth cap STOPS it: poll until the
	// execution count has reached the cap AND stopped growing (two equal reads a
	// tick apart ⇒ the chain settled), then assert the settled count is bounded.
	// Waiting for convergence (rather than a fixed sleep) is both faster on a quick
	// cluster and correct on a slow one, and the assert runs synchronously in the
	// test goroutine — no polling closure straggles into teardown calling
	// FunctionLogs on a cancelled context. A self-loop invoked once executes at
	// depths 0..MaxChainDepth (the depth+1 enqueue past the cap is dropped); the
	// slack tolerates at-least-once redelivery.
	settled := -1
	if !assert.Eventually(t, func() bool {
		logs, err := ns.FunctionLogsE(t, ctx, fnName)
		if err != nil {
			// FunctionLogs (no E) fails the test outright, which on an expired
			// context reports a log-read error rather than the timeout that
			// caused it. Retry instead, and let the wait own the failure.
			t.Logf("chain logs: %v (retrying)", err)
			return false
		}
		n := strings.Count(logs, logMarker) - baseline
		converged := n >= asyncinvoke.MaxChainDepth && n == settled
		settled = n
		return converged
	}, asyncDeliverWindow, 3*time.Second) {
		t.Fatalf("self-loop chain must reach the depth cap and settle (last count %d, cap %d); %s",
			settled, asyncinvoke.MaxChainDepth, budgetLeft(ctx))
	}
	assert.LessOrEqualf(t, settled, asyncinvoke.MaxChainDepth+3,
		"depth cap must bound the chain (got %d executions)", settled)
}
