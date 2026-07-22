// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package versioning implements the RFC-0025 publish engine: turning a
// Function's live spec into an immutable FunctionVersion snapshot. It is a
// pure library — no controller, no CLI wiring — consumed by `fission fn
// publish` (Task 5) today and by the phase-4 auto-publish controller later,
// so both callers mint versions through exactly one algorithm.
package versioning

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

const (
	// DescriptionAnnotation records the caller-supplied publish description
	// (CLI --description flag today; an auto-publish commit-style message in
	// phase 4) on the minted FunctionVersion.
	DescriptionAnnotation = "fission.io/description"

	// SourcePackageAnnotation records the name of the live Package a
	// version's snapshot referenced at publish time, before the legacy
	// (non-OCI-digest) path repointed Snapshot.Package.PackageRef at the
	// version-owned copy (`<fn>-v<seq>-pkg`). Idempotence comparisons
	// restore this name onto the newest version's snapshot before comparing
	// it against the live spec, so the repoint alone never defeats
	// idempotence. Absent on OCI-digest-backed versions, which never repoint.
	SourcePackageAnnotation = "fission.io/source-package"

	// snapshotPackageSuffix names the version-owned copy of a legacy
	// (non-OCI) package: "<fn>-v<seq>" + this suffix.
	snapshotPackageSuffix = "-pkg"
)

// ErrPackageNotReady is returned by Publish when fn's referenced Package has
// not reached a build-ready terminal state (BuildStatus not in {succeeded,
// none}). Wrapped with package identity context; use errors.Is to detect it.
var ErrPackageNotReady = errors.New("versioning: package not ready")

// PublishResult reports the created-or-existing version.
type PublishResult struct {
	Version *fv1.FunctionVersion
	Created bool // false = idempotent no-op (spec unchanged vs newest)
}

// Publish snapshots fn's current spec as the next FunctionVersion. It is
// idempotent: called again with an unchanged spec and unchanged package
// digest, it returns the newest existing version rather than minting a
// duplicate.
//
// Algorithm (see docs/rfc/0025 and the task-4 brief for the design record):
//  1. Get fn's referenced Package; require a build-ready status
//     (BuildStatusSucceeded or BuildStatusNone — the same readiness
//     predicate spec apply itself uses). Otherwise ErrPackageNotReady.
//  2. List FunctionVersions owned by fn (function-name + function-uid
//     labels) in fn.Namespace; compute the max Sequence and its version.
//  3. Zero the live spec's Versioning field — a version is a
//     versioning-config-free leaf snapshot.
//  4. Compute the package digest. If a newest version exists, its snapshot
//     (with any legacy PackageRef repoint normalized back to the original
//     package name) semantically equals the zeroed live spec, AND its
//     recorded digest matches: return {newest, false}. Self-heals a missing
//     legacy snapshot Package left behind by a crash between steps 6a/6b.
//  5. Legacy path (no OCI digest): repoint the snapshot's PackageRef at the
//     not-yet-created snapshot package name and record the original name in
//     an annotation.
//  6. Create the FunctionVersion first; on AlreadyExists, re-list and retry
//     once (a concurrent publisher may have won the race). Only then, for
//     the legacy path, create the snapshot Package, owned by the version
//     from creation (no post-hoc patch).
//  7. Return {version, true}.
func Publish(ctx context.Context, cl versioned.Interface, fn *fv1.Function, description string) (*PublishResult, error) {
	return publish(ctx, cl, fn, description, true)
}

