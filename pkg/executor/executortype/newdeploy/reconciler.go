// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
)

// funcManager is the subset of *NewDeploy the Function handlers drive. Defined as
// an interface so the reconcile routing (create-vs-update on the last-reconciled
// object) is unit-testable with a fake.
type funcManager interface {
	createFunction(context.Context, *fv1.Function) (*fscache.FuncSvc, error)
	updateFunction(context.Context, *fv1.Function, *fv1.Function) error
	deleteFunction(context.Context, *fv1.Function) error
	reconcileDeploymentSpec(context.Context, *fv1.Function) error
	// resourcesExist reports whether the function's backing Deployment and Service
	// are present (read from the Manager cache). False means they drifted away
	// (deleted out-of-band) and must be recreated.
	resourcesExist(context.Context, *fv1.Function) (bool, error)
}

// envFunctionUpdater is the subset of *NewDeploy the Environment handler drives —
// propagating an environment image change to its functions' deployments. An
// interface so the routing is unit-testable with a fake.
type envFunctionUpdater interface {
	updateEnvFunctions(context.Context, *fv1.Environment) error
}

// ReconcileFunction satisfies executortype.FuncReconciler for newdeploy-backed
// functions (Deployment/Service/HPA). The shared Function reconciler owns the
// last-reconciled cache and executor-type transitions, so this only sees same-type
// create/update:
//
//   - create (old == nil), and any reconcile whose backing objects drifted away:
//     createAndConverge.
//   - update (old != nil): updateFunction(old, fn), which diffs HPA min/max/metrics
//     and secret/configmap/package changes.
func (deploy *NewDeploy) ReconcileFunction(ctx context.Context, old, fn *fv1.Function) error {
	return reconcileNewdeployFunc(ctx, deploy, old, fn)
}

// DeleteFunction satisfies executortype.FuncReconciler: it tears down the
// function's Deployment/Service/HPA.
func (deploy *NewDeploy) DeleteFunction(ctx context.Context, fn *fv1.Function) error {
	return deploy.deleteFunction(ctx, fn)
}

// reconcileNewdeployFunc holds the create-vs-update routing, split out so it is
// unit-testable with a fake funcManager. It is level-triggered: a reconcile of a
// managed function whose backing Deployment/Service drifted away (deleted
// out-of-band, surfaced by the drift watch) recreates them via the idempotent
// get-or-create path rather than diffing a no-longer-existent object. (The
// request path also recreates on the next invocation; this keeps MinScale>0
// functions warm without waiting for one.)
func reconcileNewdeployFunc(ctx context.Context, mgr funcManager, old, fn *fv1.Function) error {
	if old == nil {
		return createAndConverge(ctx, mgr, fn)
	}
	exist, err := mgr.resourcesExist(ctx, fn)
	if err != nil {
		return err
	}
	if !exist {
		return createAndConverge(ctx, mgr, fn)
	}
	return mgr.updateFunction(ctx, old, fn)
}

// createAndConverge is the create-routed path both branches above take: create (or
// adopt) the function's backing objects, then bring the deployment to the current
// spec. reconcileDeploymentSpec is a no-op when the deployment already reflects fn.
//
// The second step is not optional. createFunction only adopts/scales an existing
// deployment — it never rewrites the pod spec — so when this reconcile is the ONLY
// carrier of a spec change, adopting without a respec consumes the update
// permanently: lastReconciled stores the new spec, GenerationChangedPredicate
// filters resyncs, and no event ever re-fires (measured in the wild as a function
// serving its old entrypoint indefinitely after `fn update`). Two ways in: a create
// and a later update coalescing into one first reconcile — the router specialized
// the function on-demand just before `fission fn update` landed — or a cache
// false-alarm where !exist saw a Deployment that had lagged out of the Manager
// cache.
func createAndConverge(ctx context.Context, mgr funcManager, fn *fv1.Function) error {
	if _, err := mgr.createFunction(ctx, fn); err != nil {
		return err
	}
	return mgr.reconcileDeploymentSpec(ctx, fn)
}

// ReconcileEnvironment satisfies executortype.EnvReconciler: it propagates an
// environment's runtime-image change to the deployments of its newdeploy
// functions. It acts only on an image change (the informer handler it replaced
// treated Add/Delete as no-ops), so first sight (old == nil) caches only and a
// later event rolls the functions only when the image actually changed. It asks
// for no periodic requeue (0); the shared dispatcher's requeue is driven by the
// pool manager's resync.
func (deploy *NewDeploy) ReconcileEnvironment(ctx context.Context, old, env *fv1.Environment) (time.Duration, error) {
	return reconcileNewdeployEnv(ctx, deploy, old, env)
}

// CleanupEnvironment is a no-op: function cleanup is driven by the Function watch,
// so a deleted Environment needs no newdeploy-side action (matching the old
// handler, which did nothing on delete).
func (deploy *NewDeploy) CleanupEnvironment(context.Context, *fv1.Environment) {}

// reconcileNewdeployEnv holds the image-diff routing, split out so it is
// unit-testable with a fake envFunctionUpdater.
func reconcileNewdeployEnv(ctx context.Context, up envFunctionUpdater, old, env *fv1.Environment) (time.Duration, error) {
	if old == nil {
		// First sight: nothing to diff against, and functions already track their
		// environment. Matches the old AddFunc no-op.
		return 0, nil
	}
	if old.Spec.Runtime.Image != env.Spec.Runtime.Image {
		if err := up.updateEnvFunctions(ctx, env); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// RegisterReconcilers registers no type-specific watches: newdeploy's Function
// and Environment reconciles are handled by the shared executor-level reconcilers
// (see funcreconciler/envreconciler RegisterReconciler), which newdeploy plugs
// into via FuncReconciler and EnvReconciler. It captures the Manager's
// cache-backed client for IsValid's Deployment/Service reads (replacing the
// per-type informer factory).
func (deploy *NewDeploy) RegisterReconcilers(mgr ctrl.Manager) error {
	deploy.crClient = mgr.GetClient()
	return nil
}
