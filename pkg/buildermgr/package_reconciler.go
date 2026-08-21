// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// builderPodPollInterval is how long the package reconciler waits before
// re-checking whether the environment's builder pod has become ready. The wait
// is requeue-based (not a blocking sleep), so it never holds a reconcile worker.
const builderPodPollInterval = 5 * time.Second

// convergeRequeueInterval paces the self-requeue taken when a package's content
// changed while its build was in flight. It replaces a `Requeue: true`
// (deprecated in controller-runtime v0.24) that rode the workqueue's
// exponential rate limiter, so unlike that one it does NOT back off: each pass
// that still sees a hash mismatch starts another build. 5s rather than a
// sub-second value because every iteration can spawn a builder pod, and a spec
// being rewritten continuously by an external GitOps controller would otherwise
// turn this into a per-second rebuild loop.
const convergeRequeueInterval = 5 * time.Second

// PackageReconciler builds source-archive packages into deployment archives. It
// replaces the informer-driven packageWatcher: controller-runtime's workqueue
// owns scheduling and serializes reconciles per Package key, which replaces the
// old buildCache (the dedupe of concurrent build goroutines). Builds run
// synchronously inside Reconcile, bounded by the controller's
// MaxConcurrentReconciles.
//
// The rebuild trigger is a status write, not a spec change: callers set
// Status.BuildStatus = pending through the /status subresource. The controller
// is therefore registered with buildTriggerPredicate (NOT
// GenerationChangedPredicate, which would drop that trigger).
type PackageReconciler struct {
	logger           logr.Logger
	client           client.Client
	fissionClient    versioned.Interface
	kubernetesClient kubernetes.Interface
	nsResolver       *utils.NamespaceResolver
	storageSvcUrl    string
	registryCfg      *packageRegistryConfig
	podPollInterval  time.Duration
}

func makePackageReconciler(logger logr.Logger, client client.Client, fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface, storageSvcUrl string, registryCfg *packageRegistryConfig) *PackageReconciler {
	return &PackageReconciler{
		logger:           logger.WithName("package_reconciler"),
		client:           client,
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		nsResolver:       utils.DefaultNSResolver(),
		storageSvcUrl:    storageSvcUrl,
		registryCfg:      registryCfg,
		podPollInterval:  builderPodPollInterval,
	}
}

