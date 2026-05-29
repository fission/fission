// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatcher

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/controller"
)

// KubernetesWatchTriggerReconciler keeps the in-process Kubernetes watch
// subscriptions in sync with the KubernetesWatchTrigger CRDs. It replaces the
// previous informer + event-handler (WatchSync): controller-runtime delivers
// add/update/delete through a rate-limited workqueue, the
// GenerationChangedPredicate (in controller.Register) drops status-only
// updates, and a failed watch start is retried via the returned error instead
// of being swallowed.
//
// Reads go through the Manager's cache-backed client; status writes go through
// client.Status().Update.
type KubernetesWatchTriggerReconciler struct {
	logger      logr.Logger
	client      client.Client
	kubeWatcher *KubeWatcher
}

func (r *KubernetesWatchTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	w := &fv1.KubernetesWatchTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, w); err != nil {
		if apierrors.IsNotFound(err) {
			r.kubeWatcher.removeWatch(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if addErr := r.kubeWatcher.addWatch(ctx, w); addErr != nil {
		// err.Error() can be unbounded, so truncate before it reaches a condition.
		msg := conditions.TruncateMessage(addErr.Error())
		controller.SetConditions(ctx, r.logger, r.client, w,
			metav1.Condition{
				Type: fv1.KubernetesWatchTriggerConditionSubscribed, Status: metav1.ConditionFalse,
				Reason: fv1.KubernetesWatchTriggerReasonStartFailed, Message: msg,
			},
			metav1.Condition{
				Type: fv1.KubernetesWatchTriggerConditionReady, Status: metav1.ConditionFalse,
				Reason: fv1.KubernetesWatchTriggerReasonStartFailed, Message: msg,
			},
		)
		return ctrl.Result{}, addErr
	}

	controller.SetConditions(ctx, r.logger, r.client, w,
		metav1.Condition{
			Type: fv1.KubernetesWatchTriggerConditionSubscribed, Status: metav1.ConditionTrue,
			Reason: fv1.KubernetesWatchTriggerReasonSubscribed, Message: "watch loop is running",
		},
		metav1.Condition{
			Type: fv1.KubernetesWatchTriggerConditionReady, Status: metav1.ConditionTrue,
			Reason: fv1.KubernetesWatchTriggerReasonSubscribed, Message: "watch loop is running",
		},
	)
	return ctrl.Result{}, nil
}
