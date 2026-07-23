// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// functionAliasReconciler and functionVersionReconciler are the RFC-0025
// router-side counterparts to httpTriggerReconciler/functionReconciler: each
// replica reconciles FunctionAlias/FunctionVersion CRs into the route table
// via the incremental path (incremental.go), materializing the `:<alias>`/
// `:<version>` internal routes and — for aliases — cascading a repoint to
// every trigger consuming it (TriggersForAlias).
//
// They own NO status: FunctionAlias.Status.ResolvedVersion is written
// exclusively by the leader-elected pkg/versioning.AliasReconciler
// (buildermgr). This reconciler only READS spec+status to decide what to
// serve — no multi-writer conflict.
//
// Registered with RegisterTenantScopedWithPredicates and NO predicates
// (permissive: every Create/Update/Delete admits). This is deliberate,
// unlike httpTriggerReconciler/functionReconciler's default
// GenerationChangedPredicate (via RegisterTenantScoped): a FunctionAlias
// repoint is a Status-subresource write (Status.ResolvedVersion), which does
// NOT bump ObjectMeta.Generation — a GenerationChangedPredicate would drop
// every alias-resolver update and the router would never learn of a
// repoint. FunctionVersion is immutable after creation (types.go: "carries
// no Status: its content is fixed at creation"), so the predicate choice is
// moot there, but the same permissive registration is used for consistency
// and because a future FunctionVersion field would then already be observed.

// functionAliasReconciler reconciles one FunctionAlias into the route table.
type functionAliasReconciler struct {
	logger logr.Logger
	client client.Client
	ts     *HTTPTriggerSet
}

func (r *functionAliasReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	alias := &fv1.FunctionAlias{}
	if err := r.client.Get(ctx, req.NamespacedName, alias); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: drop the materialized :<alias> internal route and
			// re-apply the triggers that consumed it (they go unresolved and
			// their routes drop, via the existing errFunctionNotFound path).
			return ctrl.Result{}, r.ts.deleteAliasIncremental(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}
	// Per-event: cascade to every trigger resolving through this alias
	// (re-resolve + HandlerSwapped — a repoint never rebuilds a mux) and
	// upsert/refresh its internal route (insert = shape change, repoint =
	// handler swap).
	_, err := r.ts.applyAliasIncremental(ctx, alias)
	return ctrl.Result{}, err
}

// functionVersionReconciler reconciles one FunctionVersion into the route
// table's materialized `:<version>` internal route.
type functionVersionReconciler struct {
	logger logr.Logger
	client client.Client
	ts     *HTTPTriggerSet
}

func (r *functionVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	v := &fv1.FunctionVersion{}
	if err := r.client.Get(ctx, req.NamespacedName, v); err != nil {
		if apierrors.IsNotFound(err) {
			// FunctionVersion carries no function name once deleted; the
			// route is found by namespace+suffix (the version CR's own name),
			// scoped to whichever function still actually owns that suffix.
			return ctrl.Result{}, r.ts.deleteVersionIncremental(ctx, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}
	_, err := r.ts.applyVersionIncremental(ctx, v)
	return ctrl.Result{}, err
}