func publish(ctx context.Context, cl versioned.Interface, fn *fv1.Function, description string, allowRetry bool) (*PublishResult, error) {
	pkg, err := cl.CoreV1().Packages(fn.Spec.Package.PackageRef.Namespace).Get(ctx, fn.Spec.Package.PackageRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("versioning: getting package %s/%s referenced by function %s/%s: %w",
			fn.Spec.Package.PackageRef.Namespace, fn.Spec.Package.PackageRef.Name, fn.Namespace, fn.Name, err)
	}

	if pkg.Status.BuildStatus != fv1.BuildStatusSucceeded && pkg.Status.BuildStatus != fv1.BuildStatusNone {
		return nil, fmt.Errorf("%w: package %s/%s build status is %q", ErrPackageNotReady, pkg.Namespace, pkg.Name, pkg.Status.BuildStatus)
	}

	newest, maxSeq, err := newestVersion(ctx, cl, fn)
	if err != nil {
		return nil, err
	}

	snap := fn.Spec.DeepCopy()
	snap.Versioning = nil

	digest, err := PackageDigest(pkg)
	if err != nil {
		return nil, err
	}

	if newest != nil && equality.Semantic.DeepEqual(*snap, normalizedSnapshot(newest)) && digest == newest.Spec.PackageDigest {
		if err := selfHealSnapshotPackage(ctx, cl, fn, newest, pkg); err != nil {
			return nil, err
		}
		return &PublishResult{Version: newest, Created: false}, nil
	}

	envNS := fn.Spec.Environment.Namespace
	if envNS == "" {
		envNS = fn.Namespace
	}
	env, err := cl.CoreV1().Environments(envNS).Get(ctx, fn.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("versioning: getting environment %s/%s referenced by function %s/%s: %w",
			envNS, fn.Spec.Environment.Name, fn.Namespace, fn.Name, err)
	}

	seq := maxSeq + 1
	name := fmt.Sprintf("%s-v%d", fn.Name, seq)

	legacy := !isOCIDigestBacked(pkg)
	origPkgName := snap.Package.PackageRef.Name
	if legacy {
		snap.Package.PackageRef.Name = name + snapshotPackageSuffix
	}

	version := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fn.Namespace,
			Labels: map[string]string{
				fv1.VersionFunctionNameLabel: fn.Name,
				fv1.VersionFunctionUIDLabel:  string(fn.UID),
			},
			Annotations:     versionAnnotations(description, legacy, origPkgName),
			OwnerReferences: []metav1.OwnerReference{functionOwnerRef(fn)},
		},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:          fn.Name,
			FunctionUID:           fn.UID,
			FunctionGeneration:    fn.Generation,
			Sequence:              seq,
			Snapshot:              *snap,
			PackageDigest:         digest,
			EnvObservedGeneration: env.Generation,
			EnvRuntimeImage:       env.Spec.Runtime.Image,
			PublishedAt:           metav1.Now(),
		},
	}

	created, err := cl.CoreV1().FunctionVersions(fn.Namespace).Create(ctx, version, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) && allowRetry {
			// A concurrent publisher won the race for this sequence number;
			// re-list and recompute from scratch (sequence, idempotence,
			// digest) exactly once rather than blindly retrying this name.
			return publish(ctx, cl, fn, description, false)
		}
		return nil, fmt.Errorf("versioning: creating FunctionVersion %s/%s: %w", fn.Namespace, name, err)
	}

	if legacy {
		if _, err := ensureSnapshotPackage(ctx, cl, fn, created, pkg); err != nil {
			return nil, err
		}
	}

	return &PublishResult{Version: created, Created: true}, nil
}

// newestVersion lists the FunctionVersions owned by fn and returns the one
// with the highest Sequence (nil if none exist yet) along with that max
// sequence number (0 if none exist).
func newestVersion(ctx context.Context, cl versioned.Interface, fn *fv1.Function) (*fv1.FunctionVersion, int64, error) {
	selector := labels.SelectorFromSet(labels.Set{
		fv1.VersionFunctionNameLabel: fn.Name,
		fv1.VersionFunctionUIDLabel:  string(fn.UID),
	}).String()

	versions, err := cl.CoreV1().FunctionVersions(fn.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, 0, fmt.Errorf("versioning: listing FunctionVersions for function %s/%s: %w", fn.Namespace, fn.Name, err)
	}

	var maxSeq int64
	var newest *fv1.FunctionVersion
	for i := range versions.Items {
		v := &versions.Items[i]
		if newest == nil || v.Spec.Sequence > maxSeq {
			maxSeq = v.Spec.Sequence
			newest = v
		}
	}
	return newest, maxSeq, nil
}

// normalizedSnapshot returns v's Snapshot with any legacy PackageRef repoint
// undone: the PackageRef.Name is restored to the value recorded in
// SourcePackageAnnotation at creation, if present. Without this, a legacy
// version's snapshot would never DeepEqual the live spec it was published
// from — its PackageRef always points at the version-owned copy, never the
// original package — and idempotence would never fire for legacy functions.
func normalizedSnapshot(v *fv1.FunctionVersion) fv1.FunctionSpec {
	snap := *v.Spec.Snapshot.DeepCopy()
	if orig, ok := v.Annotations[SourcePackageAnnotation]; ok && orig != "" {
		snap.Package.PackageRef.Name = orig
	}
	return snap
}

