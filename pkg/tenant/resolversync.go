// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// SyncResolverFromTenants sets the resolver's live tenant set to the env seed
// plus every FissionTenant's namespace. It is shared by the tenant controller
// (which also provisions RBAC/keys) and the data-plane resolver-sync (read-only),
// so the live set is computed identically in every process. The env seed
// (utils.GetNamespaces) keeps the static FISSION_RESOURCE_NAMESPACES entries as a
// base, so dynamic onboarding is additive and removing a FissionTenant never
// drops an env-configured namespace.
func SyncResolverFromTenants(ctx context.Context, c client.Client, resolver *utils.NamespaceResolver) error {
	list := &fv1.FissionTenantList{}
	if err := c.List(ctx, list); err != nil {
		return err
	}
	set := utils.GetNamespaces()
	for i := range list.Items {
		if ns := list.Items[i].Spec.Namespace; ns != "" {
			set[ns] = ns
		}
	}
	resolver.SetTenants(set)
	return nil
}

// ResolverSyncReconciler keeps a data-plane manager's in-process resolver in step
// with the FissionTenant set WITHOUT doing any provisioning (that is the tenant
// controller's job, and the privilege the data plane must not hold). Every
// cluster-wide data-plane subsystem (router, buildermgr, and the trigger managers
// via crmanager.NewLeaderElected) runs one under dynamic tenancy: it is the
// cross-process half of dynamic onboarding — a namespace onboarded at runtime
// reaches each manager's membership predicate (controller.MembershipPredicate
// reads the same resolver) without a restart. The executor is the deliberate
// exception: it keeps a per-namespace cache until its cluster-wide-cache
// provisioning phase lands, so it has nothing cluster-wide to admit yet.
type ResolverSyncReconciler struct {
	client   client.Client
	resolver *utils.NamespaceResolver
	logger   logr.Logger
}

func (r *ResolverSyncReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if err := SyncResolverFromTenants(ctx, r.client, r.resolver); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// AddResolverSync registers the resolver-sync reconciler on mgr, watching
// FissionTenant. Call it from a data-plane subsystem's Start under dynamic
// tenancy (utils.DynamicNamespacesEnabled). The manager's cache is cluster-wide
// in that mode, so the cluster-scoped FissionTenant watch needs no extra cache
// wiring; the component's ClusterRole must grant fissiontenants get/list/watch
// (charts/.../tenant-controller/dynamic-cluster-roles.yaml).
func AddResolverSync(mgr ctrl.Manager, resolver *utils.NamespaceResolver, logger logr.Logger) error {
	r := &ResolverSyncReconciler{client: mgr.GetClient(), resolver: resolver, logger: logger.WithName("resolver-sync")}
	return builder.ControllerManagedBy(mgr).
		For(&fv1.FissionTenant{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("fission-resolver-sync").
		Complete(r)
}
