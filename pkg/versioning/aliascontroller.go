// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/utils"
)

// aliasHistoryLimit bounds FunctionAliasStatus.History (types.go: "a bounded
// tail ... most recent last"). Oldest entries are dropped from the front once
// the cap is exceeded.
const aliasHistoryLimit = 10

// AliasReconciler resolves each FunctionAlias's spec target — name-pinned
// (Spec.Version) or digest-pinned (Spec.PackageDigest) — to a concrete
// FunctionVersion, writing Status.ResolvedVersion/Conditions and a bounded
// switch history. It also repairs two pieces of metadata that can drift or
// start out missing: the ownerRef back to the named Function and the
// VersionFunctionNameLabel used for alias→function filtering.
//
// It is registered directly via builder.ControllerManagedBy (not
// controller.RegisterTenantScoped) because, beyond FunctionAlias spec
// events, it must also watch FunctionVersion: a version appearing after the
// alias was created is what flips a digest-pinned alias's Resolved condition
// from False to True, and RegisterTenantScoped has no hook for a second
// watched type. See pkg/executor/funcreconciler for the same pattern.
type AliasReconciler struct {
	logger logr.Logger
	client client.Client
}

// RegisterAliasReconciler wires the RFC-0025 alias resolver onto mgr. Under
// dynamic/cluster-wide tenancy (utils.CrdWatchClusterWide) both watches are
// additionally scoped to live tenant namespaces via
// controller.MembershipPredicate, and a FissionTenant watch re-converges a
// namespace's aliases on onboarding — mirroring
// pkg/controller.RegisterTenantScoped for the types this reconciler cannot
// register through that helper.
func RegisterAliasReconciler(mgr ctrl.Manager, logger logr.Logger) error {
	r := &AliasReconciler{
		logger: logger.WithName("alias_reconciler"),
		client: mgr.GetClient(),
	}

	// GenerationChangedPredicate drops our own status-only writes (Status is
	// a subresource; it never bumps Generation), so this reconciler's status
	// patches never re-trigger themselves via the .For() watch.
	aliasPredicates := []predicate.Predicate{predicate.GenerationChangedPredicate{}}
	var versionPredicates []predicate.Predicate
	if utils.CrdWatchClusterWide() {
		mp := controller.MembershipPredicate(utils.DefaultNSResolver())
		aliasPredicates = append(aliasPredicates, mp)
		versionPredicates = append(versionPredicates, mp)
	}

	b := builder.ControllerManagedBy(mgr).
		Named("versioning-alias").
		For(&fv1.FunctionAlias{}, builder.WithPredicates(aliasPredicates...)).
		Watches(&fv1.FunctionVersion{}, handler.EnqueueRequestsFromMapFunc(r.mapVersionToAliases),
			builder.WithPredicates(versionPredicates...))

	if utils.CrdWatchClusterWide() {
		b = b.Watches(&fv1.FissionTenant{},
			controller.TenantReenqueueHandler(mgr.GetAPIReader(), mgr.GetScheme(), &fv1.FunctionAlias{}),
			builder.WithPredicates(controller.TenantOnboardPredicate()))
	}

	return b.Complete(r)
}

// mapVersionToAliases enqueues every FunctionAlias in a created/updated
// FunctionVersion's namespace that could plausibly be affected by it: one
// whose Spec.PackageDigest matches the version's recorded digest (a
// digest-pinned alias's exact resolution target just appeared), or one that
// is not currently Resolved (its unmatched target may now resolve — covers
// both digest-pinned aliases waiting on any matching version and name-pinned
// aliases waiting on a version that was recreated under the same name).
// Aliases for a different function are always skipped.
func (r *AliasReconciler) mapVersionToAliases(ctx context.Context, obj client.Object) []reconcile.Request {
	v, ok := obj.(*fv1.FunctionVersion)
	if !ok {
		return nil
	}

	aliases := &fv1.FunctionAliasList{}
	if err := r.client.List(ctx, aliases, client.InNamespace(v.Namespace)); err != nil {
		r.logger.V(1).Info("failed to list function aliases for function version watch",
			"namespace", v.Namespace, "version", v.Name, "error", err)
		return nil
	}

	var reqs []reconcile.Request
	for i := range aliases.Items {
		a := &aliases.Items[i]
		if a.Spec.FunctionName != v.Spec.FunctionName {
			continue
		}
		digestMatch := a.Spec.PackageDigest != "" && a.Spec.PackageDigest == v.Spec.PackageDigest
		unresolved := !conditions.IsTrue(a.Status.Conditions, fv1.FunctionAliasConditionResolved)
		if digestMatch || unresolved {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		}
	}
	return reqs
}

