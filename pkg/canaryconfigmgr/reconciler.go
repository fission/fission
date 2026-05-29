// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfigmgr

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// CanaryConfigReconciler drives a canary rollout one weight increment per
// reconcile, rescheduling itself with RequeueAfter(WeightIncrementDuration).
// It replaces the per-config time.Ticker + cancel-func map the old
// informer-based manager kept: controller-runtime's workqueue owns scheduling,
// the GenerationChangedPredicate (in controller.Register) drops the
// reconciler's own status writes, and a deleted CanaryConfig simply stops being
// requeued — there is no in-memory state to tear down.
//
// Reads go through the Manager's cache-backed client; the terminal/Progressing
// status is written through client.Status().Update on the /status subresource.
type CanaryConfigReconciler struct {
	logger logr.Logger
	client client.Client
	mgr    *canaryConfigMgr
}

func (r *CanaryConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cfg := &fv1.CanaryConfig{}
	if err := r.client.Get(ctx, req.NamespacedName, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: nothing to clean up — a rollout has no goroutine or timer
			// to stop, only its workqueue entry, which is already drained.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Only pending rollouts are progressed; Succeeded/Failed/Aborted are
	// terminal. An empty status is treated as pending: with the /status
	// subresource enabled the API server drops the status set at create time,
	// so a freshly created CanaryConfig first reaches us with an empty status.
	switch cfg.Status.Status {
	case fv1.CanaryConfigStatusSucceeded, fv1.CanaryConfigStatusFailed, fv1.CanaryConfigStatusAborted:
		return ctrl.Result{}, nil
	}

	interval, err := time.ParseDuration(cfg.Spec.WeightIncrementDuration)
	if err != nil {
		// Unworkable spec: log and stop. A spec fix bumps the generation and
		// re-triggers (status-only changes are filtered out by the predicate).
		r.logger.Error(err, "invalid WeightIncrementDuration; not scheduling canary",
			"name", cfg.Name, "namespace", cfg.Namespace, "duration", cfg.Spec.WeightIncrementDuration)
		return ctrl.Result{}, nil
	}

	out, err := r.mgr.step(ctx, cfg)
	if err != nil {
		return ctrl.Result{}, err
	}
	if out.terminalStatus != "" {
		r.writeStatus(ctx, cfg, out.terminalStatus)
		return ctrl.Result{}, nil
	}
	if out.requeue {
		// Keep Progressing/Ready asserted (the write is skipped when unchanged)
		// and step again after one increment window.
		r.writeStatus(ctx, cfg, fv1.CanaryConfigStatusPending)
		return ctrl.Result{RequeueAfter: interval}, nil
	}
	return ctrl.Result{}, nil
}

// writeStatus sets the bare status string and the mirrored Progressing/Ready
// conditions, persisting through the /status subresource. The write is skipped
// when nothing would change — important on the Pending fast path that runs
// every WeightIncrementDuration. Best-effort: a failed status write never gates
// the rollout; the next reconcile reconverges.
func (r *CanaryConfigReconciler) writeStatus(ctx context.Context, cfg *fv1.CanaryConfig, status string) {
	changed := cfg.Status.Status != status
	cfg.Status.Status = status
	if setCanaryConfigConditions(&cfg.Status, status, cfg.Generation) {
		changed = true
	}
	if !changed {
		return
	}
	if err := r.client.Status().Update(ctx, cfg); err != nil {
		r.logger.V(1).Info("canary status update failed",
			"name", cfg.Name, "namespace", cfg.Namespace, "error", err)
	}
}
