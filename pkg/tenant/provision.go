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

	fetcherRoleName          = "fission-fetcher"
	builderRoleName          = "fission-builder"
	fetcherWebsocketRoleName = "fission-fetcher-websocket"
)

// EnsureNamespaceRBAC creates (idempotently) the ServiceAccounts, Roles, and
// RoleBindings a tenant namespace needs for the fetcher and builder to run — the
// dynamic, runtime equivalent of the chart's _function-access-role.tpl, for
// namespaces onboarded after install. Every object carries the managed-by label
// so offboarding can clean them up. The Roles grant read-only access to the
// namespace's OWN ConfigMaps/Secrets/Packages (no cross-namespace, no
// escalation): a tenant controller holding rbac `escalate`/`bind` can mint these
// without itself being able to read those Secrets.
func EnsureNamespaceRBAC(ctx context.Context, c client.Client, namespace, releaseNamespace string) error {
	for _, obj := range namespaceRBACObjects(namespace, releaseNamespace) {
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
	// RoleBindings before Roles before ServiceAccounts (reverse of creation). The
	// label selector means only controller-managed objects are removed — never
	// chart- or user-managed RBAC.
	for _, proto := range []client.Object{&rbacv1.RoleBinding{}, &rbacv1.Role{}, &corev1.ServiceAccount{}} {
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

func namespaceRBACObjects(ns, releaseNamespace string) []client.Object {
	labels := map[string]string{managedByLabelKey: managedByValue}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}
	}
	roleBinding := func(name, sa string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: meta(name),
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: ns}},
		}
	}
	// clusterRoleBinding binds a release-namespace control-plane SA to a fixed
	// workload ClusterRole, scoped to THIS namespace by the RoleBinding — so the
	// executor / buildermgr can manage their workloads here, never cluster-wide.
	clusterRoleBinding := func(name, sa, clusterRole string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: meta(name),
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: releaseNamespace}},
		}
	}
	get := []string{"get"}
	objs := []client.Object{
		&corev1.ServiceAccount{ObjectMeta: meta(fv1.FissionFetcherSA)},
		&corev1.ServiceAccount{ObjectMeta: meta(fv1.FissionBuilderSA)},

		&rbacv1.Role{ObjectMeta: meta(fetcherRoleName), Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets"}, Verbs: get},
			{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, Verbs: get},
			{APIGroups: []string{"fission.io"}, Resources: []string{"packages"}, Verbs: get},
		}},
		&rbacv1.Role{ObjectMeta: meta(builderRoleName), Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"fission.io"}, Resources: []string{"packages"}, Verbs: get},
			{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets"}, Verbs: get},
		}},
		&rbacv1.Role{ObjectMeta: meta(fetcherWebsocketRoleName), Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: get},
		}},

		roleBinding(fetcherRoleName, fv1.FissionFetcherSA),
		roleBinding(builderRoleName, fv1.FissionBuilderSA),
		roleBinding(fetcherWebsocketRoleName, fv1.FissionFetcherSA),
	}

	// Bind the executor and buildermgr (release-namespace SAs) to their workload
	// ClusterRoles so they can create/manage workloads in this tenant namespace —
	// the dynamic equivalent of the chart's per-namespace executor/buildermgr
	// kubernetes Roles. Skipped when the release namespace is unknown (the subject
	// would be unresolvable), in which case the executor/buildermgr fall back to
	// any statically-rendered Role for this namespace.
	if releaseNamespace != "" {
		objs = append(objs,
			clusterRoleBinding("fission-executor-workload", fv1.FissionExecutorSA, fv1.ExecutorTenantWorkloadClusterRole),
			clusterRoleBinding("fission-buildermgr-workload", fv1.FissionBuildermgrSA, fv1.BuildermgrTenantWorkloadClusterRole),
		)
	}
	return objs
}
