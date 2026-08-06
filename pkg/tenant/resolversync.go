// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"sync/atomic"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// hookRetryDelay is the FIRST poll interval used when a hook reports pending
// work (e.g. the router waiting on a tenant's fission-router-dataplane
// RoleBinding, which the tenant controller provisions asynchronously — no
// FissionTenant event fires when it lands). An explicit interval is used
// instead of Result.Requeue because Requeue drives the workqueue rate limiter,
// whose exponential backoff caps at ~1000s and would leave a namespace
// unadmitted for ~16 minutes after the binding finally lands. Requeue is also
// deprecated in controller-runtime v0.24.x; when it is removed, Requeue:true
// silently becomes a no-op and the wedge returns with no compile error.
//
// The interval doubles on each consecutive pending reconcile up to
// hookRetryMaxDelay, then holds. That keeps the common case — RBAC landing a
// second or two after the event — converging in ~2s, while a namespace that
// can never pass (an orphaned FissionTenant whose namespace was deleted) settles
// into a cheap 30s poll instead of re-checking every 2s forever. At a flat 2s
// such a tenant costs ~43k reconciles, SelfSubjectAccessReviews and log lines
// per replica per day; capped at 30s that drops to ~2.9k.
const (
	hookRetryDelay    = 2 * time.Second
	hookRetryMaxDelay = 30 * time.Second
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
// cluster-wide data-plane subsystem (router, buildermgr, executor, and the
// trigger managers via crmanager.NewLeaderElected) runs one under dynamic
// tenancy: it is the cross-process half of dynamic onboarding — a namespace
// onboarded at runtime reaches each manager's membership predicate
// (controller.MembershipPredicate reads the same resolver) without a restart.
type ResolverSyncReconciler struct {
	client   client.Client
	resolver *utils.NamespaceResolver
	// hooks are called after every successful SetTenants. See AddResolverSync
	// for the hook/pending contract.
	hooks []func() (pending bool)
	// pendingRounds counts consecutive reconciles whose hooks reported pending
	// work; it drives the escalating poll interval and resets as soon as one
	// reconcile comes back clean. Atomic because the manager may run this
	// reconciler with more than one worker.
	pendingRounds atomic.Int64
}

func (r *ResolverSyncReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if err := SyncResolverFromTenants(ctx, r.client, r.resolver); err != nil {
		return ctrl.Result{}, err
	}
	pending := false
	for _, h := range r.hooks {
		if h() {
			pending = true
		}
	}
	if pending {
		return ctrl.Result{RequeueAfter: r.nextRetryDelay()}, nil
	}
	r.pendingRounds.Store(0)
	return ctrl.Result{}, nil
}

// nextRetryDelay returns the poll interval for the next pending retry: it
// doubles from hookRetryDelay on each consecutive pending reconcile and is
// clamped at hookRetryMaxDelay (2s, 4s, 8s, 16s, 30s, 30s, …). The shift is
// bounded before it is applied so the doubling can never overflow.
func (r *ResolverSyncReconciler) nextRetryDelay() time.Duration {
	const maxShift = 4 // hookRetryDelay<<4 == 32s, already past the cap
	shift := r.pendingRounds.Add(1) - 1
	if shift > maxShift {
		shift = maxShift
	}
	delay := hookRetryDelay << shift
	return min(delay, hookRetryMaxDelay)
}

// AddResolverSync registers the resolver-sync reconciler on mgr, watching
// FissionTenant. It is a no-op unless the Fission-CRD cache is cluster-wide
// (dynamic OR cluster mode), so every data-plane Start can call it
// unconditionally. Both modes need it: it is what keeps each process's resolver —
// and therefore IsTenant, which gates the membership predicate AND per-namespace
// HMAC key stamping — in step with the live FissionTenant set. The manager's cache
// is cluster-wide in these modes, so the cluster-scoped FissionTenant watch needs
// no extra cache wiring; the component's ClusterRole must grant fissiontenants
// get/list/watch (charts/.../tenant-controller/dynamic-cluster-roles.yaml).
//
// hooks are called after every successful SetTenants. The router passes one to
// re-scope its per-namespace EndpointSlice informers (#3647); executor/buildermgr
// pass none. A hook returns pending=true when it has unfinished work (e.g. RBAC
// not yet landed); the reconciler then requeues after an interval that starts at
// hookRetryDelay and doubles up to hookRetryMaxDelay while work stays pending.
// Variadic so existing callers (AddResolverSync(mgr)) compile unchanged.
func AddResolverSync(mgr ctrl.Manager, hooks ...func() (pending bool)) error {
	if !utils.CrdWatchClusterWide() {
		return nil
	}
	r := &ResolverSyncReconciler{client: mgr.GetClient(), resolver: utils.DefaultNSResolver(), hooks: hooks}
	return builder.ControllerManagedBy(mgr).
		For(&fv1.FissionTenant{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("fission-resolver-sync").
		Complete(r)
}
