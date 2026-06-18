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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestClusterTenantAutoOnboard exercises the opt-in trusted-cluster mode: a
// function runs in a brand-new, never-declared namespace with NO FissionTenant
// ceremony (the controller auto-onboards it), and the fetcher's grant in that
// namespace stays least-privilege — a per-namespace RoleBinding, never a
// cluster-wide ClusterRoleBinding. That least-privilege guarantee is the whole
// reason cluster mode runs the controller instead of binding fetcher cluster-wide.
//
// Serial because it mutates the cluster-wide watched set (auto-onboard) and
// asserts no control-plane pod restarted. Skips itself unless tenancy.mode=cluster.
func TestClusterTenantAutoOnboard(t *testing.T) {
	// Not t.Parallel(): see the doc comment. 15m covers the control-plane-stable
	// wait + a cold-start invoke (poolmgr spin-up + first specialization) in a
	// freshly auto-onboarded namespace, after the other serial tests.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)

	f := framework.Connect(t)
	if !f.ClusterTenancyEnabled(t, ctx) {
		t.Skip("tenancy.mode is not cluster; this leg runs a different tenancy model")
	}
	pyImage := f.Images().RequirePython(t)

	// Settle any rollout from a prior serial test, then snapshot the control plane:
	// auto-onboarding a new namespace must restart nothing.
	f.WaitForControlPlaneStable(t, ctx, 3*time.Minute)
	before := f.ControlPlanePodUIDs(t, ctx)

	// A namespace that did not exist at install time and is NEVER explicitly
	// enabled — the auto-onboard path's reason to exist.
	tenantNS := "fission-cluster-e2e-" + framework.RandomID()
	ns := f.NewTestNamespaceIn(t, ctx, tenantNS)

	// 1. Creating the namespace is the only trigger: the controller auto-materializes
	//    a FissionTenant and provisions the namespace's fetcher/builder RBAC + key.
	//    We assert readiness WITHOUT calling EnableTenant — that is the difference
	//    from dynamic mode.
	f.WaitForTenantReady(t, ctx, tenantNS)

	// 2. The fetcher's grant must be a per-namespace RoleBinding to the
	//    fetcher-tenant-workload ClusterRole — least privilege. It must NOT be a
	//    ClusterRoleBinding (which would let the fetcher read Secrets cluster-wide).
	rb, err := f.KubeClient().RbacV1().RoleBindings(tenantNS).Get(ctx, fv1.FissionFetcherSA, metav1.GetOptions{})
	require.NoError(t, err, "the controller must provision a per-namespace fetcher RoleBinding")
	assert.Equal(t, "ClusterRole", rb.RoleRef.Kind, "fetcher RoleBinding references a ClusterRole by name")
	assert.Equal(t, fv1.FetcherTenantWorkloadClusterRole, rb.RoleRef.Name, "fetcher binds the narrow fetcher-tenant-workload rules")

	crbs, err := f.KubeClient().RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "list ClusterRoleBindings")
	for i := range crbs.Items {
		for _, s := range crbs.Items[i].Subjects {
			if s.Kind == "ServiceAccount" && s.Name == fv1.FissionFetcherSA && s.Namespace == tenantNS {
				t.Fatalf("fetcher SA in %q is bound cluster-wide by ClusterRoleBinding %q — cluster mode must keep fetcher per-namespace",
					tenantNS, crbs.Items[i].Name)
			}
		}
	}

	// 3. Run a function in the auto-onboarded namespace, end to end.
	envName := "python-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: pyImage, Poolsize: 1})

	fnName := "hello-" + ns.ID
	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})

	routeURL := "/" + fnName
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routeURL, Method: "GET"})

	// The namespace-explicit internal route proves the executor specialized a pod in
	// the auto-onboarded namespace (whose fetcher used the namespace's own derived
	// HMAC key), and the public HTTPTrigger proves the router admitted the trigger.
	r := f.Router(t)
	body := r.GetEventually(t, ctx, "/fission-function/"+tenantNS+"/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, strings.ToLower(body), "hello")
	r.GetEventually(t, ctx, routeURL, framework.BodyContains("hello"))

	// 4. Auto-onboarding + serving a fresh namespace must not have rolled any
	//    control-plane pod.
	f.AssertNoControlPlaneRestart(t, ctx, before)

	// 5. Cleanup: the namespace (and its owner-referenced FissionTenant + RBAC) is
	//    removed by the NewTestNamespaceIn cleanup hook.
}
