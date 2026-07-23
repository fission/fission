// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/versioning"
)

// errAliasUnresolved marks resolveEntry's "the alias exists but has nothing
// resolvable behind it right now" outcome: the FunctionAlias/FunctionVersion
// isn't found yet, or the alias has never resolved a target
// (Spec.Version/Status.ResolvedVersion both empty). It is never a
// reconcile-ending error -- Reconcile handles it by keeping the last-known
// entry serving (or, if there is none yet, falling back to the live
// function's own Tool config) rather than failing or removing the tool.
var errAliasUnresolved = errors.New("function alias has not resolved a target yet")

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

	entry, err := r.resolveEntry(ctx, fn)
	// fallback marks that this reconcile is serving entry from the live
	// function's own Tool config because the alias it names has never
	// resolved -- NOT from the alias's resolved snapshot. Threaded through to
	// the ToolExposed condition below (distinct Reason) so an operator can
	// tell snapshot-serving from fallback-serving via kubectl without reading
	// logs; it flips back to the normal Reason on the reconcile after the
	// alias first resolves, since that pass takes the non-error branch above
	// and fallback stays false.
	fallback := false
	if errors.Is(err, errAliasUnresolved) {
		if r.reg.HasFunction(req.NamespacedName) {
			// Keep the last tool entry serving -- mirrors the router's own
			// eventual-consistency posture for an alias mid-resolve (or
			// momentarily broken) rather than yanking a working tool out from
			// under an agent for a transient condition. The condition is left
			// untouched too: whatever Reason the last successful reconcile
			// wrote (ToolExposed or ToolAliasFallback) already accurately
			// describes what's still being served.
			r.logger.V(1).Info("alias target unresolved; keeping last-known tool entry",
				"function", req.NamespacedName, "alias", fn.Spec.Tool.Alias)
			return ctrl.Result{}, nil
		}
		// Never resolved before: fall back to the live function's own Tool
		// config so a tool is advertised immediately rather than staying
		// invisible until the alias first resolves. Clear Alias so the entry
		// proxies straight to the live function -- toolEntryFromFunction sets
		// it from tc.Alias unconditionally, but routing tools/call through a
		// ":<alias>" route the router has never materialized would just 404
		// the whole time the alias stays unresolved, which defeats the point
		// of advertising a working fallback tool at all.
		entry = toolEntryFromFunction(fn)
		entry.Alias = ""
		fallback = true
	} else if err != nil {
		return ctrl.Result{}, err
	}

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

	// Best-effort condition; never gates exposure. SetConditions skips the
	// write when nothing changed -- which is also what makes the reason flip
	// back to FunctionReasonToolExposed a normal, observable write: the
	// reconcile right after the alias first resolves takes the non-fallback
	// branch above, so this call's Reason differs from what's currently on
	// Status and the update actually lands.
	reason := fv1.FunctionReasonToolExposed
	message := "exposed as MCP tool " + entry.ToolName
	if fallback {
		reason = fv1.FunctionReasonToolAliasFallback
		message = fmt.Sprintf("exposed as MCP tool %s: alias %q has not resolved a target yet; serving this function's live Tool config directly instead of the alias's snapshot",
			entry.ToolName, fn.Spec.Tool.Alias)
	}
	controller.SetConditions(ctx, r.logger, r.client, fn, metav1.Condition{
		Type:    fv1.FunctionConditionToolExposed,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
	return ctrl.Result{}, nil
}

// resolveEntry builds the ToolEntry to advertise for fn (which must carry a
// non-nil Tool). A bare Tool (Alias empty) is built straight from the live
// function, unchanged from pre-RFC-0025 behavior.
//
// An alias-addressed Tool (Alias set) is instead built from the ALIAS'S
// RESOLVED VERSION's snapshot: Get the named FunctionAlias, compute its
// effective target (Spec.Version if name-pinned, else
// Status.ResolvedVersion -- the same precedence
// pkg/router/functionReferenceResolver.go's resolveByAlias uses), Get that
// FunctionVersion, and build the entry from
// versioning.VersionedFunction(fn, v) -- toolEntryFromFunction works
// unchanged on that projection, so a schema/description change published
// into a new version is what tools/list actually advertises, not
// necessarily what the live spec currently says. entry.Alias is then forced
// to tc.Alias (the alias CR's own name, which this call just confirmed
// exists) rather than trusting whatever the snapshot itself recorded, since
// a stale snapshot's own Tool.Alias could in principle differ.
//
// Returns errAliasUnresolved (never wrapped with detail the caller needs)
// when the alias or its target isn't resolvable yet -- see Reconcile for how
// that is handled.
func (r *FunctionToolReconciler) resolveEntry(ctx context.Context, fn *fv1.Function) (ToolEntry, error) {
	tc := fn.Spec.Tool
	if tc.Alias == "" {
		return toolEntryFromFunction(fn), nil
	}

	alias := &fv1.FunctionAlias{}
	err := r.client.Get(ctx, types.NamespacedName{Namespace: fn.Namespace, Name: tc.Alias}, alias)
	if apierrors.IsNotFound(err) {
		return ToolEntry{}, errAliasUnresolved
	}
	if err != nil {
		return ToolEntry{}, err
	}

	target := alias.Spec.Version
	if target == "" {
		target = alias.Status.ResolvedVersion
	}
	if target == "" {
		return ToolEntry{}, errAliasUnresolved
	}

	v := &fv1.FunctionVersion{}
	err = r.client.Get(ctx, types.NamespacedName{Namespace: fn.Namespace, Name: target}, v)
	if apierrors.IsNotFound(err) {
		// The version the alias named was GC'd (or never existed) between the
		// alias resolving and this reconcile -- transient, not a hard error.
		return ToolEntry{}, errAliasUnresolved
	}
	if err != nil {
		return ToolEntry{}, err
	}

	entry := toolEntryFromFunction(versioning.VersionedFunction(fn, v))
	entry.Alias = tc.Alias
	return entry, nil
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
