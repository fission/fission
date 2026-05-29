// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// TimeTriggerReconciler keeps the in-process cron schedules in sync with the
// TimeTrigger CRDs. It replaces the previous informer + event-handler
// (TimerSync): the controller-runtime controller delivers the same add/update/
// delete events through a rate-limited workqueue, and the GenerationChangedPredicate
// (applied in controller.Register) drops the status-only updates the old
// UpdateFunc filtered by hand.
//
// Reads go through the Manager's cache-backed client (the same informer cache
// the watch populates), and status writes go through client.Status().Update.
type TimeTriggerReconciler struct {
	logger logr.Logger
	client client.Client
	timer  *Timer
}

func (r *TimeTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tt := &fv1.TimeTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, tt); err != nil {
		if apierrors.IsNotFound(err) {
			r.timer.remove(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	r.timer.addUpdate(tt)

	// Best-effort Scheduled + Ready conditions. Status writes never gate the
	// schedule; SetConditions skips the write when nothing changed.
	controller.SetConditions(ctx, r.logger, r.client, tt,
		metav1.Condition{
			Type: fv1.TimeTriggerConditionScheduled, Status: metav1.ConditionTrue,
			Reason:  fv1.TimeTriggerReasonCronRegistered,
			Message: "timer registered cron schedule " + tt.Spec.Cron,
		},
		metav1.Condition{
			Type: fv1.TimeTriggerConditionReady, Status: metav1.ConditionTrue,
			Reason:  fv1.TimeTriggerReasonCronRegistered,
			Message: "trigger is firing on schedule",
		},
	)
	return ctrl.Result{}, nil
}
