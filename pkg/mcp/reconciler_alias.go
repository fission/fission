// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// FunctionAliasToolReconciler is the RFC-0025 counterpart to
// FunctionToolReconciler: it watches FunctionAlias CRs so an alias repoint
// (Status.ResolvedVersion, written by the leader-elected
// pkg/versioning.AliasReconciler) or retarget (Spec.Version) refreshes the
// MCP tool entries built from that alias's resolved snapshot.
// FunctionToolReconciler alone never observes this -- it watches Functions,
// and an alias's Status write does not touch any Function object.
//
// On every FunctionAlias event it lists Functions in the same namespace
// (tool-bearing functions are few; a List here is cheap) and re-runs the
// Function tool reconcile for each whose Tool.Alias names this alias --
// resolveEntry re-resolves against the alias's current target, and any
// resulting change goes through the same registry Upsert -> ApplyToolDelta
// path FunctionToolReconciler uses, so a repoint emits
// notifications/tools/list_changed exactly like any other tool change.
//
// Registered with RegisterTenantScopedWithPredicates and NO predicates
// (permissive: every Create/Update/Delete admits) -- for the same reason as
// the router's functionAliasReconciler (pkg/router/reconciler_alias.go):
// Status.ResolvedVersion is a status-subresource write and does not bump
// ObjectMeta.Generation, so the default GenerationChangedPredicate would
// silently drop every resolver repoint.
type FunctionAliasToolReconciler struct {
	logger logr.Logger
	client client.Client
	tool   *FunctionToolReconciler
}

func (r *FunctionAliasToolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// A delete needs no special handling here: the Functions that named this
	// alias now resolve through resolveEntry's errAliasUnresolved path (the
	// FunctionAlias Get returns NotFound) the next time anything reconciles
	// them, and Reconcile's fallback already keeps their last-known tool
	// entry serving -- there is no registry state owned by the alias itself
	// to clean up.
	fns := &fv1.FunctionList{}
	if err := r.client.List(ctx, fns, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	for i := range fns.Items {
		fn := &fns.Items[i]
		if fn.Spec.Tool == nil || fn.Spec.Tool.Alias != req.Name {
			continue
		}
		nn := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
		if _, err := r.tool.Reconcile(ctx, ctrl.Request{NamespacedName: nn}); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}
