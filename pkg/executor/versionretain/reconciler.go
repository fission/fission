// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versionretain

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// reconciler drives View.Rebuild on any FunctionAlias or FunctionVersion
// event. Both watches below share this Reconcile: it lists both kinds fresh
// from the Manager cache and rebuilds the whole set every time, so the
// reconciler itself carries no incremental diffing state (see View's doc for
// why full recompute is the chosen trade-off). The request that triggered a
// given reconcile is not otherwise used.
type reconciler struct {
	client client.Client
	view   *View
}

func (r *reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var aliases fv1.FunctionAliasList
	if err := r.client.List(ctx, &aliases); err != nil {
		return ctrl.Result{}, err
	}
	var versions fv1.FunctionVersionList
	if err := r.client.List(ctx, &versions); err != nil {
		return ctrl.Result{}, err
	}
	r.view.Rebuild(aliases.Items, versions.Items)
	return ctrl.Result{}, nil
}

// RegisterReconcilers wires view onto the executor Manager: one controller
// watching FunctionVersion, one watching FunctionAlias, both driving the same
// full-recompute Reconcile. Registered like funcreconciler/envreconciler —
// tenant-scoped, so a cluster-wide Fission-CRD cache (dynamic/cluster tenancy)
// only reconciles events in live tenant namespaces.
func RegisterReconcilers(mgr ctrl.Manager, view *View) error {
	r := &reconciler{client: mgr.GetClient(), view: view}

	// FunctionVersion is immutable after creation (see its type doc): the
	// default GenerationChangedPredicate is fine here since Create/Delete
	// always pass and no in-place Update ever happens.
	if err := controller.RegisterTenantScoped(mgr, &fv1.FunctionVersion{}, r, "executor-versionretain-version"); err != nil {
		return err
	}

	// FunctionAlias's status.resolvedVersion is a /status subresource write —
	// Generation is unchanged, so the default GenerationChangedPredicate would
	// drop exactly the event that moves the retain set for a digest-pinned
	// alias (Spec.PackageDigest resolved asynchronously to a version name).
	// Alias events are rare (moved by a human, `spec apply`, or the
	// version-control loop — never per-request), so admit every event rather
	// than hand-picking which spec/status fields matter.
	if err := controller.RegisterTenantScopedWithPredicates(mgr, &fv1.FunctionAlias{}, r, "executor-versionretain-alias", 0); err != nil {
		return err
	}
	return nil
}
