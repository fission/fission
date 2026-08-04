// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestDynamicTenantLifecycle exercises the full multi-namespace tenancy
// lifecycle against a dynamic-mode cluster: onboard a brand-new namespace at
// runtime, run a function in it, then offboard and delete the namespace — all
// without restarting the control plane (#3298).
//
// It lives in the serial suite because it onboards a tenant (mutating the
// cluster-wide watched set) and asserts that no control-plane pod restarted,
// which the parallel suite's churn would invalidate. It skips itself on the
// static/cluster CI legs (tenancy.mode != dynamic), matching how the
// RFC-0002/0013 gate-dependent tests detect their mode.
func TestDynamicTenantLifecycle(t *testing.T) {
	// Deliberately NOT t.Parallel(): see the doc comment. 15m (matching the adopt
	// test) covers the stack-up — control-plane-stable wait, onboard, a cold-start
	// function invoke (poolmgr spin-up + first specialization in a brand-new
	// namespace), and offboard — when this runs after the other serial tests.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	if !f.DynamicNamespacesEnabled(t, ctx) {
		t.Skip("tenancy.mode is not dynamic; this leg runs a different tenancy model")
	}
	pyImage := f.Images().RequirePython(t)

	// Let any rollout from a prior serial test (e.g. the adopt test's un-waited
	// executor restore) settle, then snapshot the control plane so we can prove
	// onboarding restarts nothing — the headline promise of dynamic tenancy.
	f.WaitForControlPlaneStable(t, ctx, 3*time.Minute)
	before := f.ControlPlanePodUIDs(t, ctx)

	// A namespace that did not exist at install time: the dynamic path's reason
	// to exist. NewTestNamespaceIn creates it and binds the Create* helpers to it.
	tenantNS := "fission-tenant-e2e-" + framework.RandomID()
	ns := f.NewTestNamespaceIn(t, ctx, tenantNS)

	// 1. Stage a sample environment + function + route in the namespace BEFORE
	//    onboarding. The data plane drops them (not a tenant yet); onboarding then
	//    re-converges them in one pass. This is the deterministic exercise of the
	//    runtime-onboarding path: the FissionTenant re-enqueue re-lists the CRs that
	//    already exist, with no dependency on a CR's own create event racing the
	//    membership flip (which an "onboard, then create" ordering would, and which
	//    flaked the heavily-loaded coverage leg).
	envName := "python-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: pyImage, Poolsize: 1})

	fnName := "hello-" + ns.ID
	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})

	routeURL := "/" + fnName
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routeURL, Method: "GET"})

	// Make sure the staged CRs are durably persisted before onboarding, so the
	// stage-then-onboard ordering the test depends on is real and not racing the
	// CLI's create round-trip. The onboard re-enqueue LISTs the namespace's CRs to
	// converge them, so a function that isn't list-visible yet would be missed.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fns, lerr := f.FissionClient().CoreV1().Functions(tenantNS).List(ctx, metav1.ListOptions{})
		assert.NoError(c, lerr, "list functions in tenant namespace")
		assert.NotEmpty(c, fns.Items, "staged function must be list-visible before onboarding")
	}, time.Minute, time.Second)

	// 2. Onboard. The controller provisions per-namespace RBAC, ServiceAccounts and
	//    the derived-key Secret; the data-plane managers add the namespace to their
	//    watched set AND re-enqueue the staged CRs — all without a restart.
	//    Capture the router's informer count baseline: the final offboard must
	//    return to it (deterministic leak guard — exact, vs. goroutine delta ±10).
	informersBefore := scrapeRouterMetric(t, ctx, f, "fission_router_endpointcache_informers")
	ns.EnableTenant(t, ctx)
	f.WaitForTenantReady(t, ctx, tenantNS)

	// Re-enabling is idempotent: `tenant enable` matches on spec.namespace and
	// updates the existing tenant instead of creating a duplicate. The enable does
	// a full-object update, which can briefly 409 against the controller's status
	// writer, so retry; then prove exactly one FissionTenant exists for the namespace.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, eerr := ns.CLICaptureStdoutBestEffort(t, ctx, "tenant", "enable", "--namespace", tenantNS)
		assert.NoError(c, eerr, "re-enable tenant (idempotent)")
	}, 30*time.Second, 2*time.Second)
	tenants, err := f.FissionClient().CoreV1().FissionTenants().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "list FissionTenants")
	count := 0
	for i := range tenants.Items {
		if tenants.Items[i].Spec.Namespace == tenantNS {
			count++
		}
	}
	require.Equalf(t, 1, count, "re-enabling must not create a duplicate tenant for %q", tenantNS)

	// 3. Invoke it. The namespace-explicit internal route proves the executor
	//    specialized a pod in the tenant namespace — whose fetcher used the
	//    namespace's own derived HMAC key to fetch its package — and that the
	//    router resolved the tenant trigger. (Cross-namespace key *isolation* —
	//    that namespace A's key cannot auth as B — is a separate negative test,
	//    tracked as a follow-up; this asserts the positive path only.)
	r := f.Router(t)
	body := r.GetEventually(t, ctx, "/fission-function/"+tenantNS+"/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, strings.ToLower(body), "hello")

	// The public HTTPTrigger path must work too: the router re-enqueued the new
	// tenant's trigger on onboarding, with no restart.
	r.GetEventually(t, ctx, routeURL, framework.BodyContains("hello"))

	// 3b. The router's per-namespace EndpointSlice informer must have picked up
	//     the new tenant (#3647): the RBAC grant exists AND the warm path served
	//     a request — proving the informer fed the new tenant's EndpointSlices
	//     into the index. Before the fix the informer was frozen at startup and
	//     the router never saw the new tenant's EndpointSlices.
	rbac, rerr := f.KubeClient().RbacV1().RoleBindings(tenantNS).Get(ctx, "fission-router-dataplane", metav1.GetOptions{})
	require.NoErrorf(t, rerr, "router dataplane RoleBinding must exist in tenant namespace %q", tenantNS)
	require.Equalf(t, "ClusterRole", rbac.RoleRef.Kind, "RoleBinding should reference a ClusterRole")
	require.Equalf(t, "fission-router-dataplane-tenant", rbac.RoleRef.Name,
		"RoleBinding should reference the fission-router-dataplane-tenant ClusterRole")

	// The first invocation was a cold start (executor RPC); a warm hit proves the
	// per-namespace informer fed the tenant's EndpointSlices into the index.
	hitsBefore := scrapeRouterMetric(t, ctx, f, "fission_router_endpointcache_hits_total")
	assertWarmPathHit(t, ctx, r, f, routeURL, hitsBefore, "warm path never served tenant %q's function", tenantNS)

	// 4. Onboarding + serving a fresh tenant must not have rolled any
	//    control-plane pod (#3298: the whole point of the dynamic model).
	f.AssertNoControlPlaneRestart(t, ctx, before)

	// 5. Offboard, the safe-by-default way. First prove `tenant disable` REFUSES
	//    while the namespace still has a function — the controller never silently
	//    strips a busy namespace. (Best-effort CLI variant so the expected
	//    non-zero exit is an asserted error, not a t.Fatal.)
	out, derr := ns.CLICaptureStdoutBestEffort(t, ctx, "tenant", "disable", "--namespace", tenantNS)
	require.Errorf(t, derr, "tenant disable must refuse while %q still has a function (got: %s)", tenantNS, out)
	require.Containsf(t, derr.Error(), "function",
		"the refusal should explain the namespace still has functions: %v", derr)

	//    Now drain the namespace and disable for real. `tenant disable` succeeds
	//    once no functions remain; the controller then tears the namespace's
	//    RBAC/Secret down via its finalizer.
	fc := f.FissionClient().CoreV1()
	require.NoError(t, fc.HTTPTriggers(tenantNS).Delete(ctx, "route-"+fnName, metav1.DeleteOptions{}))
	require.NoError(t, fc.Functions(tenantNS).Delete(ctx, fnName, metav1.DeleteOptions{}))
	require.NoError(t, fc.Environments(tenantNS).Delete(ctx, envName, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := fc.Functions(tenantNS).Get(ctx, fnName, metav1.GetOptions{})
		assert.Truef(c, apierrors.IsNotFound(err), "function %q should clear before offboarding (err=%v)", fnName, err)
	}, time.Minute, time.Second)

	ns.DisableTenant(t, ctx)
	f.WaitForTenantOffboarded(t, ctx, tenantNS)

	// 6. Re-onboard the SAME namespace (testing-plan §4.2: onboard→offboard→
	//    re-onboard). This is the Tier-B teardown leak guard: the old informer's
	//    goroutine must have exited during offboard, and the new informer must
	//    start cleanly without overlapping the old one. A function created in
	//    the re-onboarded namespace must be invocable through the warm path.
	ns.EnableTenant(t, ctx)
	f.WaitForTenantReady(t, ctx, tenantNS)

	// Re-create env + function + trigger in the same namespace, using the
	// same patterns as the initial stage (ns.ID for unique names).
	envName2 := "python2-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName2, Image: pyImage, Poolsize: 1})

	fnName2 := "hello2-" + ns.ID
	codePath2 := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName2, Env: envName2, Code: codePath2})

	route2URL := "/" + fnName2
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName2, URL: route2URL, Method: "GET"})

	// First invocation cold-starts; verify the re-onboarded namespace serves.
	r.GetEventually(t, ctx, route2URL, framework.BodyContains("hello"))

	// Second invocation must hit the warm path — proves the new informer fed
	// the re-onboarded tenant's EndpointSlices into the index.
	hitsBefore2 := scrapeRouterMetric(t, ctx, f, "fission_router_endpointcache_hits_total")
	assertWarmPathHit(t, ctx, r, f, route2URL, hitsBefore2, "warm path never served re-onboarded tenant %q's function", tenantNS)

	// 7. Final offboard + informer-count leak guard (testing-plan §4.2): the
	//    per-namespace informer must die with the tenant. The informer count
	//    gauge is exact (0 = no leak, >0 = leak), replacing the ±10 goroutine
	//    delta which couldn't detect a single leaked informer (~5-7 goroutines).
	require.NoError(t, fc.HTTPTriggers(tenantNS).Delete(ctx, "route-"+fnName2, metav1.DeleteOptions{}))
	require.NoError(t, fc.Functions(tenantNS).Delete(ctx, fnName2, metav1.DeleteOptions{}))
	require.NoError(t, fc.Environments(tenantNS).Delete(ctx, envName2, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := fc.Functions(tenantNS).Get(ctx, fnName2, metav1.GetOptions{})
		assert.Truef(c, apierrors.IsNotFound(err), "function %q should clear before offboarding (err=%v)", fnName2, err)
	}, time.Minute, time.Second)
	ns.DisableTenant(t, ctx)
	f.WaitForTenantOffboarded(t, ctx, tenantNS)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		n := scrapeRouterMetric(c, ctx, f, "fission_router_endpointcache_informers")
		assert.Equalf(c, informersBefore, n,
			"informer count must return to the pre-onboard baseline after offboard (baseline=%.0f, current=%.0f) — a leaked informer pins goroutines forever",
			informersBefore, n)
	}, 2*time.Minute, 5*time.Second, "informer count never returned to baseline — per-namespace informer leak suspected")

	// 8. The namespace itself is deleted by the NewTestNamespaceIn cleanup hook.
}

