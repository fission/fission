// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// MessageQueueTriggerReconciler keeps the live message-queue subscriptions in
// sync with the MessageQueueTrigger CRDs. It replaces the previous informer +
// two rate-limited workqueues + 4+1 wait.Until workers: controller-runtime
// delivers add/update/delete through its own rate-limited workqueue
// (MaxConcurrentReconciles lets independent triggers reconcile in parallel),
// the GenerationChangedPredicate (in controller.Register) drops the status-only
// updates the old enqueueMqtUpdate filtered by generation, and a failed
// subscribe/unsubscribe is retried via the returned error instead of os.Exit.
//
// Reads go through the Manager's cache-backed client; the actual subscribe/
// unsubscribe side effects and the in-memory subscription map live in
// MessageQueueTriggerManager, whose service() actor serializes the map so
// concurrent reconciles are safe.
type MessageQueueTriggerReconciler struct {
	logger logr.Logger
	client client.Client
	mqtMgr *MessageQueueTriggerManager
}

// NewMessageQueueTriggerReconciler builds the reconciler. client is the
// Manager's cache-backed client; mqtMgr owns the live subscriptions.
func NewMessageQueueTriggerReconciler(logger logr.Logger, client client.Client, mqtMgr *MessageQueueTriggerManager) *MessageQueueTriggerReconciler {
	return &MessageQueueTriggerReconciler{
		logger: logger.WithName("messagequeuetrigger_reconciler"),
		client: client,
		mqtMgr: mqtMgr,
	}
}

func (r *MessageQueueTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// The subscription manager binds its leader-scoped context and starts its
	// actor in a sibling leader Runnable; block until that's done so we never
	// subscribe with a nil context or send on the actor channel before it serves.
	if err := r.mqtMgr.waitReady(ctx); err != nil {
		return ctrl.Result{}, err
	}

	mqt := &fv1.MessageQueueTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, mqt); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: tear down the subscription. Identified by name only —
			// the object is already gone.
			if err := r.mqtMgr.unsubscribe(req.NamespacedName); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Not this head's trigger: a different MQ type (another classic head owns
	// it) or a KEDA-kind trigger (the scaler manager owns it). Unsubscribe
	// rather than plain skip — a spec update can move a trigger away from this
	// head, and unsubscribe is a no-op when we never held a subscription.
	if !r.mqtMgr.Owns(mqt) {
		return ctrl.Result{}, r.mqtMgr.unsubscribe(req.NamespacedName)
	}

	// RegisterTrigger subscribes on first sight and re-subscribes on a spec
	// change (it checks the in-memory map). A failure returns an error so the
	// workqueue requeues with backoff.
	if err := r.mqtMgr.RegisterTrigger(mqt); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
