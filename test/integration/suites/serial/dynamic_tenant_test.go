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
// static-namespace CI leg (FISSION_DYNAMIC_NAMESPACES off), matching how the
// RFC-0002/0013 gate-dependent tests detect their mode.
func TestDynamicTenantLifecycle(t *testing.T) {
	// Deliberately NOT t.Parallel(): see the doc comment.
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	if !f.DynamicNamespacesEnabled(t, ctx) {
		t.Skip("FISSION_DYNAMIC_NAMESPACES is off; this leg runs the static namespace model")
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

	// 1. Onboard. The controller provisions per-namespace RBAC, ServiceAccounts
	//    and the derived-key Secret; the data-plane managers add the namespace to
	//    their watched set via resolver-sync — no restart.
	ns.EnableTenant(t, ctx)
	f.WaitForTenantReady(t, ctx, tenantNS)

	// 2. Create a sample environment + function + route in the new namespace.
	envName := "python-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: pyImage, Poolsize: 1})

	fnName := "hello-" + ns.ID
	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})

	routeURL := "/" + fnName
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routeURL, Method: "GET"})

	// 3. Invoke it. The namespace-explicit internal route proves the executor
	//    specialized a pod in the tenant namespace — exercising the per-namespace
	//    fetcher HMAC key end-to-end — and the router serves it.
	r := f.Router(t)
	body := r.GetEventually(t, ctx, "/fission-function/"+tenantNS+"/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, strings.ToLower(body), "hello")

	// The public HTTPTrigger path must work too: the router picked up the new
	// tenant's trigger via resolver-sync, with no restart.
	r.GetEventually(t, ctx, routeURL, framework.BodyContains("hello"))

	// 4. Onboarding + serving a fresh tenant must not have rolled any
	//    control-plane pod (#3298: the whole point of the dynamic model).
	f.AssertNoControlPlaneRestart(t, ctx, before)

	// 5. Offboard, the safe-by-default way: drain the namespace, then disable.
	//    `tenant disable` refuses while functions remain (the controller never
	//    silently strips a busy namespace), so delete the resources and wait for
	//    the function to clear first. The controller then tears the namespace's
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

	// 6. The namespace itself is deleted by the NewTestNamespaceIn cleanup hook.
}