// assertWarmPathHit polls until invoking routeURL increments the router's
// fission_router_endpointcache_hits_total beyond hitsBefore — i.e. the warm
// path (slice-fed index, no executor RPC) served a request. The invocation is
// INSIDE the poll: after a cold start the executor patches the pod's served
// label and the EndpointSlice controller mirrors it (both async, seconds of
// lag), so scraping without invoking can never move the counter.
func assertWarmPathHit(t *testing.T, ctx context.Context, r *framework.RouterClient, f *framework.Framework, routeURL string, hitsBefore float64, msg string, args ...any) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, body, err := r.Get(ctx, routeURL)
		if !assert.NoError(c, err) {
			return
		}
		assert.Contains(c, strings.ToLower(body), "hello")
		hitsAfter := scrapeRouterMetric(c, ctx, f, "fission_router_endpointcache_hits_total")
		assert.Greaterf(c, hitsAfter, hitsBefore,
			"endpointcache_hits_total must increase on a warm invocation — proves the warm path served the tenant's function")
	}, 2*time.Minute, 5*time.Second, append([]any{msg}, args...)...)
}

// scrapeRouterMetric scrapes a single Prometheus metric from the router pods
// (via pod proxy, no port-forward needed) and returns its sum across replicas.
// Sums all matching samples per pod via framework.SumMetricLines.
func scrapeRouterMetric(t require.TestingT, ctx context.Context, f *framework.Framework, metricName string) float64 {
	pods, err := f.KubeClient().CoreV1().Pods(f.FissionNamespace()).List(ctx, metav1.ListOptions{LabelSelector: "svc=router"})
	require.NoErrorf(t, err, "listing router pods")
	require.NotEmptyf(t, pods.Items, "no router pod found in namespace %s", f.FissionNamespace())
	var total float64
	for _, p := range pods.Items {
		raw, err := f.KubeClient().CoreV1().Pods(f.FissionNamespace()).ProxyGet("http", p.Name, "8080", "/metrics", nil).DoRaw(ctx)
		require.NoErrorf(t, err, "scraping /metrics from router pod %s", p.Name)
		total += framework.SumMetricLines(raw, metricName)
	}
	return total
}
