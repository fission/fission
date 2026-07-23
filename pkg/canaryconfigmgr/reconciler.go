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
// status is written through a client.Status().Patch on the /status subresource
// (see writeStatus).
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
	if interval <= 0 {
		// time.ParseDuration accepts "0s" and negatives; requeuing with a
		// non-positive RequeueAfter would spin a hot reconcile loop. Treat it
		// as an unworkable spec and stop until it is fixed.
		r.logger.Info("non-positive WeightIncrementDuration; not scheduling canary",
			"name", cfg.Name, "namespace", cfg.Namespace, "duration", cfg.Spec.WeightIncrementDuration)
		return ctrl.Result{}, nil
	}
	if cfg.Spec.WeightIncrement <= 0 {
		// WeightIncrement is optional and unbounded in the CRD. A zero increment
		// would requeue forever without ever shifting traffic; a negative one
		// would move weights the wrong way (and could exceed bounds). Treat it
		// as an unworkable spec and stop until it is fixed.
		r.logger.Info("non-positive WeightIncrement; not scheduling canary",
			"name", cfg.Name, "namespace", cfg.Namespace, "weight_increment", cfg.Spec.WeightIncrement)
		return ctrl.Result{}, nil
	}

	out, err := r.mgr.step(ctx, cfg)
	if err != nil {
		return ctrl.Result{}, err
	}
	if out.terminalStatus != "" {
		// The weights are already at their terminal split. If the status write
		// fails the GenerationChangedPredicate would drop the resulting
		// status-only event, leaving the config stranded in Pending with no
		// reschedule — so surface the error and let the workqueue requeue until
		// the terminal status sticks.
		if err := r.writeStatus(ctx, cfg, out.terminalStatus, out.message); err != nil {
			r.logger.Error(err, "failed to write terminal canary status; requeuing",
				"name", cfg.Name, "namespace", cfg.Namespace, "status", out.terminalStatus)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if out.requeue {
		// Keep Progressing/Ready asserted (the write is skipped when unchanged).
		// The RequeueAfter below reschedules regardless, so a failed status
		// write here is best-effort and not fatal.
		if err := r.writeStatus(ctx, cfg, fv1.CanaryConfigStatusPending, ""); err != nil {
			r.logger.V(1).Info("canary progressing status update failed",
				"name", cfg.Name, "namespace", cfg.Namespace, "error", err)
		}
		return ctrl.Result{RequeueAfter: interval}, nil
	}
	return ctrl.Result{}, nil
}

// writeStatus sets the bare status string and the mirrored Progressing/Ready
// conditions, persisting through the /status subresource. message, when
// non-empty, overrides the condition's default per-status text — used for the
// alias-mode reconcile-start validation refusals, where the default Failed
// message ("traffic rolled back") is wrong (see stepOutcome.message). The
// write is skipped (and nil returned) when nothing would change — important
// on the Pending fast path that runs every WeightIncrementDuration. The
// caller decides whether a write failure should requeue.
//
// It uses a status Patch computed from a pre-mutation DeepCopy
// (client.MergeFrom) rather than Update: the merge patch carries no
// ResourceVersion precondition, so a status write never fails with a
// conflict against the cache-read object. That matters on the terminal path —
// a conflict there would requeue with the rollout still Pending, and step()
// would run again and could shift the HTTPTrigger weights a second time after
// a rollback.
func (r *CanaryConfigReconciler) writeStatus(ctx context.Context, cfg *fv1.CanaryConfig, status string, message string) error {
	original := cfg.DeepCopy()
	changed := cfg.Status.Status != status
	cfg.Status.Status = status
	if setCanaryConfigConditions(&cfg.Status, status, cfg.Generation, message) {
		changed = true
	}
	if !changed {
		return nil
	}
	return r.client.Status().Patch(ctx, cfg, client.MergeFrom(original))
}