// Reconcile repairs alias metadata (ownerRef + label), resolves its spec
// target, and persists the outcome. Every step is guarded to a no-op write
// when nothing changed, so a reconcile driven by an unrelated event (or a
// duplicate delivery) performs zero API calls beyond the initial Get.
func (r *AliasReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	alias := &fv1.FunctionAlias{}
	if err := r.client.Get(ctx, req.NamespacedName, alias); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if err := r.repairMetadata(ctx, alias); err != nil {
		return reconcile.Result{}, err
	}

	resolvedVersion, resolvedOK, reason, err := r.resolve(ctx, alias)
	if err != nil {
		return reconcile.Result{}, err
	}

	if err := r.writeStatus(ctx, alias, resolvedVersion, resolvedOK, reason); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// repairMetadata backfills VersionFunctionNameLabel and repairs a
// missing/stale-UID ownerRef to the named Function, in a single guarded
// Update — a no-op when both are already correct. The Function is allowed to
// be absent (a dangling alias, or one that races Function creation): the
// ownerRef is simply left as-is in that case rather than treated as an
// error.
func (r *AliasReconciler) repairMetadata(ctx context.Context, alias *fv1.FunctionAlias) error {
	changed := false

	if alias.Labels[fv1.VersionFunctionNameLabel] != alias.Spec.FunctionName {
		if alias.Labels == nil {
			alias.Labels = make(map[string]string, 1)
		}
		alias.Labels[fv1.VersionFunctionNameLabel] = alias.Spec.FunctionName
		changed = true
	}

	fn := &fv1.Function{}
	err := r.client.Get(ctx, client.ObjectKey{Namespace: alias.Namespace, Name: alias.Spec.FunctionName}, fn)
	switch {
	case err == nil:
		want := fv1.FunctionOwnerRef(fn)
		if !hasOwnerRef(alias.OwnerReferences, want) {
			alias.OwnerReferences = upsertOwnerRef(alias.OwnerReferences, want)
			changed = true
		}
	case apierrors.IsNotFound(err):
		// Tolerate: nothing to repair against.
	default:
		return fmt.Errorf("versioning: getting function %s/%s for alias %s/%s ownerRef repair: %w",
			alias.Namespace, alias.Spec.FunctionName, alias.Namespace, alias.Name, err)
	}

	if !changed {
		return nil
	}
	if err := r.client.Update(ctx, alias); err != nil {
		return fmt.Errorf("versioning: updating alias %s/%s metadata: %w", alias.Namespace, alias.Name, err)
	}
	return nil
}

// hasOwnerRef reports whether refs already contains want, matched on
// Kind+Name+UID (an exact, non-stale reference).
func hasOwnerRef(refs []metav1.OwnerReference, want metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Kind == want.Kind && ref.Name == want.Name && ref.UID == want.UID {
			return true
		}
	}
	return false
}

// upsertOwnerRef replaces the first same-Kind+Name reference (a stale UID,
// e.g. the Function was deleted and recreated) with want, or appends want
// when no such reference exists (missing case).
func upsertOwnerRef(refs []metav1.OwnerReference, want metav1.OwnerReference) []metav1.OwnerReference {
	for i, ref := range refs {
		if ref.Kind == want.Kind && ref.Name == want.Name {
			refs[i] = want
			return refs
		}
	}
	return append(refs, want)
}

// resolve computes alias's current effective target. Name-pinned
// (Spec.Version) resolves to that FunctionVersion iff it still exists — a
// version can be garbage collected out from under a stale name-pinned alias.
// Digest-pinned (Spec.PackageDigest) resolves to the highest-Sequence
// FunctionVersion belonging to Spec.FunctionName whose recorded digest
// matches; ties cannot occur (Sequence is strictly increasing per function).
// A miss on either path reports ok=false without an error — the caller
// leaves Status.ResolvedVersion untouched, so the alias keeps serving its
// last resolved target (eventual consistency: the router does not need to
// know why resolution is currently unmet).
func (r *AliasReconciler) resolve(ctx context.Context, alias *fv1.FunctionAlias) (resolvedVersion string, ok bool, reason string, err error) {
	switch {
	case alias.Spec.Version != "":
		v := &fv1.FunctionVersion{}
		getErr := r.client.Get(ctx, client.ObjectKey{Namespace: alias.Namespace, Name: alias.Spec.Version}, v)
		switch {
		case getErr == nil:
			return v.Name, true, fv1.FunctionAliasReasonResolved, nil
		case apierrors.IsNotFound(getErr):
			return "", false, fv1.FunctionAliasReasonVersionNotFound, nil
		default:
			return "", false, "", fmt.Errorf("versioning: getting function version %s/%s for alias %s/%s: %w",
				alias.Namespace, alias.Spec.Version, alias.Namespace, alias.Name, getErr)
		}

	case alias.Spec.PackageDigest != "":
		versions := &fv1.FunctionVersionList{}
		if err := r.client.List(ctx, versions, client.InNamespace(alias.Namespace),
			client.MatchingLabels{fv1.VersionFunctionNameLabel: alias.Spec.FunctionName}); err != nil {
			return "", false, "", fmt.Errorf("versioning: listing function versions for function %s/%s: %w",
				alias.Namespace, alias.Spec.FunctionName, err)
		}
		var best *fv1.FunctionVersion
		for i := range versions.Items {
			v := &versions.Items[i]
			if v.Spec.FunctionName != alias.Spec.FunctionName || v.Spec.PackageDigest != alias.Spec.PackageDigest {
				continue
			}
			if best == nil || v.Spec.Sequence > best.Spec.Sequence {
				best = v
			}
		}
		if best == nil {
			return "", false, fv1.FunctionAliasReasonDigestUnmatched, nil
		}
		return best.Name, true, fv1.FunctionAliasReasonResolved, nil

	default:
		// The admission webhook's XOR rule (FunctionAliasSpec) makes this
		// unreachable for any object that ever passed validation; handled
		// defensively rather than panicking on a hand-crafted/legacy object.
		return "", false, fv1.FunctionAliasReasonVersionNotFound, nil
	}
}