// versionAnnotations builds the annotation set for a newly minted
// FunctionVersion: an optional human description plus, on the legacy path,
// the original package name idempotence normalization needs. Returns nil
// (never an empty non-nil map) when there is nothing to record.
func versionAnnotations(description string, legacy bool, origPkgName string) map[string]string {
	ann := map[string]string{}
	if description != "" {
		ann[DescriptionAnnotation] = description
	}
	if legacy {
		ann[SourcePackageAnnotation] = origPkgName
	}
	if len(ann) == 0 {
		return nil
	}
	return ann
}

// functionOwnerRef is the ownerRef a FunctionVersion carries back to its
// owning Function (mirrors the CR-owns-CR pattern in pkg/tenant/reconciler.go
// and pkg/webhook/functionversion.go's ownerFunctionRef, which reads Kind
// "Function" back off this reference).
func functionOwnerRef(fn *fv1.Function) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: fv1.SchemeGroupVersion.String(),
		Kind:       "Function",
		Name:       fn.Name,
		UID:        fn.UID,
	}
}

// versionOwnerRef is the ownerRef a legacy snapshot Package carries back to
// the FunctionVersion that owns it, set at creation (step 6): no unowned
// window, no post-hoc patch.
func versionOwnerRef(v *fv1.FunctionVersion) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: fv1.SchemeGroupVersion.String(),
		Kind:       "FunctionVersion",
		Name:       v.Name,
		UID:        v.UID,
	}
}

// selfHealSnapshotPackage re-creates a legacy version's snapshot Package
// when it is missing — the trace left by a crash between the FunctionVersion
// create and the snapshot Package create (step 6), or an out-of-band
// deletion. version's own annotations, not pkg's current OCI-backedness,
// decide whether it is a legacy version: SourcePackageAnnotation is only
// ever set on the legacy path, so its presence is definitive regardless of
// whether the live package has since gained an OCI digest.
func selfHealSnapshotPackage(ctx context.Context, cl versioned.Interface, fn *fv1.Function, version *fv1.FunctionVersion, pkg *fv1.Package) error {
	if _, ok := version.Annotations[SourcePackageAnnotation]; !ok {
		return nil
	}

	snapPkgName := version.Spec.Snapshot.Package.PackageRef.Name
	_, err := cl.CoreV1().Packages(fn.Namespace).Get(ctx, snapPkgName, metav1.GetOptions{})
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		_, err := ensureSnapshotPackage(ctx, cl, fn, version, pkg)
		return err
	default:
		return fmt.Errorf("versioning: checking snapshot package %s/%s for version %s: %w", fn.Namespace, snapPkgName, version.Name, err)
	}
}

// ensureSnapshotPackage creates the version-owned copy of pkg's spec, named
// by version's (already repointed) Snapshot.Package.PackageRef, owned by
// version from the moment it exists. Tolerates AlreadyExists (the create
// path and the self-heal path can both reach here for the same object under
// concurrent publishers) by fetching and returning the existing object.
func ensureSnapshotPackage(ctx context.Context, cl versioned.Interface, fn *fv1.Function, version *fv1.FunctionVersion, pkg *fv1.Package) (*fv1.Package, error) {
	snapPkgName := version.Spec.Snapshot.Package.PackageRef.Name

	snapPkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:            snapPkgName,
			Namespace:       fn.Namespace,
			OwnerReferences: []metav1.OwnerReference{versionOwnerRef(version)},
		},
		Spec: *pkg.Spec.DeepCopy(),
		Status: fv1.PackageStatus{
			BuildStatus:         pkg.Status.BuildStatus,
			LastUpdateTimestamp: metav1.Now(),
		},
	}

	created, err := cl.CoreV1().Packages(fn.Namespace).Create(ctx, snapPkg, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing, gerr := cl.CoreV1().Packages(fn.Namespace).Get(ctx, snapPkgName, metav1.GetOptions{})
			if gerr != nil {
				return nil, fmt.Errorf("versioning: snapshot package %s/%s create raced and the follow-up get also failed: %w", fn.Namespace, snapPkgName, gerr)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("versioning: creating snapshot package %s/%s for version %s: %w", fn.Namespace, snapPkgName, version.Name, err)
	}
	return created, nil
}
