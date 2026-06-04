// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
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
	podPollInterval  time.Duration
	poolMgr          *BuilderPoolManager
	scale            deploymentScaler
}

func makePackageReconciler(logger logr.Logger, client client.Client, fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface, storageSvcUrl string, poolMgr *BuilderPoolManager) *PackageReconciler {
	return &PackageReconciler{
		logger:           logger.WithName("package_reconciler"),
		client:           client,
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		nsResolver:       utils.DefaultNSResolver(),
		storageSvcUrl:    storageSvcUrl,
		podPollInterval:  builderPodPollInterval,
		poolMgr:          poolMgr,
		scale:            k8sDeploymentScaler(kubernetesClient, logger),
	}
}

func (r *PackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pkg := &fv1.Package{}
	if err := r.client.Get(ctx, req.NamespacedName, pkg); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: drop any in-flight demand slot the package still held while
			// requeue-waiting for a builder pod, so demand/idle accounting does not
			// leak. (The deployment archive lives in storagesvc and is pruned
			// independently.)
			r.poolMgr.RemoveBuild(req.Namespace, req.Name)
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
			return ctrl.Result{}, fmt.Errorf("error setting initial package build status: %w", err)
		}
		return ctrl.Result{}, nil
	case fv1.BuildStatusPending, fv1.BuildStatusRunning:
		// pending: a build is requested. running: a previous build was
		// interrupted (e.g. a buildermgr restart re-lists this Package as a
		// Create) — re-drive it, the build is idempotent.
		return r.build(ctx, pkg)
	default:
		// none / succeeded / failed are terminal until the package is
		// re-triggered (BuildStatus -> pending).
		return ctrl.Result{}, nil
	}
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

	// Record this build as in-flight for the environment. demand is the number
	// of distinct concurrent builds; it drives how many builder pods we provision
	// (one per concurrent build, capped at the env's pool size) and keeps the idle
	// reaper from scaling the builder down mid-build. StartBuild is idempotent in
	// this package, so the requeues below never inflate demand.
	demand := r.poolMgr.StartBuild(env, builderNs, pkg)

	// Scale the builder deployment UP toward demand (capped at the pool size).
	// Scale-down is left entirely to the idle reaper, so a pod running a build is
	// never terminated underneath it.
	if serr := r.scaleBuilderForDemand(ctx, env, builderNs, demand); serr != nil {
		if apierrors.IsNotFound(serr) {
			// The EnvironmentReconciler creates the builder Deployment and can
			// race behind a freshly applied Package. A missing deployment is
			// transient — wait for it rather than failing the build terminally.
			logger.Info("builder deployment not created yet, will retry")
			return ctrl.Result{RequeueAfter: r.podPollInterval}, nil
		}
		logger.Error(serr, "error scaling builder for demand")
		r.poolMgr.FinishBuild(env.UID, pkg)
		return r.failBuild(ctx, logger, pkg, fmt.Sprintf("error scaling builder: %v", serr))
	}

	// Claim a Ready builder pod that no other build is using, so this build gets
	// its own dedicated pod. Pinning fetch+build+upload to one pod IP is required
	// for correctness with more than one replica (the fetched source lives on the
	// pod's local volume). Requeue rather than block a worker while we wait — the
	// Package stays "pending"/"running" and visibly in-flight, so it still counts
	// as demand. (The EnvironmentReconciler owns creating the builder Deployment;
	// here we wait for a pod of it to become Ready and free.)
	readyIPs, err := r.readyBuilderPodIPs(ctx, env, builderNs)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(readyIPs) == 0 {
		logger.Info("environment builder pod not ready, will retry")
		return ctrl.Result{RequeueAfter: r.podPollInterval}, nil
	}
	podIP, claimed := r.poolMgr.ClaimFreeBuilderPod(env.UID, readyIPs)
	if !claimed {
		// Every Ready pod is busy with another build: we are at the pool cap and
		// must queue. Requeue and retry; the idle reaper leaves us alone while in-flight.
		logger.Info("all ready builder pods are busy, will retry", "ready_pods", len(readyIPs))
		return ctrl.Result{RequeueAfter: r.podPollInterval}, nil
	}
	defer r.poolMgr.ReleaseBuilderPod(env.UID, podIP)
	defer r.poolMgr.FinishBuild(env.UID, pkg)
	logger = logger.WithValues("builder_pod_ip", podIP)

	logger.Info("starting build for package")
	pkg, err = updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusRunning, "", nil)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error setting package running state: %w", err)
	}

	uploadResp, buildLogs, err := buildPackage(ctx, logger, r.fissionClient, builderNs, podIP, r.storageSvcUrl, pkg)
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
	for i := range fnList.Items {
		fn := &fnList.Items[i]
		if fn.Spec.Package.PackageRef.Name == pkg.Name &&
			fn.Spec.Package.PackageRef.Namespace == pkg.Namespace &&
			fn.Spec.Package.PackageRef.ResourceVersion != pkg.ResourceVersion {
			fn.Spec.Package.PackageRef.ResourceVersion = pkg.ResourceVersion
			if _, err := r.fissionClient.CoreV1().Functions(fn.Namespace).Update(ctx, fn, metav1.UpdateOptions{}); err != nil {
				e := "error updating function package resource version"
				logger.Error(err, e)
				buildLogs += fmt.Sprintf("%s: %v\n", e, err)
				r.markBuildFailed(ctx, logger, pkg, buildLogs)
				markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, false)
				return ctrl.Result{}, nil
			}
		}
	}

	// Discard the return: updatePackage returns a nil package on failure, and the
	// error branch below still needs the live pkg for markBuildFailed /
	// markFunctionsForPackage (both dereference pkg.Name/Namespace).
	if _, err = updatePackage(ctx, logger, r.fissionClient, pkg, fv1.BuildStatusSucceeded, buildLogs, uploadResp); err != nil {
		logger.Error(err, "error updating package info")
		r.markBuildFailed(ctx, logger, pkg, buildLogs)
		markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, false)
		return ctrl.Result{}, nil
	}

	// Surface the build outcome on every Function that references this package
	// so its Ready/PackageReady conditions track package readiness.
	markFunctionsForPackage(ctx, logger, r.fissionClient, fnList.Items, pkg, true)
	logger.Info("completed package build request")
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

