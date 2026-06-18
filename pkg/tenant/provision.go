// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	managedByLabelKey = "app.kubernetes.io/managed-by"
	managedByValue    = "fission-tenant-controller"

	// These name the per-namespace RoleBindings the controller provisions (and
	// match the static chart's binding names). Each binds a fetcher/builder pod SA
	// to the fixed-name *TenantWorkloadClusterRole ClusterRole that carries its
	// rules — the controller no longer authors Roles of its own.
	fetcherRoleName          = "fission-fetcher"
	builderRoleName          = "fission-builder"
	fetcherWebsocketRoleName = "fission-fetcher-websocket"
)

// EnsureNamespaceRBAC creates (idempotently) the ServiceAccounts and RoleBindings
// a tenant namespace needs for the fetcher and builder to run — the dynamic,
// runtime equivalent of the chart's _function-access-role.tpl, for namespaces
// onboarded after install. Every object carries the managed-by label so
// offboarding can clean them up. The grants live in the chart's fixed-name
// ClusterRoles (rendered in dynamic mode); the controller only BINDS them by name
// into each tenant namespace — it needs rbac `bind` on those ClusterRoles, but no
// longer `escalate` (it authors no Role), and never read of the tenant's secrets.
func EnsureNamespaceRBAC(ctx context.Context, c client.Client, namespace, releaseNamespace string, owner metav1.OwnerReference) error {
	for _, obj := range namespaceRBACObjects(namespace, releaseNamespace, owner) {
		if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("provisioning %T %s/%s: %w", obj, namespace, obj.GetName(), err)
		}
	}
	return nil
}

// DeleteNamespaceRBAC removes the RBAC objects EnsureNamespaceRBAC created,
// selecting by the managed-by label so it never touches chart- or user-managed
// RBAC. Called on tenant offboard (via the finalizer). NotFound is ignored so it
// is safe to re-run.
func DeleteNamespaceRBAC(ctx context.Context, c client.Client, namespace string) error {
	opts := []client.DeleteAllOfOption{
		client.InNamespace(namespace),
		client.MatchingLabels{managedByLabelKey: managedByValue},
	}
	// RoleBindings before ServiceAccounts (reverse of creation). The label selector
	// means only controller-managed objects are removed — never chart- or
	// user-managed RBAC. DeleteAllOf issues a deletecollection request, so the
	// tenant-controller ClusterRole MUST grant `deletecollection` on these resources
	// (charts/.../tenant-controller/rbac.yaml) — `delete` alone is insufficient and
	// the finalizer would wedge on a forbidden error. The controller authors no
	// Role of its own (it binds the fixed-name *-tenant-workload ClusterRoles), so
	// Roles are not in the sweep.
	for _, proto := range []client.Object{&rbacv1.RoleBinding{}, &corev1.ServiceAccount{}} {
		if err := c.DeleteAllOf(ctx, proto, opts...); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting %T in %s: %w", proto, namespace, err)
		}
	}
	// The derived-key Secret is deleted by NAME, not by label: DeleteAllOf would
	// require secrets list/get cluster-wide (i.e. read of every secret) — exactly
	// the privilege the design withholds from the controller. A name-scoped delete
	// needs only the `delete` verb, so the controller can write/remove the auth
	// secret without ever being able to read tenant secrets. The name is the
	// controller-owned keysSecretName, never the chart's master copy, so teardown
	// cannot disturb a Helm-managed Secret.
	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: keysSecretName, Namespace: namespace}}
	if err := c.Delete(ctx, authSecret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting auth secret in %s: %w", namespace, err)
	}
	return nil
}

func namespaceRBACObjects(ns, releaseNamespace string, owner metav1.OwnerReference) []client.Object {
	labels := map[string]string{managedByLabelKey: managedByValue}
	meta := func(name string) metav1.ObjectMeta {
		m := metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}
		// Own the provisioned object by the FissionTenant (cluster-scoped, so it may
		// own these namespaced objects) as a GC backstop: even a force-delete that
		// strips the finalizer reaps the grant, so a workload binding can't outlive
		// its tenant. The finalizer (DeleteNamespaceRBAC) stays the primary path.
		if owner.UID != "" {
			m.OwnerReferences = []metav1.OwnerReference{owner}
		}
		return m
	}
	// clusterRoleBinding binds a fixed-name ClusterRole into THIS tenant namespace
	// with a namespaced RoleBinding (NOT a ClusterRoleBinding), so the grant is
	// scoped to the namespace. saNamespace is where the bound ServiceAccount lives:
	// the tenant namespace for the fetcher/builder pod SAs, the release namespace
	// for the executor/buildermgr control-plane SAs. The rules live ONLY in the
	// chart's shared partials (rendered as these ClusterRoles), so the static and
	// dynamic paths share one source of truth and the controller carries no copy.
	clusterRoleBinding := func(name, sa, saNamespace, clusterRole string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: meta(name),
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: saNamespace}},
		}
	}
	objs := []client.Object{
		&corev1.ServiceAccount{ObjectMeta: meta(fv1.FissionFetcherSA)},
		&corev1.ServiceAccount{ObjectMeta: meta(fv1.FissionBuilderSA)},

		// The fetcher/builder pod SAs (in THIS tenant namespace) bound to the
		// fixed-name ClusterRoles carrying their read rules (configmaps/secrets/
		// packages/serviceaccounts; events+pods for the websocket fetcher). The
		// chart's _function-access-role.tpl renders the SAME rules into the static
		// per-namespace Roles from the same partials, so the two paths cannot drift.
		clusterRoleBinding(fetcherRoleName, fv1.FissionFetcherSA, ns, fv1.FetcherTenantWorkloadClusterRole),
		clusterRoleBinding(builderRoleName, fv1.FissionBuilderSA, ns, fv1.BuilderTenantWorkloadClusterRole),
		clusterRoleBinding(fetcherWebsocketRoleName, fv1.FissionFetcherSA, ns, fv1.FetcherWebsocketTenantWorkloadClusterRole),
	}

	// Bind the executor and buildermgr (release-namespace SAs) to their workload
	// ClusterRoles so they can create/manage workloads in this tenant namespace —
	// the dynamic equivalent of the chart's per-namespace executor/buildermgr
	// kubernetes Roles. Skipped when the release namespace is unknown (the subject
	// would be unresolvable), in which case the executor/buildermgr fall back to
	// any statically-rendered Role for this namespace.
	if releaseNamespace != "" {
		objs = append(objs,
			clusterRoleBinding("fission-executor-workload", fv1.FissionExecutorSA, releaseNamespace, fv1.ExecutorTenantWorkloadClusterRole),
			clusterRoleBinding("fission-buildermgr-workload", fv1.FissionBuildermgrSA, releaseNamespace, fv1.BuildermgrTenantWorkloadClusterRole),
		)
	}
	return objs
}
