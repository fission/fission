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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// TimeTriggerReconciler keeps the in-process cron schedules in sync with the
// TimeTrigger CRDs. It replaces the previous informer + event-handler
// (TimerSync): the controller-runtime controller delivers the same add/update/
// delete events through a rate-limited workqueue, and the GenerationChangedPredicate
// (applied in controller.Register) drops the status-only updates the old
// UpdateFunc filtered by hand.
type TimeTriggerReconciler struct {
	logger        logr.Logger
	fissionClient versioned.Interface
	timer         *Timer
}

func (r *TimeTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tt, err := r.fissionClient.CoreV1().TimeTriggers(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		r.timer.remove(req.NamespacedName)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	r.timer.addUpdate(tt)

	// Best-effort Scheduled + Ready conditions. Status writes never gate the
	// schedule; SetConditions fast-paths the no-op case.
	controller.SetConditions(ctx, r.logger, r.fissionClient.CoreV1().TimeTriggers(req.Namespace), tt,
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