// scaleBuilderForDemand raises the environment's builder deployment toward the
// number of concurrent in-flight builds (demand), capped at the env's pool size
// (spec.builder.poolsize, default 1). It only ever scales UP: scale-down is the
// idle reaper's job, so a pod running a build is never terminated underneath it.
// A no-op when the deployment is already at or above the desired replica count.
func (r *PackageReconciler) scaleBuilderForDemand(ctx context.Context, env *fv1.Environment, builderNs string, demand int32) error {
	builderName := fmt.Sprintf("%v-%v", env.Name, env.ResourceVersion)
	desired := demand
	if desired < 1 {
		desired = 1
	}
	if maxPods := builderPoolSize(env); desired > maxPods {
		desired = maxPods
	}
	// Read the current replica count via a plain Get (not the scale subresource)
	// so the "only scale up" decision is testable against the fake clientset. A
	// missing deployment surfaces as NotFound, which build() maps to a requeue.
	dep, err := r.kubernetesClient.AppsV1().Deployments(builderNs).Get(ctx, builderName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting builder deployment %q in namespace %s: %w", builderName, builderNs, err)
	}
	current := int32(0)
	if dep.Spec.Replicas != nil {
		current = *dep.Spec.Replicas
	}
	if current >= desired {
		return nil
	}
	r.logger.Info("scaling builder deployment up for concurrent builds",
		"builder", builderName, "namespace", builderNs,
		"current_replicas", current, "desired_replicas", desired, "demand", demand)
	return r.scale(ctx, builderNs, builderName, desired)
}

// readyBuilderPodIPs lists the environment's builder pods (matched by the env
// name/namespace/resourceVersion labels) and returns the pod IPs of those that
// are routable: all containers Ready and a non-empty PodIP. A pod that has not
// yet published a container status or has no IP is skipped, so a build is never
// routed at a not-yet-serving pod.
func (r *PackageReconciler) readyBuilderPodIPs(ctx context.Context, env *fv1.Environment, builderNs string) ([]string, error) {
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
	var ips []string
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.PodIP == "" || len(pod.Status.ContainerStatuses) == 0 {
			continue
		}
		ready := true
		for _, cStatus := range pod.Status.ContainerStatuses {
			ready = ready && cStatus.Ready
		}
		if ready {
			ips = append(ips, pod.Status.PodIP)
		}
	}
	return ips, nil
}

// builderPodReady reports whether at least one builder pod for the environment is
// routably Ready (see readyBuilderPodIPs).
func (r *PackageReconciler) builderPodReady(ctx context.Context, env *fv1.Environment, builderNs string) (bool, error) {
	ips, err := r.readyBuilderPodIPs(ctx, env, builderNs)
	if err != nil {
		return false, err
	}
	return len(ips) > 0, nil
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
//   - Update: enqueue only when BuildStatus transitions INTO pending. The
//     reconciler's own running/succeeded/failed/none writes are therefore
//     dropped, so it neither re-triggers itself nor risks double-building off a
//     stale cache read of its own status write.
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
			return newPkg.Status.BuildStatus == fv1.BuildStatusPending &&
				oldPkg.Status.BuildStatus != fv1.BuildStatusPending
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
		fresh.Status = fv1.PackageStatus{
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
			Conditions:          fresh.Status.Conditions,
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
