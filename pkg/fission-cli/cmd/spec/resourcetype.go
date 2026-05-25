// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sCache "k8s.io/client-go/tools/cache"
)

// Object constrains the generic spec reconciler to a pointer to a Fission CRD
// value type (e.g. *fv1.Function). Such pointers satisfy metav1.Object (via the
// embedded ObjectMeta) and runtime.Object (via the generated DeepCopyObject),
// which is all the reconciler needs to read names, namespaces and annotations.
type Object[T any] interface {
	*T
	metav1.Object
	runtime.Object
}

// resourceOps describes how to reconcile one Fission resource kind during
// `spec apply`. applyResourceType owns the shared list -> diff ->
// create/update/delete skeleton; these closures supply only the per-kind typed
// client calls and equality, so each kind needs a few lines rather than its own
// copy of the whole loop. Kind-specific quirks live inside the closures: the
// Package update closure waits out an in-flight build, and the HTTPTrigger
// validate closure rejects duplicate routes.
type resourceOps[T any, PT Object[T]] struct {
	items func(fr *FissionResources) []T         // desired resources from the spec
	list  func(ctx context.Context) ([]T, error) // all such resources on the cluster
	meta  func(PT) *metav1.ObjectMeta            // the object's ObjectMeta
	equal func(existing, desired PT) bool        // true => apply is a no-op
	// validate, if set, runs read-only admission-style checks before a
	// create/update (e.g. HTTPTrigger duplicate-route detection). It runs in
	// dry-run too, so a preview surfaces errors a real apply would hit.
	validate func(ctx context.Context, desired PT) error
	create   func(ctx context.Context, desired PT) (*metav1.ObjectMeta, error)
	// update receives the existing object so it can carry the ResourceVersion
	// forward (and, for packages, wait out an in-flight build).
	update func(ctx context.Context, existing, desired PT) (*metav1.ObjectMeta, error)
	delete func(ctx context.Context, namespace, name string) error
}

// dryRunResourceVersion is the synthetic ResourceVersion recorded for a would-be
// update under --dry-run. A real update bumps the object's ResourceVersion, and a
// Function embeds the ResourceVersion of the Package it references; recording a
// sentinel here means such dependents detect and report the would-be change too,
// instead of appearing as no-ops because they still carry the old RV. It is never
// written to the cluster — dry-run performs no create/update/delete calls.
const dryRunResourceVersion = "dry-run-would-update"

// setDeploymentUID stamps the deployment name/UID annotations so future applies
// can recognise the objects this spec owns.
func setDeploymentUID(o metav1.Object, fr *FissionResources) {
	ann := o.GetAnnotations()
	if ann == nil {
		ann = make(map[string]string)
	}
	ann[FISSION_DEPLOYMENT_NAME_KEY] = fr.DeploymentConfig.Name
	ann[FISSION_DEPLOYMENT_UID_KEY] = fr.DeploymentConfig.UID
	o.SetAnnotations(ann)
}

// ownedByDeployment reports whether o was created by this spec deployment, i.e.
// it carries the deployment-UID annotation matching fr's. A spec with an empty
// UID owns nothing, and an object without the annotation is never owned — this
// guards `spec apply --delete` from ever touching unannotated cluster resources
// (matching the pre-generics hasDeploymentConfig semantics).
func ownedByDeployment(o metav1.Object, fr *FissionResources) bool {
	if fr.DeploymentConfig.UID == "" {
		return false
	}
	uid, ok := o.GetAnnotations()[FISSION_DEPLOYMENT_UID_KEY]
	return ok && uid == fr.DeploymentConfig.UID
}

