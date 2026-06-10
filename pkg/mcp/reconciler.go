// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// FunctionToolReconciler keeps the in-memory tool registry and the shared MCP
// server's tool set in sync with the Function CRDs. It mirrors the timer's
// reconciler: cache-backed client.Get, IsNotFound → remove, and a best-effort
// ToolExposed status condition. The GenerationChangedPredicate (applied in
// controller.Register) drops the status-only updates this reconciler writes.
//
// It runs on every replica (no leader election): each replica serves tools/list
// from its own registry, so each must reconcile. The work is idempotent registry
// mutation, so concurrent replicas do not conflict.
type FunctionToolReconciler struct {
	logger logr.Logger
	client client.Client
	reg    *Registry
	server *Server
}

func (r *FunctionToolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			r.removeTool(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Not exposed (Tool nil): ensure any prior tool is gone and do not assert
	// ToolExposed.
	if fn.Spec.Tool == nil {
		r.removeTool(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	entry := toolEntryFromFunction(fn)
	res, oldName, evicted := r.reg.Upsert(entry)

	if res == UpsertConflict {
		// The desired tool name is owned by a lexicographically-smaller function.
		// Don't advertise a hijacked name; drop any prior registration for this
		// function and surface the conflict so it's visible via kubectl.
		r.removeTool(req.NamespacedName)
		r.setConflict(ctx, fn, entry.ToolName)
		return ctrl.Result{}, nil
	}

	if evicted != nil {
		// This function won a contested name from a prior owner. Mark the loser
		// not-exposed so its condition doesn't lag at True until it next reconciles
		// (best-effort; its own reconcile will reach the same result).
		r.markEvicted(ctx, *evicted, entry.ToolName)
	}

	if oldName != "" && oldName != entry.ToolName {
		// ToolName changed: drop the stale registration before adding the new one.
		r.server.ApplyToolDelta(nil, []string{oldName})
	}
	if res == UpsertApplied {
		r.server.ApplyToolDelta([]ToolEntry{entry}, nil)
	}

	// Best-effort condition; never gates exposure. SetConditions skips the write
	// when nothing changed.
	controller.SetConditions(ctx, r.logger, r.client, fn, metav1.Condition{
		Type:    fv1.FunctionConditionToolExposed,
		Status:  metav1.ConditionTrue,
		Reason:  fv1.FunctionReasonToolExposed,
		Message: "exposed as MCP tool " + entry.ToolName,
	})
	return ctrl.Result{}, nil
}

// removeTool drops a function's tool from the registry and the server if present.
func (r *FunctionToolReconciler) removeTool(nn types.NamespacedName) {
	if oldName, existed := r.reg.RemoveByFunction(nn); existed {
		r.server.ApplyToolDelta(nil, []string{oldName})
	}
}

// setConflict marks a function not-exposed because its tool name is taken.
func (r *FunctionToolReconciler) setConflict(ctx context.Context, fn *fv1.Function, toolName string) {
	controller.SetConditions(ctx, r.logger, r.client, fn, metav1.Condition{
		Type:    fv1.FunctionConditionToolExposed,
		Status:  metav1.ConditionFalse,
		Reason:  fv1.FunctionReasonToolNameConflict,
		Message: "MCP tool name " + toolName + " is already used by another function",
	})
}

// markEvicted best-effort marks the function that just lost a contested tool
// name not-exposed. It re-fetches the object (it may have changed or been
// deleted); failures are non-gating since the loser's own reconcile reaches the
// same conclusion.
func (r *FunctionToolReconciler) markEvicted(ctx context.Context, nn types.NamespacedName, toolName string) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, nn, fn); err != nil {
		return
	}
	r.setConflict(ctx, fn, toolName)
}
