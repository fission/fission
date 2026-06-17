// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
)

// Helpers for the multi-namespace tenancy (dynamic onboarding) lifecycle test.
// They only do something meaningful when the cluster runs the dynamic model;
// DynamicNamespacesEnabled lets a test skip itself otherwise, mirroring the
// RFC-0002/0013 gate-detection helpers (RouterEndpointSliceMode etc.).

// DynamicNamespacesEnabled reports whether the cluster runs the dynamic
// multi-namespace tenancy model, read from FISSION_DYNAMIC_NAMESPACES on the
// executor Deployment (the chart sets it on every control-plane Deployment via
// the fission-resource-namespace.envs helper). The tenancy lifecycle test skips
// itself when it is off, so the static-namespace CI leg stays a no-op for it.
func (f *Framework) DynamicNamespacesEnabled(t *testing.T, ctx context.Context) bool {
	t.Helper()
	dep, err := f.kubeClient.AppsV1().Deployments(f.FissionNamespace()).Get(ctx, executorDeploymentName, metav1.GetOptions{})
	require.NoErrorf(t, err, "DynamicNamespacesEnabled: get executor Deployment")
	v, _ := executorEnvValue(dep, "FISSION_DYNAMIC_NAMESPACES")
	// Parse like production (utils.DynamicNamespacesEnabled) so "1"/"True"/etc.
	// agree with the cluster's own interpretation, not just the chart's "true".
	on, _ := strconv.ParseBool(v)
	return on
}

// NewTestNamespaceIn creates a real Kubernetes namespace and returns a
// TestNamespace bound to it, so the standard Create* helpers (which target
// ns.Name) operate inside that namespace instead of `default`. It is the entry
// point for the dynamic-tenancy lifecycle test, which onboards a fresh namespace
// at runtime.
//
// The cleanup hook mirrors NewTestNamespace — dump diagnostics on failure, then
// run resource cleanups LIFO (which delete the FissionTenant, functions, etc.) —
// and finally deletes the namespace itself, which also reaps anything a cleanup
// missed. Skipped when TEST_NOCLEANUP=1.
func (f *Framework) NewTestNamespaceIn(t *testing.T, ctx context.Context, name string) *TestNamespace {
	t.Helper()
	_, err := f.kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	require.NoErrorf(t, err, "NewTestNamespaceIn: create namespace %q", name)

	ns := &TestNamespace{f: f, t: t, Name: name, ID: randomID()}
	t.Cleanup(func() {
		if t.Failed() {
			ns.dumpDiagnostics()
		}
		if noCleanup() {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		// LIFO, so the FissionTenant (registered by EnableTenant) drains via its
		// finalizer while the namespace still exists, before we delete the namespace.
		for i := len(ns.cleanups) - 1; i >= 0; i-- {
			c := ns.cleanups[i]
			if err := c.fn(cctx); err != nil {
				t.Logf("cleanup %s: %v", c.name, err)
			}
		}
		if err := f.kubeClient.CoreV1().Namespaces().Delete(cctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Logf("cleanup: delete namespace %q: %v", name, err)
		}
	})
	return ns
}

// EnableTenant onboards this namespace as a Fission tenant via `fission tenant
// enable`, and registers cleanup that deletes the FissionTenant (cluster-scoped,
// so the namespace deletion does not reap it). The controller then provisions
// per-namespace RBAC, ServiceAccounts and the derived-key Secret; pair with
// WaitForTenantReady to block until that is done.
func (ns *TestNamespace) EnableTenant(t *testing.T, ctx context.Context) {
	t.Helper()
	ns.CLI(t, ctx, "tenant", "enable", "--namespace", ns.Name)
	ns.addCleanup("tenant "+ns.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().FissionTenants().Delete(c, ns.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

// DisableTenant offboards this namespace via `fission tenant disable`. The
// command is safe by default — it refuses while the namespace still has
// Functions — so callers must delete their functions first. The controller then
// drains the provisioned RBAC/Secret via its finalizer (see
// WaitForTenantOffboarded).
func (ns *TestNamespace) DisableTenant(t *testing.T, ctx context.Context) {
	t.Helper()
	ns.CLI(t, ctx, "tenant", "disable", "--namespace", ns.Name)
}

// WaitForTenantReady blocks until the FissionTenant for namespace reports
// Ready=True and the controller has provisioned the landmarks a function pod
// needs there: the fission-fetcher ServiceAccount and the derived-key Secret
// (mounted as a REQUIRED ref under dynamic tenancy, so the kubelet blocks pod
// start until it exists). Checking the concrete objects — not just the rollup
// condition — gives a precise failure if one provisioning step lags.
func (f *Framework) WaitForTenantReady(t *testing.T, ctx context.Context, namespace string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ft, err := f.fissionClient.CoreV1().FissionTenants().Get(ctx, namespace, metav1.GetOptions{})
		if !assert.NoErrorf(c, err, "get FissionTenant %q", namespace) {
			return
		}
		cond := conditions.Find(ft.Status.Conditions, fv1.FissionTenantConditionReady)
		if assert.NotNilf(c, cond, "FissionTenant %q has no Ready condition yet", namespace) {
			assert.Equalf(c, metav1.ConditionTrue, cond.Status,
				"FissionTenant %q not Ready (reason=%s message=%s)", namespace, cond.Reason, cond.Message)
		}
		_, err = f.kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, fv1.FissionFetcherSA, metav1.GetOptions{})
		assert.NoErrorf(c, err, "fetcher ServiceAccount not provisioned in %q yet", namespace)
		_, err = f.kubeClient.CoreV1().Secrets(namespace).Get(ctx, fv1.TenantAuthKeysSecret, metav1.GetOptions{})
		assert.NoErrorf(c, err, "derived-key Secret not provisioned in %q yet", namespace)
	}, 2*time.Minute, 2*time.Second)
}

// WaitForTenantOffboarded blocks until the FissionTenant is gone (its finalizer
// released) and the controller-provisioned derived-key Secret has been removed —
// proving the offboard finalizer drained the namespace.
func (f *Framework) WaitForTenantOffboarded(t *testing.T, ctx context.Context, namespace string) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := f.fissionClient.CoreV1().FissionTenants().Get(ctx, namespace, metav1.GetOptions{})
		assert.Truef(c, apierrors.IsNotFound(err),
			"FissionTenant %q should be deleted after offboarding (err=%v)", namespace, err)
		_, err = f.kubeClient.CoreV1().Secrets(namespace).Get(ctx, fv1.TenantAuthKeysSecret, metav1.GetOptions{})
		assert.Truef(c, apierrors.IsNotFound(err),
			"derived-key Secret in %q should be removed on offboarding (err=%v)", namespace, err)
	}, 2*time.Minute, 2*time.Second)
}

