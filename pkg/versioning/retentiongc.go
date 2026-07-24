// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// DefaultRetain is the unaliased-version-history floor applied when a
// Function opts into RFC-0025 versioning (Spec.Versioning != nil) without an
// explicit Spec.Versioning.Retain — see VersioningConfig's doc comment.
const DefaultRetain = 10

// retentionGCRequeueInterval bounds how long the reconciler waits before
// retrying a sweep that skipped at least one delete on Forbidden. A webhook
// admission race (see SweepVersions's doc comment) is expected to clear on
// its own shortly; this is a cheap poll, not a blocking wait.
const retentionGCRequeueInterval = time.Minute

// SweepResult reports one SweepVersions call's outcome, split by why each
// candidate version did NOT end up deleted:
//
//   - Deleted: versions actually removed.
//   - Retained: versions never considered for deletion — inside the
//     newest max(retain,1), or alias-referenced at the initial scan.
//   - SkippedReferenced: versions that were candidates at scan time but, on
//     the delete-time recheck, turned out to be alias-referenced after all
//     (the aliasgc.tla GCAbandon transition — a concurrent alias create won
//     the race). Not an error; the version survives.
//   - SkippedForbidden: versions whose Delete call itself was denied with a
//     403 Forbidden. Never terminal — see the doc comment on SweepVersions
//     for why. The reconciler requeues when this is non-empty.
type SweepResult struct {
	Deleted           []string
	Retained          []string
	SkippedReferenced []string
	SkippedForbidden  []string
}

// SweepVersions garbage-collects old FunctionVersions of fnNS/fnName down to
// retain (floored at 1), never deleting a version referenced by any
// FunctionAlias — invariant V3: spec.Version ∪ spec.SecondaryVersion ∪
// status.ResolvedVersion, across every FunctionAlias in fnNS.
//
// Algorithm:
//  1. List versions for fnName (VersionFunctionNameLabel), sort by Sequence
//     ascending.
//  2. List aliases in fnNS once; collect every referenced version name from
//     all three ref fields.
//  3. retained = the newest max(retain,1) versions ∪ alias-referenced
//     versions. candidates = the rest, oldest first. The newest version (and
//     the only version, when just one exists) is always inside the
//     newest-max(retain,1) set, so it is never a candidate — even with
//     retain=1 and zero alias references.
//  4. For each candidate, oldest first: re-List aliases IMMEDIATELY BEFORE
//     the Delete call (THE RECHECK) and skip (SkippedReferenced) if the
//     candidate has since become referenced. Only then Delete.
//
// Model correspondence (docs/rfc/specs/aliasgc.tla): this per-candidate
// re-List-then-Delete approximates the model's atomic GCCommit recheck
// (RecheckGuard=TRUE) — TLC's counterexample under RecheckGuard=FALSE is
// exactly the un-rechecked delete this function never performs. A residual
// window remains: two admissions can still race between this function's
// recheck List and its Delete call landing on the apiserver (this recheck is
// not itself atomic with the delete). That residual window is ACCEPTED here;
// the nets closing it are (1) this recheck (catches the overwhelming
// majority of the race), (2) the FunctionAlias admission webhook (rejects an
// alias create/update naming a version that is already gone), and (3) the
// alias resolver's Resolved=False/VersionNotFound detection (self-heals a
// name-pinned alias whose target was deleted out from under it — see
// AliasReconciler.mapVersionToAliases's DELETE-event clause). No single net
// is load-bearing alone; together they make a stranded alias practically
// unreachable without ever claiming atomicity this list-then-delete
// controller does not have.
//
// Denial handling: a Delete call denied with 403 Forbidden (indistinguishable
// at the error-type level between an RBAC denial and the FunctionAlias
// admission webhook's delete-time guard, should one ever be added — never
// substring-matched) is recorded as SkippedForbidden and swept over, NEVER
// treated as terminal: buildermgr's own RBAC grant includes delete on
// functionversions (see charts/fission-all/templates/_fission-component-
// roles.tpl), so a persistent Forbidden here means a webhook denial won a
// race and the next sweep should simply try again. Any other error aborts
// the sweep and is returned to the caller.
func SweepVersions(ctx context.Context, cl versioned.Interface, fnNS, fnName string, retain int) (SweepResult, error) {
	var result SweepResult

	versions, err := listVersionsForName(ctx, cl, fnNS, fnName)
	if err != nil {
		return result, err
	}
	if len(versions) == 0 {
		return result, nil
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Spec.Sequence < versions[j].Spec.Sequence })

	keep := retain
	if keep < 1 {
		keep = 1
	}
	n := len(versions)
	if keep > n {
		keep = n
	}
	keepStart := n - keep

	initialRefs, err := aliasReferencedVersions(ctx, cl, fnNS)
	if err != nil {
		return result, err
	}

	var candidates []fv1.FunctionVersion
	for i := 0; i < keepStart; i++ {
		v := versions[i]
		if initialRefs[v.Name] {
			result.Retained = append(result.Retained, v.Name)
			continue
		}
		candidates = append(candidates, v)
	}
	for i := keepStart; i < n; i++ {
		result.Retained = append(result.Retained, versions[i].Name)
	}

	for _, v := range candidates {
		// THE RECHECK: re-List aliases immediately before this candidate's
		// Delete, not once up front — see the doc comment above for why.
		refs, err := aliasReferencedVersions(ctx, cl, fnNS)
		if err != nil {
			return result, err
		}
		if refs[v.Name] {
			result.SkippedReferenced = append(result.SkippedReferenced, v.Name)
			continue
		}

		delErr := cl.CoreV1().FunctionVersions(fnNS).Delete(ctx, v.Name, metav1.DeleteOptions{})
		switch {
		case delErr == nil:
			result.Deleted = append(result.Deleted, v.Name)
		case apierrors.IsForbidden(delErr):
			result.SkippedForbidden = append(result.SkippedForbidden, v.Name)
		case apierrors.IsNotFound(delErr):
			// Already gone (a concurrent sweep or manual delete won the
			// race) -- not a failure, nothing left to report either way.
		default:
			return result, fmt.Errorf("versioning: deleting function version %s/%s: %w", fnNS, v.Name, delErr)
		}
	}

	return result, nil
}

