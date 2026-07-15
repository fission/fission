// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// RFC-0024 async invocation integration tests. These live in the serial suite
// because the statestore-down case scales a shared control-plane Deployment
// (svc/statestore) to zero. Crash-recovery redelivery (a leased-but-unsettled
// invocation redelivered after lease expiry) is timing-sensitive at the
// integration layer and is instead covered deterministically at the queue layer:
// the statestore conformance ExhaustedByExpiry + Q2 epoch-guard subtests and the
// dispatcher's testing/synctest backoff test.
package serial_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/test/integration/framework"
)

// logMarker is the line the nodejs/log/log.js fixture writes on each invocation.
const logMarker = "log test"

// requireAsyncEnabled skips the test unless the router runs with async invocation
// on (ASYNC_INVOCATION_ENABLED=true), so the suite passes on installs that leave
// the feature off.
func requireAsyncEnabled(t *testing.T, ctx context.Context, f *framework.Framework) {
	t.Helper()
	dep, err := f.KubeClient().AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "router", metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "ASYNC_INVOCATION_ENABLED" && e.Value == "true" {
				return
			}
		}
	}
	t.Skip("async invocation is not enabled on the router (ASYNC_INVOCATION_ENABLED != true); skipping")
}

// scaleDeployment scales a control-plane Deployment to replicas and returns a
// restore func that scales it back to its original replica count.
func scaleDeployment(t *testing.T, ctx context.Context, f *framework.Framework, name string, replicas int32) func() {
	t.Helper()
	deps := f.KubeClient().AppsV1().Deployments(f.FissionNamespace())
	dep, err := deps.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	orig := int32(1)
	if dep.Spec.Replicas != nil {
		orig = *dep.Spec.Replicas
	}
	patch := func(r int32) {
		_, perr := deps.Patch(ctx, name, k8stypes.StrategicMergePatchType,
			fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, r), metav1.PatchOptions{})
		require.NoError(t, perr)
	}
	patch(replicas)
	return func() {
		patch(orig)
	}
}

// asyncPost issues an X-Fission-Invoke-Mode: async request through the public
// listener (the unsigned client, since async is an HTTPTrigger feature) and
// returns the status and the X-Fission-Invocation-Id header.
func asyncPost(t *testing.T, ctx context.Context, f *framework.Framework, route, dedupKey string) (int, string) {
	t.Helper()
	status, id, err := asyncPostE(t, ctx, f, route, dedupKey)
	require.NoError(t, err)
	return status, id
}

// asyncPostE is asyncPost without the fatal require, for use inside polling
// closures where a transient error must mean "retry this tick" (t is only
// forwarded to Router for its Helper bookkeeping — never failed).
func asyncPostE(t *testing.T, ctx context.Context, f *framework.Framework, route, dedupKey string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Router(t).BaseURL()+route, strings.NewReader("payload"))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	if dedupKey != "" {
		req.Header.Set(asyncinvoke.HeaderDedupKey, dedupKey)
	}
	resp, err := f.HTTPClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get(asyncinvoke.HeaderInvocationID), nil
}

// TestAsyncInvocationExecutes: an async request returns 202 + an invocation id
// and the function executes out-of-band (invariant A1 accept + at-least-once
// delivery). A dedup key collapses the retried POSTs used to wait for the route
// to go live into a single durable invocation.
func TestAsyncInvocationExecutes(t *testing.T) {
	f := framework.Connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	requireAsyncEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-async-" + ns.ID
	fn := "async-log-" + ns.ID
	route := "/async-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	code := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fn, Env: env, Code: code, ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fn, URL: route, Method: http.MethodPost})

	// Warm the function with a synchronous POST so the route is live and a pod is
	// specialized BEFORE the async path is exercised: this keeps the async delivery
	// off the cold-start path (whose specialization latency under a loaded CI node
	// can otherwise exhaust the delivery retry budget and dead-letter the message)
	// and gives a stable baseline marker count.
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
	}, 3*time.Minute, 2*time.Second)
	baseline := strings.Count(ns.FunctionLogs(t, ctx, fn), logMarker)

	// Async POST returns 202 + an invocation id; the dedup key collapses the
	// route-liveness retries into a single durable invocation.
	dedup := "async-once-" + ns.ID
	var invID string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, id := asyncPost(t, ctx, f, route, dedup)
		assert.Equal(c, http.StatusAccepted, status)
		invID = id
		assert.NotEmpty(c, id)
	}, 2*time.Minute, 2*time.Second)
	require.NotEmpty(t, invID, "202 must carry an X-Fission-Invocation-Id")

	// The async invocation runs the function out-of-band: the marker count grows
	// beyond the warm-up baseline (proving the async delivery executed it, not just
	// that the marker is present from the warm-up).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := strings.Count(ns.FunctionLogs(t, ctx, fn), logMarker)
		assert.Greater(c, got, baseline, "async invocation must execute the function")
	}, 3*time.Minute, 3*time.Second)
}

// TestAsyncInvocationStatestoreDown503: with the statestore unreachable, an async
// enqueue fails loud with 503 and never a silently dropped 202 (invariant A1).
func TestAsyncInvocationStatestoreDown503(t *testing.T) {
	f := framework.Connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	requireAsyncEnabled(t, ctx, f)
	if _, err := f.KubeClient().AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "statestore", metav1.GetOptions{}); err != nil {
		t.Skipf("embedded statestore Deployment not found (external mode cannot be scaled here); skipping: %v", err)
	}
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-async503-" + ns.ID
	fn := "async503-" + ns.ID
	route := "/async503-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	code := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fn, Env: env, Code: code, ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fn, URL: route, Method: http.MethodPost})

	// Warm the route with a synchronous POST (no async header) so the later async
	// 503 is attributable to the enqueue, not a not-yet-materialized trigger. The
	// trigger is POST-only, so a GET would 405 — warm with the method it accepts.
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

	// Take the statestore down and confirm the enqueue fails loud (503), restoring
	// it afterward for the rest of the suite.
	restore := scaleDeployment(t, ctx, f, "statestore", 0)
	defer restore()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _ := asyncPost(t, ctx, f, route, "")
		assert.Equal(c, http.StatusServiceUnavailable, status,
			"async enqueue with the statestore down must 503, never 202")
	}, 3*time.Minute, 3*time.Second)
}