func (r *PackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pkg := &fv1.Package{}
	if err := r.client.Get(ctx, req.NamespacedName, pkg); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: nothing to tear down (the deployment archive lives in
			// storagesvc and is pruned independently).
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	switch pkg.Status.BuildStatus {
	case "":
		// Freshly applied, or status stripped by the /status subresource on
		// create: derive and write the initial build status. The resulting
		// BuildStatus -> pending transition re-triggers us (via
		// buildTriggerPredicate) when a build is actually needed; deploy-only
		// packages settle on "none" and are not built.
		if _, err := setInitialBuildStatus(ctx, r.fissionClient, pkg); err != nil {
			if apierrors.IsNotFound(err) {
				// Package deleted between our Get and the status write —
				// nothing to initialize.
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("error setting initial package build status: %w", err)
		}
		return ctrl.Result{}, nil
	case fv1.BuildStatusPending, fv1.BuildStatusRunning:
		// pending: a build is requested. running: a previous build was
		// interrupted (e.g. a buildermgr restart re-lists this Package as a
		// Create) — re-drive it, the build is idempotent.
		return r.build(ctx, pkg)
	default:
		// none / succeeded / failed are terminal for the BUILD, but not for
		// content: a Git-applied Package whose archive or OCI digest changed
		// must converge without the CLI's status->pending poke (RFC-0029 §3).
		return r.reconcileContentChange(ctx, pkg)
	}
}

// reconcileContentChange converges a package whose build status is terminal but
// whose content may have moved underneath it — the GitOps case, where nothing
// pokes the status.
//
// Two shapes, one hash:
//
//   - a SOURCE package re-enters the build queue (status -> pending), which is
//     what the CLI's poke used to do by hand;
//   - a DEPLOY-ONLY or OCI package never builds at all, so there is nothing to
//     re-enter; its referencing Functions are re-stamped directly. That is the
//     digest-pinned-OCI-in-Git path this RFC blesses, and before this it had no
//     propagation whatsoever.
//
// The seeding rule is what keeps an upgrade safe: a package with no recorded
// hash — which is every package on the first reconcile after this ships — has
// its hash written WITHOUT rebuilding or re-stamping. An absent hash must never
// read as "changed", or adopting this feature would rebuild the entire cluster
// at once.
func (r *PackageReconciler) reconcileContentChange(ctx context.Context, pkg *fv1.Package) (ctrl.Result, error) {
	current := PackageContentHash(pkg.Spec)
	if pkg.Status.ContentHash == current {
		return ctrl.Result{}, nil
	}

	logger := r.logger.WithValues("package", pkg.Name, "namespace", pkg.Namespace)
	seeding := pkg.Status.ContentHash == ""
	if seeding {
		// Recorded without propagating. Logged because it is the one path that
		// deliberately swallows a content change, and status is user-writable —
		// an empty ContentHash written by hand suppresses exactly one
		// propagation, and without this line it would do so silently.
		logger.Info("seeding package content hash without propagating (first observation of this package)")
	}

	if !seeding && !pkg.Spec.Source.IsEmpty() {
		// Source changed: re-enter the build queue. updatePackage records the
		// hash on every status write it makes — including this pending one —
		// so a build that then fails is NOT left looking unconverged and
		// re-queued in a loop; the user's next source edit is what re-triggers
		// it.
		logger.Info("package source content changed, re-queuing build")
		if _, err := updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusPending, "", nil); err != nil {
			return ctrl.Result{}, fmt.Errorf("error re-queuing build after content change: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if !seeding {
		// Deploy-only / OCI: nothing to build, so propagate directly.
		fnList, err := r.fissionClient.CoreV1().Functions(pkg.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error listing functions after content change: %w", err)
		}
		stamped, err := r.restampReferencingFunctions(ctx, pkg, fnList.Items, true)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error re-stamping functions after content change: %w", err)
		}
		// stamped == 0 means every referencing Function already points at this
		// package — the CLI stamped them itself. That is the CONVERGED state,
		// so the hash is still recorded below.
		//
		// The hash must be recorded on EVERY reconcile, converged or not: a
		// hash that lags a revision behind the spec makes a revert to the
		// previously-recorded content compare equal, silently swallowing the
		// incident-response rollback RFC-0029 G4 exists for.
		logger.Info("package content change reconciled", "functions_restamped", stamped)
	}

	if err := r.recordContentHash(ctx, pkg, current); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// recordContentHash writes the observed hash to status, re-reading the package
// first so it never clobbers a concurrent status write (the build path writes
// the same subresource).
func (r *PackageReconciler) recordContentHash(ctx context.Context, pkg *fv1.Package, hash string) error {
	packages := r.fissionClient.CoreV1().Packages(pkg.Namespace)
	// Retry rather than drop on conflict: the competing writers here are
	// status-only (condition writes, the CLI's build-status reset), and those
	// change neither Generation nor BuildStatus-into-pending, so the trigger
	// predicate drops their events. Nothing would re-reconcile us, and the
	// hash would stay empty — which makes the NEXT content change read as a
	// seed and be swallowed.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, gerr := packages.Get(ctx, pkg.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		if latest.Status.ContentHash == hash {
			return nil
		}
		latest.Status.ContentHash = hash
		_, uerr := packages.UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return uerr
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error recording package content hash: %w", err)
	}
	return nil
}

// build drives a source package through the environment's builder into a
// deployment archive, updating Package status and dependent Function conditions
// along the way. It returns a RequeueAfter result while the builder pod is not
// yet ready; a build failure is terminal (recorded as BuildStatusFailed, not
// requeued).
func (r *PackageReconciler) build(ctx context.Context, pkg *fv1.Package) (ctrl.Result, error) {
	logger := r.logger.WithValues("package", pkg.Name, "namespace", pkg.Namespace, "resource_version", pkg.ResourceVersion)

	// Defence in depth — the admission webhook should already have rejected a
	// cross-namespace environment reference at submit time, but reconcile loops
	// can still see stale objects on upgraded clusters or on clusters running
	// the webhook with failurePolicy=Ignore (GHSA-vjhc-cf4p-72q4).
	if pkg.Spec.Environment.Namespace != "" && pkg.Spec.Environment.Namespace != pkg.Namespace {
		msg := fmt.Sprintf("cross-namespace environment reference is not allowed: pkg.namespace=%s env.namespace=%s",
			pkg.Namespace, pkg.Spec.Environment.Namespace)
		logger.Info("rejecting cross-namespace environment reference", "env_namespace", pkg.Spec.Environment.Namespace)
		return r.failBuild(ctx, logger, pkg, msg)
	}

	env, err := r.fissionClient.CoreV1().Environments(pkg.Spec.Environment.Namespace).Get(ctx, pkg.Spec.Environment.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logger.Info("environment does not exist", "environment", pkg.Spec.Environment.Name)
		return r.failBuild(ctx, logger, pkg, fmt.Sprintf("environment does not exist: %q", pkg.Spec.Environment.Name))
	}
	if err != nil {
		// Transient API error — requeue with the controller's backoff.
		return ctrl.Result{}, fmt.Errorf("error getting environment %q: %w", pkg.Spec.Environment.Name, err)
	}

	builderNs := r.nsResolver.GetBuilderNS(env.Namespace)
	logger = logger.WithValues("environment", env.Name, "builder_namespace", builderNs, "environment_namespace", env.Namespace)

	builderPod, err := r.readyBuilderPod(ctx, env, builderNs)
	if err != nil {
		return ctrl.Result{}, err
	}
	if builderPod == nil {
		// The EnvironmentReconciler owns creating the builder Deployment; here
		// we just wait for its pod to report ready. Requeue rather than block a
		// worker — the Package stays "pending" and is visibly waiting.
		logger.Info("environment builder pod not ready, will retry")
		return ctrl.Result{RequeueAfter: r.podPollInterval}, nil
	}

	logger.Info("starting build for package")
	pkg, err = updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusRunning, "", nil)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error setting package running state: %w", err)
	}

	// Version-aware signing: sign this builder pod's sidecar calls with the key
	// it actually verifies with (its per-namespace key if it was stamped under
	// dynamic tenancy, else master-derived).
	signNamespace, _ := builderSigningNamespace(builderPod, builderNs)
	uploadResp, buildLogs, err := buildPackage(ctx, logger, r.fissionClient, builderNs, signNamespace, r.storageSvcUrl, r.registryCfg, pkg)
	if err != nil {
		logger.Error(err, "error building package")
		r.markBuildFailed(ctx, logger, pkg, buildLogs)
		r.propagateFunctionFailure(ctx, logger, pkg)
		return ctrl.Result{}, nil
	}

	logger.Info("starting package info update")
	fnList, err := r.fissionClient.CoreV1().Functions(pkg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		e := "error getting function list"
		logger.Error(err, e)
		buildLogs += fmt.Sprintf("%s: %v\n", e, err)
		r.markBuildFailed(ctx, logger, pkg, buildLogs)
		r.propagateFunctionFailure(ctx, logger, pkg)
		return ctrl.Result{}, nil
	}

	// A package may be used by multiple functions. Point functions that still
	// reference the old package resource version at the freshly built one.
	if _, err := r.restampReferencingFunctions(ctx, pkg, fnList.Items, false); err != nil {
		e := "error updating function package resource version"
		logger.Error(err, e)
		buildLogs += fmt.Sprintf("%s: %v\n", e, err)
		r.markBuildFailed(ctx, logger, pkg, buildLogs)
		markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, false)
		return ctrl.Result{}, nil
	}

	// updated is the live object after the status write, so its Spec reflects
	// any edit that landed WHILE this build ran. Its status carries the hash of
	// what was actually built.
	updated, err := updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusSucceeded, buildLogs, uploadResp)
	if err != nil {
		logger.Error(err, "error updating package info")
		r.markBuildFailed(ctx, logger, pkg, buildLogs)
		markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, false)
		return ctrl.Result{}, nil
	}

	// Surface the build outcome on every Function that references this package
	// so its Ready/PackageReady conditions track package readiness.
	markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, true)
	logger.Info("completed package build request")

	// A content edit that landed mid-build was dropped by the trigger predicate
	// (nothing may enqueue a second build while one is in flight), and the
	// status-only write we just made does not enqueue either — the spec is
	// identical on both sides of it. Nothing else would ever notice, so the
	// package would sit correct-but-stale: hash says A, spec says B, no event
	// coming. Requeue ourselves instead of loosening the predicate, which would
	// re-admit the double-build this suppression exists to prevent.
	if updated != nil && PackageContentHash(updated.Spec) != updated.Status.ContentHash {
		logger.Info("package content changed while the build was in flight, requeuing to converge")
		return ctrl.Result{RequeueAfter: convergeRequeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

// failBuild records a terminal build failure on the package and propagates it to
// dependent functions. It returns (empty result, nil) so the reconcile stops
// without requeuing — the failure is terminal until the package is re-triggered.
func (r *PackageReconciler) failBuild(ctx context.Context, logger logr.Logger, pkg *fv1.Package, msg string) (ctrl.Result, error) {
	r.markBuildFailed(ctx, logger, pkg, msg)
	r.propagateFunctionFailure(ctx, logger, pkg)
	return ctrl.Result{}, nil
}

// markBuildFailed writes BuildStatusFailed with the given log, logging (but not
// returning) a status-write error since the caller is already on a failure path.
func (r *PackageReconciler) markBuildFailed(ctx context.Context, logger logr.Logger, pkg *fv1.Package, buildLogs string) {
	if _, err := updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusFailed, buildLogs, nil); err != nil {
		logger.Error(err, "error updating package to failed state")
	}
}

// readyBuilderPod returns the environment's ready builder pod (matched by the env
// name/namespace/resourceVersion labels), or nil when none is ready yet. A pod
// that has not yet published any container status is treated as not-ready so we
// keep waiting rather than build against a starting pod. The pod itself is
// returned (not just a bool) so the caller can read its key-scheme annotation to
// pick version-aware signing (builderSigningNamespace).
func (r *PackageReconciler) readyBuilderPod(ctx context.Context, env *fv1.Environment, builderNs string) (*apiv1.Pod, error) {
	sel := map[string]string{
		LABEL_ENV_NAME:            env.Name,
		LABEL_ENV_NAMESPACE:       builderNs,
		LABEL_ENV_RESOURCEVERSION: env.ResourceVersion,
	}
	podList, err := r.kubernetesClient.CoreV1().Pods(builderNs).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(sel).AsSelector().String(),
	})
	if err != nil {
		return nil, fmt.Errorf("error listing builder pods for environment %q in namespace %s: %w", env.Name, builderNs, err)
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if len(pod.Status.ContainerStatuses) == 0 {
			continue
		}
		ready := true
		for _, cStatus := range pod.Status.ContainerStatuses {
			ready = ready && cStatus.Ready
		}
		if ready {
			return pod, nil
		}
	}
	return nil, nil
}