// writeStatus persists the resolution outcome: on an effective-target CHANGE
// (ResolvedVersion transitioning from a non-empty X to Y != X) it appends an
// AliasTargetRecord for the OUTGOING target X before overwriting
// ResolvedVersion, bounded to aliasHistoryLimit (oldest dropped from the
// front). No history entry is appended on first resolution (no outgoing
// target yet) or when resolution is unmet (ResolvedVersion is left
// untouched entirely). The Resolved condition is always kept current. The
// write is skipped (nil returned, zero API calls) when nothing changed —
// the idempotence guarantee a duplicate/unrelated-event reconcile relies on.
//
// Uses a status Patch computed from a pre-mutation DeepCopy (client.MergeFrom)
// rather than Update, following the canaryconfigmgr reconciler pattern: the
// merge patch carries no ResourceVersion precondition, so this write never
// conflicts against the cache-read object.
func (r *AliasReconciler) writeStatus(ctx context.Context, alias *fv1.FunctionAlias, resolvedVersion string, resolvedOK bool, reason string) error {
	original := alias.DeepCopy()
	changed := false

	var message string
	switch {
	case resolvedOK:
		outgoing := alias.Status.ResolvedVersion
		if outgoing != "" && outgoing != resolvedVersion {
			rec := fv1.AliasTargetRecord{
				Version:       outgoing,
				PackageDigest: r.bestEffortDigest(ctx, alias.Namespace, outgoing),
				SwitchedAt:    metav1.Now(),
			}
			alias.Status.History = appendBoundedHistory(alias.Status.History, rec)
			changed = true
		}
		if outgoing != resolvedVersion {
			alias.Status.ResolvedVersion = resolvedVersion
			changed = true
		}
		message = fmt.Sprintf("resolved to FunctionVersion %q", resolvedVersion)
	case reason == fv1.FunctionAliasReasonDigestUnmatched:
		message = fmt.Sprintf("no FunctionVersion for function %q records packageDigest %q yet", alias.Spec.FunctionName, alias.Spec.PackageDigest)
	default:
		message = fmt.Sprintf("FunctionVersion %q not found", alias.Spec.Version)
	}

	condStatus := metav1.ConditionFalse
	if resolvedOK {
		condStatus = metav1.ConditionTrue
	}
	if conditions.Set(&alias.Status.Conditions, metav1.Condition{
		Type:               fv1.FunctionAliasConditionResolved,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: alias.Generation,
	}) {
		changed = true
	}

	if !changed {
		return nil
	}
	return r.client.Status().Patch(ctx, alias, client.MergeFrom(original))
}

// bestEffortDigest fetches name's FunctionVersion to record its digest on an
// outgoing AliasTargetRecord. It is best-effort by design (a Get, not a
// List, so cheap) — the version may have already been garbage collected by
// the time its target rolls out of ResolvedVersion, in which case an empty
// digest is recorded (AliasTargetRecord.PackageDigest is optional).
func (r *AliasReconciler) bestEffortDigest(ctx context.Context, namespace, name string) string {
	v := &fv1.FunctionVersion{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, v); err != nil {
		return ""
	}
	return v.Spec.PackageDigest
}

// appendBoundedHistory appends rec and, once len exceeds aliasHistoryLimit,
// drops from the front — keeping the newest aliasHistoryLimit entries with
// the most recent last, per the FunctionAliasStatus.History contract.
func appendBoundedHistory(hist []fv1.AliasTargetRecord, rec fv1.AliasTargetRecord) []fv1.AliasTargetRecord {
	hist = append(hist, rec)
	if len(hist) > aliasHistoryLimit {
		hist = hist[len(hist)-aliasHistoryLimit:]
	}
	return hist
}