// applyResourceType reconciles one resource kind: it lists the cluster objects
// owned by this deployment (or all of them when conflicts are allowed), then for
// each spec resource creates it, updates it when it differs, or leaves it
// untouched. When deleteStale is set, owned cluster objects absent from the spec
// are removed. It returns each desired object's metadata keyed by namespace/name
// so callers can wire up cross-references such as a function's package
// ResourceVersion.
//
// When dryRun is set, the same list/diff is performed read-only: the
// create/update/delete client calls (and the per-deletion log line) are skipped,
// but ras and the metadata map are still populated as a real run would report —
// the desired object's metadata for would-be-creates, and the existing object's
// metadata with a sentinel ResourceVersion (dryRunResourceVersion) for would-be-
// updates so dependents that embed it detect the change — so the caller's summary
// and cross-reference wiring stay correct without touching the cluster.
func applyResourceType[T any, PT Object[T]](
	ctx context.Context,
	fr *FissionResources,
	ops resourceOps[T, PT],
	deleteStale bool,
	allowConflicts bool,
	dryRun bool,
) (map[string]metav1.ObjectMeta, *ResourceApplyStatus, error) {

	clusterObjs, err := ops.list(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Index the cluster objects this deployment owns, by namespace/name. Keep an
	// ordered slice too so deletions print in a stable (list) order. allByName
	// holds every cluster object's name (owned or not) so dry-run can detect the
	// AlreadyExists conflict a real create would hit (see the create branch).
	ownedByName := make(map[string]PT)
	allByName := make(map[string]bool)
	var owned []PT
	for i := range clusterObjs {
		obj := PT(&clusterObjs[i])
		allByName[k8sCache.MetaObjectToName(obj).String()] = true
		if allowConflicts || ownedByDeployment(obj, fr) {
			ownedByName[k8sCache.MetaObjectToName(obj).String()] = obj
			owned = append(owned, obj)
		}
	}

	metadata := make(map[string]metav1.ObjectMeta)
	desired := make(map[string]bool)
	var ras ResourceApplyStatus

	items := ops.items(fr)
	for i := range items {
		obj := items[i] // operate on a copy, as the previous per-kind code did
		ptr := PT(&obj)
		setDeploymentUID(ptr, fr)

		name := k8sCache.MetaObjectToName(ptr).String()
		desired[name] = true

		switch existing, found := ownedByName[name]; {
		case found && ops.equal(existing, ptr):
			// Already up to date; record existing metadata for cross-refs.
			metadata[name] = *ops.meta(existing)
		case found:
			// would-be update: validate first (runs in dry-run too), then either
			// perform the update or, in dry-run, synthesize the metadata a real
			// update would return (existing meta with a bumped ResourceVersion) so
			// dependent resources still detect the would-be change.
			if ops.validate != nil {
				if err := ops.validate(ctx, ptr); err != nil {
					return nil, nil, err
				}
			}
			var newMeta *metav1.ObjectMeta
			if dryRun {
				m := *ops.meta(existing)
				m.ResourceVersion = dryRunResourceVersion
				newMeta = &m
			} else {
				newMeta, err = ops.update(ctx, existing, ptr)
				if err != nil {
					return nil, nil, err
				}
			}
			ras.Updated = append(ras.Updated, newMeta)
			metadata[name] = *newMeta
		default:
			// would-be create: validate first (runs in dry-run too), record the
			// desired metadata (RV empty), and only mutate when not a dry run.
			if ops.validate != nil {
				if err := ops.validate(ctx, ptr); err != nil {
					return nil, nil, err
				}
			}
			// A real apply calls Create here; if an object with this name already
			// exists but isn't owned by this spec (so it wasn't a `found` update)
			// and we're not adopting conflicts, the server returns AlreadyExists
			// and the apply fails. Dry-run skips the call, so surface that conflict
			// explicitly instead of reporting a would-create that can't happen.
			if dryRun && !allowConflicts && allByName[name] {
				return nil, nil, fmt.Errorf("%s already exists on the cluster and is not managed by this spec deployment; a real apply would fail with AlreadyExists (use --allowconflicts to adopt it)", name)
			}
			newMeta := ops.meta(ptr)
			if !dryRun {
				newMeta, err = ops.create(ctx, ptr)
				if err != nil {
					return nil, nil, err
				}
			}
			ras.Created = append(ras.Created, newMeta)
			metadata[name] = *newMeta
		}
	}

	if deleteStale {
		for _, obj := range owned {
			name := k8sCache.MetaObjectToName(obj).String()
			if desired[name] {
				continue
			}
			if !dryRun {
				if err := ops.delete(ctx, obj.GetNamespace(), obj.GetName()); err != nil {
					return nil, nil, err
				}
				fmt.Printf("Deleted %v %v\n", obj.GetObjectKind().GroupVersionKind().Kind, name)
			}
			ras.Deleted = append(ras.Deleted, ops.meta(obj))
		}
	}

	return metadata, &ras, nil
}