// listVersionsForName lists the FunctionVersions in ns labeled for fnName,
// unsorted.
func listVersionsForName(ctx context.Context, cl versioned.Interface, ns, fnName string) ([]fv1.FunctionVersion, error) {
	selector := labels.SelectorFromSet(labels.Set{fv1.VersionFunctionNameLabel: fnName}).String()
	list, err := cl.CoreV1().FunctionVersions(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("versioning: listing function versions for %s/%s: %w", ns, fnName, err)
	}
	return list.Items, nil
}

// aliasReferencedVersions lists every FunctionAlias in ns and returns the set
// of FunctionVersion names referenced by any of the three ref fields
// (invariant V3's union): Spec.Version, Spec.SecondaryVersion,
// Status.ResolvedVersion. Not scoped to a single function: a version name
// squatting on another function's alias reference is a pre-existing naming
// collision this function does not need to reason about, and scoping would
// only ever widen (never narrow) what a caller with a bogus FunctionName
// mismatch protects.
func aliasReferencedVersions(ctx context.Context, cl versioned.Interface, ns string) (map[string]bool, error) {
	aliases, err := cl.CoreV1().FunctionAliases(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("versioning: listing function aliases in %s for retention GC: %w", ns, err)
	}

	refs := make(map[string]bool, len(aliases.Items))
	for i := range aliases.Items {
		a := &aliases.Items[i]
		if a.Spec.Version != "" {
			refs[a.Spec.Version] = true
		}
		if a.Spec.SecondaryVersion != "" {
			refs[a.Spec.SecondaryVersion] = true
		}
		if a.Status.ResolvedVersion != "" {
			refs[a.Status.ResolvedVersion] = true
		}
	}
	return refs, nil
}

// RetentionGCReconciler sweeps old FunctionVersions for every Function that
// has opted into RFC-0025 versioning, via the pure SweepVersions engine
// above. Like AliasReconciler and AutoPublishReconciler, it watches more than
// one type and so is registered directly via builder.ControllerManagedBy
// rather than controller.RegisterTenantScoped.
type RetentionGCReconciler struct {
	logger logr.Logger
	// client is the manager's cached controller-runtime client: used for the
	// primary Function fetch that drives every Reconcile call.
	client client.Client
	// clientset is the generated Fission clientset: SweepVersions is built
	// against versioned.Interface (not a controller-runtime client) so the
	// CLI's `fission fn gc-versions` can share the exact same sweep core.
	clientset versioned.Interface
}

