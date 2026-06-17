// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"os"
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
	// Deliberately NOT t.Parallel(): see the doc comment. 15m (matching the adopt
	// test) covers the stack-up — control-plane-stable wait, onboard, a cold-start
	// function invoke (poolmgr spin-up + first specialization in a brand-new
	// namespace), and offboard — when this runs after the other serial tests.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
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

	// DIAGNOSTIC: locate where the staged CRs actually landed. envtest (CLI +
	// apiserver, no control plane) puts them in tenantNS, but a CI run showed
	// tenantNS empty at failure — so log the ground truth across all namespaces.
	allFns, ferr := f.FissionClient().CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	t.Logf("DIAG: list-all functions err=%v", ferr)
	for i := range allFns.Items {
		t.Logf("DIAG: function %s/%s", allFns.Items[i].Namespace, allFns.Items[i].Name)
	}
	allEnvs, _ := f.FissionClient().CoreV1().Environments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	for i := range allEnvs.Items {
		t.Logf("DIAG: environment %s/%s", allEnvs.Items[i].Namespace, allEnvs.Items[i].Name)
	}
	_, nsGetErr := f.KubeClient().CoreV1().Namespaces().Get(ctx, tenantNS, metav1.GetOptions{})
	t.Logf("DIAG: namespace %q exists: %v (err=%v)", tenantNS, nsGetErr == nil, nsGetErr)
	t.Logf("DIAG: FISSION_DEFAULT_NAMESPACE=%q FISSION_RESOURCE_NAMESPACES=%q", os.Getenv("FISSION_DEFAULT_NAMESPACE"), os.Getenv("FISSION_RESOURCE_NAMESPACES"))

	// 2. Onboard. The controller provisions per-namespace RBAC, ServiceAccounts and
	//    the derived-key Secret; the data-plane managers add the namespace to their
	//    watched set AND re-enqueue the staged CRs — all without a restart.
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

	// 6. The namespace itself is deleted by the NewTestNamespaceIn cleanup hook.
}