// propagateFunctionFailure marks every Function referencing pkg with
// PackageReady=False / Ready=False. Used by build failure paths that don't have
// a pre-fetched fnList in scope (the success path passes its own list).
func (r *PackageReconciler) propagateFunctionFailure(ctx context.Context, logger logr.Logger, pkg *fv1.Package) {
	fnList, err := r.fissionClient.CoreV1().Functions(pkg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.V(1).Info("function-failure propagation: list failed", "namespace", pkg.Namespace, "error", err)
		return
	}
	markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, false)
}

// buildTriggerPredicate decides which Package events enqueue a reconcile. It
// replaces GenerationChangedPredicate, which would drop the rebuild trigger: a
// rebuild is requested by setting Status.BuildStatus = pending through the
// /status subresource, which leaves Generation unchanged.
//
//   - Create: always enqueue — to set the initial status or build a freshly
//     applied pending package. This also fires for every existing Package when
//     the controller starts and re-lists, so an interrupted build resumes.
//   - Update: enqueue when BuildStatus transitions INTO pending, OR when the
//     SPEC changed (Generation moved). The reconciler's own
//     running/succeeded/failed/none status writes are therefore dropped — it
//     neither re-triggers itself nor risks double-building off a stale cache
//     read of its own status write — while a Git-applied archive or OCI digest
//     change still gets a reconcile. That second clause compares CONTENT
//     HASHES, not Generation: the build writes spec.Deployment on success, so a
//     Generation test would re-enqueue the reconciler on its own output. It is
//     also suppressed while a build is pending or running, since a build
//     already in flight picks up whatever the spec now says. Without it the
//     content-change path (RFC-0029 §3) would be unreachable — applying a new
//     digest changes the spec, not the status, so nothing would enqueue.
//   - Delete / Generic: dropped — a deleted Package has no builder state to
//     tear down.
func buildTriggerPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPkg, ok1 := e.ObjectOld.(*fv1.Package)
			newPkg, ok2 := e.ObjectNew.(*fv1.Package)
			if !ok1 || !ok2 {
				return false
			}
			if newPkg.Status.BuildStatus == fv1.BuildStatusPending &&
				oldPkg.Status.BuildStatus != fv1.BuildStatusPending {
				return true
			}
			// A build already requested or in flight will pick up whatever the
			// spec now says, so a content change must NOT enqueue on top of it.
			// Without this guard the two signals — the status poke and the
			// content change — race: the second reconcile lands while the
			// status is still pending/running, re-enters build(), and the
			// package is built (and its functions re-stamped, and an RFC-0025
			// version minted) twice for one change.
			if newPkg.Status.BuildStatus == fv1.BuildStatusPending ||
				newPkg.Status.BuildStatus == fv1.BuildStatusRunning {
				return false
			}
			// Otherwise a content change enqueues. Comparing hashes rather than
			// Generation is deliberate: the build itself writes spec.Deployment
			// on success, so a Generation test would re-enqueue the reconciler
			// on its own output. The hash ignores the build's output for source
			// packages, so the reconciler cannot trigger itself.
			return PackageContentHash(newPkg.Spec) != PackageContentHash(oldPkg.Spec)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// setInitialBuildStatus sets initial build status to a package if it is empty.