// WaitForControlPlaneStable blocks until every Deployment in the Fission release
// namespace has fully rolled out (observedGeneration current, all replicas
// updated and available, none unavailable). The serial suite's other tests (e.g.
// AdoptExistingResources) restart the executor and restore it WITHOUT waiting on
// the restore rollout, so a rollout can still be in flight when this test starts;
// snapshotting the control plane mid-rollout would make AssertNoControlPlane
// Restart flake. Call this before ControlPlanePodUIDs.
func (f *Framework) WaitForControlPlaneStable(t *testing.T, ctx context.Context, timeout time.Duration) {
	t.Helper()
	ns := f.FissionNamespace()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		deps, err := f.kubeClient.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if !assert.NoError(c, err, "list control-plane deployments") {
			return
		}
		// Guard against a vacuous pass: an empty list would skip the loop and
		// satisfy EventuallyWithT on the first tick, certifying "stable" without
		// checking anything (e.g. a wrong FISSION_NAMESPACE). The replicas check
		// below assumes no HPA-driven scaling in the release namespace (replicas
		// are Deployment-owned in kind-ci); a future control-plane HPA would make
		// .Spec.Replicas HPA-owned and need rethinking here.
		if !assert.NotEmptyf(c, deps.Items, "no Deployments found in Fission namespace %q", ns) {
			return
		}
		for i := range deps.Items {
			d := &deps.Items[i]
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			st := d.Status
			assert.GreaterOrEqualf(c, st.ObservedGeneration, d.Generation, "%s: rollout not observed", d.Name)
			assert.Equalf(c, want, st.UpdatedReplicas, "%s: rollout in progress (updated replicas)", d.Name)
			assert.Equalf(c, want, st.AvailableReplicas, "%s: available replicas", d.Name)
			assert.Equalf(c, want, st.Replicas, "%s: old pod still terminating (total replicas)", d.Name)
			assert.Zerof(c, st.UnavailableReplicas, "%s: unavailable replicas", d.Name)
		}
	}, timeout, 2*time.Second)
}

// ControlPlanePodUIDs snapshots the running control-plane pods (everything in
// the Fission release namespace) as name→UID. Pair with AssertNoControlPlane
// Restart to prove an operation — onboarding a tenant — restarted nothing, the
// zero-disruption guarantee that motivates dynamic tenancy (#3298). A container
// crash-restart keeps the pod UID (only restartCount bumps), so this flags only
// genuine pod recreation (a rollout), not a transient liveness blip.
func (f *Framework) ControlPlanePodUIDs(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	pods, err := f.kubeClient.CoreV1().Pods(f.FissionNamespace()).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "ControlPlanePodUIDs: list pods")
	uids := make(map[string]string)
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase == corev1.PodRunning {
			uids[p.Name] = string(p.UID)
		}
	}
	require.NotEmpty(t, uids, "ControlPlanePodUIDs: expected running control-plane pods in %q", f.FissionNamespace())
	return uids
}

// AssertNoControlPlaneRestart fails (non-fatally) if any pod captured in `before`
// is gone or was replaced (different UID) — i.e. a control-plane Deployment
// rolled. New pods appearing is allowed; the guarantee is that onboarding does
// not disturb the pods already serving.
func (f *Framework) AssertNoControlPlaneRestart(t *testing.T, ctx context.Context, before map[string]string) {
	t.Helper()
	pods, err := f.kubeClient.CoreV1().Pods(f.FissionNamespace()).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "AssertNoControlPlaneRestart: list pods")
	now := make(map[string]string, len(pods.Items))
	for i := range pods.Items {
		now[pods.Items[i].Name] = string(pods.Items[i].UID)
	}
	for name, uid := range before {
		got, ok := now[name]
		if !assert.Truef(t, ok, "control-plane pod %q was restarted/removed during tenant onboarding (#3298 regression)", name) {
			continue
		}
		assert.Equalf(t, uid, got,
			"control-plane pod %q was replaced (UID changed) during tenant onboarding (#3298 regression)", name)
	}
}
