// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/mqtrigger/validator"
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

	// Validate the message-queue type and topic(s) here. These need the backend
	// validator registry (pkg/mqtrigger/validator), which CEL on the CRD cannot
	// express, so an invalid spec surfaces as BindingReady=False instead of an
	// admission rejection (the fission CLI still rejects bad specs client-side).
	// We neither subscribe nor requeue a permanently-invalid trigger.
	mqType := string(mqt.Spec.MessageQueueType)
	if !validator.IsValidMessageQueue(mqType, mqt.Spec.MqtKind) {
		r.setBindingFalse(ctx, mqt, fv1.MessageQueueTriggerReasonInvalidQueueType,
			fmt.Sprintf("unsupported message queue type %q (kind %q)", mqType, mqt.Spec.MqtKind))
		return ctrl.Result{}, nil
	}
	if !validator.IsValidTopic(mqType, mqt.Spec.Topic, mqt.Spec.MqtKind) {
		r.setBindingFalse(ctx, mqt, fv1.MessageQueueTriggerReasonInvalidTopic,
			fmt.Sprintf("invalid topic %q for message queue type %q", mqt.Spec.Topic, mqType))
		return ctrl.Result{}, nil
	}
	if mqt.Spec.ResponseTopic != "" && !validator.IsValidTopic(mqType, mqt.Spec.ResponseTopic, mqt.Spec.MqtKind) {
		r.setBindingFalse(ctx, mqt, fv1.MessageQueueTriggerReasonInvalidTopic,
			fmt.Sprintf("invalid response topic %q for message queue type %q", mqt.Spec.ResponseTopic, mqType))
		return ctrl.Result{}, nil
	}

	// RegisterTrigger subscribes on first sight and re-subscribes on a spec
	// change (it checks the in-memory map). A failure returns an error so the
	// workqueue requeues with backoff.
	if err := r.mqtMgr.RegisterTrigger(mqt); err != nil {
		return ctrl.Result{}, err
	}

	controller.SetConditions(ctx, r.logger, r.client, mqt,
		metav1.Condition{
			Type: fv1.MessageQueueTriggerConditionBindingReady, Status: metav1.ConditionTrue,
			Reason: fv1.MessageQueueTriggerReasonSubscribed, Message: "subscribed to topic " + mqt.Spec.Topic,
		},
		metav1.Condition{
			Type: fv1.MessageQueueTriggerConditionReady, Status: metav1.ConditionTrue,
			Reason: fv1.MessageQueueTriggerReasonSubscribed, Message: "trigger is consuming messages",
		},
	)
	return ctrl.Result{}, nil
}

// setBindingFalse records a permanently-invalid binding as BindingReady=False +
// Ready=False. The reconcile returns nil (no requeue) — a malformed topic/type
// will not become valid without a spec change, which re-triggers reconcile.
func (r *MessageQueueTriggerReconciler) setBindingFalse(ctx context.Context, mqt *fv1.MessageQueueTrigger, reason, msg string) {
	controller.SetConditions(ctx, r.logger, r.client, mqt,
		metav1.Condition{
			Type: fv1.MessageQueueTriggerConditionBindingReady, Status: metav1.ConditionFalse,
			Reason: reason, Message: msg,
		},
		metav1.Condition{
			Type: fv1.MessageQueueTriggerConditionReady, Status: metav1.ConditionFalse,
			Reason: reason, Message: "trigger is not active: " + msg,
		},
	)
}