// This normally occurs when the user applies package YAML files that have no
// status field through kubectl.
func setInitialBuildStatus(ctx context.Context, fissionClient versioned.Interface, pkg *fv1.Package) (*fv1.Package, error) {
	packages := fissionClient.CoreV1().Packages(pkg.Namespace)
	name := pkg.Name

	// Re-get on conflict: a fast user/CLI update can race this initial status
	// write. The derived status is a pure function of the spec archives, so a
	// retry on the fresh object is idempotent.
	var out *fv1.Package
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, gerr := packages.Get(ctx, name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		// Preserve any Conditions a previous reconcile may have written.
		// ContentHash is seeded here too: a Git-applied package lands on this
		// branch (the /status subresource strips status on create) and the
		// resulting status write does not re-enqueue, so without seeding here
		// the package would carry an empty hash and swallow its FIRST content
		// change as a seed (RFC-0029 §3).
		fresh.Status = fv1.PackageStatus{
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
			Conditions:          fresh.Status.Conditions,
			ContentHash:         PackageContentHash(fresh.Spec),
		}
		if !fresh.Spec.Deployment.IsEmpty() {
			// if the deployment archive is not empty,
			// we assume it's a deployable package no matter
			// the source archive is empty or not.
			fresh.Status.BuildStatus = fv1.BuildStatusNone
		} else if !fresh.Spec.Source.IsEmpty() {
			fresh.Status.BuildStatus = fv1.BuildStatusPending
		} else {
			// mark package failed since we cannot do anything with it.
			fresh.Status.BuildStatus = fv1.BuildStatusFailed
			fresh.Status.BuildLog = "Both deploy and source archive are empty"
		}
		setPackageBuildCondition(&fresh.Status, fresh.Status.BuildStatus, fresh.Status.BuildLog, fresh.Generation)
		var uerr error
		out, uerr = packages.UpdateStatus(ctx, fresh, metav1.UpdateOptions{})
		return uerr
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// restampReferencingFunctions points every Function that references pkg at its
// current ResourceVersion, which is what makes the executor recycle their pods
// onto the new content.
//
// Extracted so the no-build path can share it: a deploy-only or OCI package
// settles at BuildStatusNone and never reaches build(), so keying this on
// build success alone left exactly the digest-pinned-OCI-in-Git golden path
// with no propagation at all (RFC-0029 §3).
// honorOptOut is true only on the content-driven path: the build-success
// re-stamp predates this feature and restamped unconditionally, so applying the
// opt-out there would silently strand a function on pre-rebuild code while
// still reporting PackageReady=True.
func (r *PackageReconciler) restampReferencingFunctions(ctx context.Context, pkg *fv1.Package, fns []fv1.Function, honorOptOut bool) (int, error) {
	stamped := 0
	for i := range fns {
		fn := &fns[i]
		if fn.Spec.Package.PackageRef.Name != pkg.Name ||
			fn.Spec.Package.PackageRef.Namespace != pkg.Namespace ||
			fn.Spec.Package.PackageRef.ResourceVersion == pkg.ResourceVersion {
			continue
		}
		// Opt-out for anyone who relies on rolling their functions by hand.
		// The supported way to pin code is an RFC-0025 FunctionVersion/alias,
		// which is a first-class object rather than a stale stamp — the
		// annotation exists for installs that predate it.
		if honorOptOut && fn.Annotations[fv1.PackageAutoFollowDisabledAnnotation] == "true" {
			continue
		}
		fn.Spec.Package.PackageRef.ResourceVersion = pkg.ResourceVersion
		if _, err := r.fissionClient.CoreV1().Functions(fn.Namespace).Update(ctx, fn, metav1.UpdateOptions{}); err != nil {
			return stamped, err
		}
		stamped++
	}
	return stamped, nil
}