// RegisterRetentionGCReconciler wires the RFC-0025 phase-4 retention GC
// controller onto mgr. Under dynamic/cluster-wide tenancy
// (utils.CrdWatchClusterWide) all three watches are additionally scoped to
// live tenant namespaces via controller.MembershipPredicate, and a
// FissionTenant watch re-converges a namespace's functions on onboarding --
// mirroring RegisterAliasReconciler/RegisterAutoPublishReconciler's
// composition for the types they cannot register through
// controller.RegisterTenantScoped.
func RegisterRetentionGCReconciler(mgr ctrl.Manager, logger logr.Logger, clientset versioned.Interface) error {
	r := &RetentionGCReconciler{
		logger:    logger.WithName("retentiongc_reconciler"),
		client:    mgr.GetClient(),
		clientset: clientset,
	}

	// GenerationChangedPredicate: an ordinary spec edit re-triggers a sweep,
	// which is what catches a Retain LOWERING on an otherwise-unchanged
	// function (nothing else about the sweep depends on what changed).
	fnPredicates := []predicate.Predicate{predicate.GenerationChangedPredicate{}}
	// CREATE-only: a version's own existence (not its later, nonexistent,
	// updates -- FunctionVersion is immutable in practice) is what can make a
	// sweep's retain-count math change (one more version now above the
	// floor).
	versionPredicates := []predicate.Predicate{versionCreatePredicate()}
	// Permissive (no predicate): every alias event -- including DELETE --
	// must re-trigger a sweep of its function, because an alias delete (or a
	// repoint off a version) is exactly what RELEASES a version for GC that
	// was previously held retained by invariant V3.
	var aliasPredicates []predicate.Predicate

	if utils.CrdWatchClusterWide() {
		mp := controller.MembershipPredicate(utils.DefaultNSResolver())
		fnPredicates = append(fnPredicates, mp)
		versionPredicates = append(versionPredicates, mp)
		aliasPredicates = append(aliasPredicates, mp)
	}

	b := builder.ControllerManagedBy(mgr).
		Named("versioning-retentiongc").
		For(&fv1.Function{}, builder.WithPredicates(fnPredicates...)).
		Watches(&fv1.FunctionVersion{}, handler.EnqueueRequestsFromMapFunc(r.mapVersionToFunction),
			builder.WithPredicates(versionPredicates...)).
		Watches(&fv1.FunctionAlias{}, handler.EnqueueRequestsFromMapFunc(r.mapAliasToFunction),
			builder.WithPredicates(aliasPredicates...))

	if utils.CrdWatchClusterWide() {
		b = b.Watches(&fv1.FissionTenant{},
			controller.TenantReenqueueHandler(mgr.GetAPIReader(), mgr.GetScheme(), &fv1.Function{}),
			builder.WithPredicates(controller.TenantOnboardPredicate()))
	}

	return b.Complete(r)
}

// versionCreatePredicate admits only FunctionVersion Create events -- see
// RegisterRetentionGCReconciler's comment on versionPredicates for why
// Update/Delete/Generic are dropped.
func versionCreatePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// mapVersionToFunction enqueues the Function a FunctionVersion Create event
// belongs to, read off VersionFunctionNameLabel -- no List needed, unlike the
// alias-side map functions elsewhere in this package.
func (r *RetentionGCReconciler) mapVersionToFunction(_ context.Context, obj client.Object) []reconcile.Request {
	v, ok := obj.(*fv1.FunctionVersion)
	if !ok {
		return nil
	}
	fnName := v.Labels[fv1.VersionFunctionNameLabel]
	if fnName == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: v.Namespace, Name: fnName}}}
}

// mapAliasToFunction enqueues the Function a FunctionAlias event names via
// Spec.FunctionName.
func (r *RetentionGCReconciler) mapAliasToFunction(_ context.Context, obj client.Object) []reconcile.Request {
	a, ok := obj.(*fv1.FunctionAlias)
	if !ok {
		return nil
	}
	if a.Spec.FunctionName == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: a.Namespace, Name: a.Spec.FunctionName}}}
}

// Reconcile sweeps req's Function's FunctionVersions down to its configured
// (or default) retain floor.
//
//  1. Function gone (NotFound): no-op -- the CRD ownerRef cascade deletes its
//     FunctionVersions, retention GC has nothing left to do.
//  2. Spec.Versioning == nil: no-op, deliberately NOT defaulted to
//     DefaultRetain. A function that never opted into RFC-0025 versioning
//     only ever accumulates FunctionVersions via an explicit `fission fn
//     publish` -- sweeping it without opt-in could delete history the user
//     is curating by hand.
//  3. retain = *Spec.Versioning.Retain, or DefaultRetain when nil.
//  4. SweepVersions. Any SkippedForbidden requeues after
//     retentionGCRequeueInterval (never returned as a reconcile error -- see
//     SweepVersions's doc comment on why Forbidden is never terminal).
func (r *RetentionGCReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if fn.Spec.Versioning == nil {
		return reconcile.Result{}, nil
	}

	retain := DefaultRetain
	if fn.Spec.Versioning.Retain != nil {
		retain = *fn.Spec.Versioning.Retain
	}

	result, err := SweepVersions(ctx, r.clientset, fn.Namespace, fn.Name, retain)
	if err != nil {
		return reconcile.Result{}, err
	}

	recordVersionGCDeleted(ctx, len(result.Deleted))
	recordVersionGCSkipped(ctx, versionGCSkipReasonReferenced, len(result.SkippedReferenced))
	recordVersionGCSkipped(ctx, versionGCSkipReasonForbidden, len(result.SkippedForbidden))

	if len(result.SkippedForbidden) > 0 {
		return reconcile.Result{RequeueAfter: retentionGCRequeueInterval}, nil
	}
	return reconcile.Result{}, nil
}
