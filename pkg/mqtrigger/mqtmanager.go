// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
)

const (
	ADD_TRIGGER requestType = iota
	DELETE_TRIGGER
	GET_TRIGGER_SUBSCRIPTION
	UPDATE_TRIGGER_SUBSCRIPTION
)

type (
	requestType int

	// MessageQueueTriggerManager owns the live message-queue subscriptions. The
	// subscription map is mutated only from the single service() goroutine
	// (serialized via reqChan), so the MessageQueueTriggerReconciler can drive
	// add/update/delete from multiple concurrent reconciles without locking.
	MessageQueueTriggerManager struct {
		logger           logr.Logger
		reqChan          chan request
		triggers         map[string]*triggerSubscription
		fissionClient    versioned.Interface
		messageQueueType fv1.MessageQueueType
		messageQueue     messageQueue.MessageQueue

		// ctx is the leader-scoped parent context for all subscriptions, set by
		// Start (a leader-only Runnable). It outlives any single reconcile so a
		// subscription's consumer goroutine isn't torn down when Reconcile
		// returns, and it is cancelled when this replica loses leadership (or
		// the Manager stops) so a promoted standby doesn't double-consume.
		ctx context.Context
		// ready is closed once ctx is bound and the actor is serving, so a
		// reconcile that races ahead of Start blocks instead of subscribing
		// with a nil context.
		ready chan struct{}
	}

	triggerSubscription struct {
		trigger      fv1.MessageQueueTrigger
		subscription messageQueue.Subscription
	}

	request struct {
		requestType
		triggerSub *triggerSubscription
		respChan   chan response
	}
	response struct {
		err        error
		triggerSub *triggerSubscription
	}
)

// MakeMessageQueueTriggerManager creates the subscription manager. The
// leader-scoped context that bounds subscription lifetime is supplied later by
// Start; reconciles wait (waitReady) until that context is bound.
func MakeMessageQueueTriggerManager(logger logr.Logger,
	fissionClient versioned.Interface,
	mqType fv1.MessageQueueType,
	messageQueue messageQueue.MessageQueue) *MessageQueueTriggerManager {
	return &MessageQueueTriggerManager{
		logger:           logger.WithName("message_queue_trigger_manager"),
		reqChan:          make(chan request),
		triggers:         make(map[string]*triggerSubscription),
		fissionClient:    fissionClient,
		messageQueueType: mqType,
		messageQueue:     messageQueue,
		ready:            make(chan struct{}),
	}
}

// Start runs the subscription actor as a leader-only Runnable. It binds ctx as
// the subscription parent (so subscriptions are cancelled on leadership loss)
// and returns when ctx is cancelled.
func (mqt *MessageQueueTriggerManager) Start(ctx context.Context) error {
	mqt.logger.Info("starting message queue trigger manager")
	mqt.bind(ctx)
	<-ctx.Done()
	mqt.logger.Info("shutting down message queue trigger manager")
	return nil
}

// bind records the leader-scoped subscription context and starts the request
// actor, then signals readiness. Separated from Start so tests can drive it
// without Start's blocking wait.
func (mqt *MessageQueueTriggerManager) bind(ctx context.Context) {
	mqt.ctx = ctx
	go mqt.service()
	close(mqt.ready)
}

// waitReady blocks until bind has run (leader-scoped ctx set, actor serving) or
// the caller's ctx is cancelled. The reconciler calls this before driving any
// subscribe so it never calls Subscribe with a nil context.
func (mqt *MessageQueueTriggerManager) waitReady(ctx context.Context) error {
	select {
	case <-mqt.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (mqt *MessageQueueTriggerManager) service() {
	for {
		req := <-mqt.reqChan
		resp := response{triggerSub: nil, err: nil}
		k, err := k8sCache.MetaNamespaceKeyFunc(&req.triggerSub.trigger)
		if err != nil {
			resp.err = err
			req.respChan <- resp
			continue
		}

		switch req.requestType {
		case ADD_TRIGGER:
			if _, ok := mqt.triggers[k]; ok {
				resp.err = ErrTriggerAlreadyExists
			} else {
				mqt.triggers[k] = req.triggerSub
				mqt.logger.V(1).Info("set trigger subscription", "key", k)
				IncreaseSubscriptionCount()
			}
			req.respChan <- resp
		case UPDATE_TRIGGER_SUBSCRIPTION:
			if _, ok := mqt.triggers[k]; ok {
				mqt.triggers[k] = req.triggerSub
				mqt.logger.V(1).Info("updated trigger subscription", "key", k)
			} else {
				resp.err = ErrTriggerSubscriptionNotFound
			}
			req.respChan <- resp
		case GET_TRIGGER_SUBSCRIPTION:
			if _, ok := mqt.triggers[k]; !ok {
				resp.err = ErrTriggerNotFound
			} else {
				resp.triggerSub = mqt.triggers[k]
			}
			req.respChan <- resp
		case DELETE_TRIGGER:
			delete(mqt.triggers, k)
			mqt.logger.V(1).Info("delete trigger", "key", k)
			DecreaseSubscriptionCount()
			req.respChan <- resp
		}
	}
}

func (mqt *MessageQueueTriggerManager) makeRequest(requestType requestType, triggerSub *triggerSubscription) response {
	respChan := make(chan response)
	mqt.reqChan <- request{requestType, triggerSub, respChan}
	return <-respChan
}

func (mqt *MessageQueueTriggerManager) addTrigger(triggerSub *triggerSubscription) error {
	resp := mqt.makeRequest(ADD_TRIGGER, triggerSub)
	return resp.err
}

func (mqt *MessageQueueTriggerManager) getTriggerSubscription(trigger *fv1.MessageQueueTrigger) *triggerSubscription {
	resp := mqt.makeRequest(GET_TRIGGER_SUBSCRIPTION, &triggerSubscription{trigger: *trigger})
	return resp.triggerSub
}

func (mqt *MessageQueueTriggerManager) updateTriggerSubscription(triggerSub *triggerSubscription) error {
	resp := mqt.makeRequest(UPDATE_TRIGGER_SUBSCRIPTION, triggerSub)
	return resp.err
}

func (mqt *MessageQueueTriggerManager) checkTriggerSubscription(trigger *fv1.MessageQueueTrigger) bool {
	return mqt.getTriggerSubscription(trigger) != nil
}

func (mqt *MessageQueueTriggerManager) delTriggerSubscription(trigger *fv1.MessageQueueTrigger) error {
	resp := mqt.makeRequest(DELETE_TRIGGER, &triggerSubscription{trigger: *trigger})
	return resp.err
}

func (mqt *MessageQueueTriggerManager) updateTrigger(trigger *fv1.MessageQueueTrigger) error {
	oldTriggerSubscription := mqt.getTriggerSubscription(trigger)
	if oldTriggerSubscription == nil {
		mqt.logger.Info("Trigger subscrption does not exist", "trigger_name", trigger.Name)
		return ErrTriggerNotFound
	}

	// unsubscribe the messagequeue
	err := mqt.messageQueue.Unsubscribe(oldTriggerSubscription.subscription)
	if err != nil {
		mqt.logger.Error(err, "failed to unsubscribe from message queue trigger", "trigger_name", trigger.Name)
		return err
	}

	// subscribe using the updated message queue trigger
	sub, err := mqt.messageQueue.Subscribe(mqt.ctx, trigger)
	if err != nil {
		mqt.logger.Error(err, "failed to re-subscribe to message queue trigger", "trigger_name", trigger.Name)
		return err
	}
	if sub == nil {
		mqt.logger.Error(nil, "subscription is nil", "trigger_name", trigger.Name)
		return ErrSubscriptionNil
	}
	newTriggerSubscription := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}

	// update our list
	err = mqt.updateTriggerSubscription(&newTriggerSubscription)
	if err != nil {
		mqt.logger.Error(err, "updating message queue trigger failed", "trigger_name", trigger.Name)
		return err
	}
	mqt.logger.Info("message queue trigger updated", "trigger_name", trigger.Name)
	return nil
}

// Owns reports whether this head's manager consumes trigger: a classic
// (non-KEDA) trigger of this head's MESSAGE_QUEUE_TYPE. One classic head runs
// per MQ type (mqtrigger-kafka, mqtrigger-statestore, ...) and all of them
// watch the same CRD, so each must claim only its own type — and never a
// KEDA-kind trigger, which the --mqt_keda scaler manager owns.
func (mqt *MessageQueueTriggerManager) Owns(trigger *fv1.MessageQueueTrigger) bool {
	return trigger.Spec.MqtKind != MqtKindKeda &&
		trigger.Spec.MessageQueueType == mqt.messageQueueType
}

// RegisterTrigger subscribes to (or re-subscribes, on a spec change) the message
// queue for trigger and records the subscription. It is the create/update path
// the reconciler calls; the delete path is unsubscribe. Subscriptions use the
// manager-wide ctx so they survive the reconcile that created them.
func (mqt *MessageQueueTriggerManager) RegisterTrigger(trigger *fv1.MessageQueueTrigger) error {
	isPresent := mqt.checkTriggerSubscription(trigger)
	if isPresent {
		mqt.logger.V(1).Info("updating message queue trigger", "trigger_name", trigger.Name)
		err := mqt.updateTrigger(trigger)
		if err != nil {
			mqt.logger.Error(err, "error updating messagequeuetrigger")
			return err
		}
		return nil
	}

	// actually subscribe using the message queue client impl
	sub, err := mqt.messageQueue.Subscribe(mqt.ctx, trigger)
	if err != nil {
		mqt.logger.Error(err, "failed to subscribe to message queue trigger", "trigger_name", trigger.Name)
		return err
	}
	if sub == nil {
		mqt.logger.Error(nil, "subscription is nil", "trigger_name", trigger.Name)
		return ErrSubscriptionNil
	}
	triggerSub := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}
	// add to our list
	err = mqt.addTrigger(&triggerSub)
	if err != nil {
		mqt.logger.Error(err, "adding message queue trigger failed", "trigger_name", trigger.Name)
		// Roll back the subscription we just created so we don't leak a consumer
		// goroutine for a trigger we failed to record; the reconciler requeues.
		if unsubErr := mqt.messageQueue.Unsubscribe(sub); unsubErr != nil {
			mqt.logger.Error(unsubErr, "failed to roll back subscription after add failure", "trigger_name", trigger.Name)
		}
		return err
	}
	mqt.logger.Info("message queue trigger created", "trigger_name", trigger.Name)
	// Use the manager-wide ctx so dangling status writes cancel on
	// manager shutdown.
	mqt.markMessageQueueTriggerBound(mqt.ctx, trigger)
	return nil
}

// unsubscribe tears down the subscription for a deleted MessageQueueTrigger,
// identified by name (a delete reconcile only has the NamespacedName). It is a
// no-op if no subscription is recorded for the key.
func (mqt *MessageQueueTriggerManager) unsubscribe(key types.NamespacedName) error {
	stub := &fv1.MessageQueueTrigger{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	sub := mqt.getTriggerSubscription(stub)
	if sub == nil {
		return nil
	}
	if err := mqt.messageQueue.Unsubscribe(sub.subscription); err != nil {
		mqt.logger.Error(err, "failed to unsubscribe from message queue trigger", "trigger_name", key.Name)
		return err
	}
	if err := mqt.delTriggerSubscription(stub); err != nil {
		mqt.logger.Error(err, "error deleting message queue trigger subscription", "trigger_name", key.Name)
		return err
	}
	mqt.logger.Info("message queue trigger deleted", "trigger_name", key.Name)
	return nil
}

// markMessageQueueTriggerBound writes BindingReady + Ready on a
// MessageQueueTrigger after subscribe-and-add succeeds. Best-effort; queue
// subscription is the source of truth and is not gated on status writes.
// Fast-path skips the apiserver call when the trigger's in-memory
// conditions already match the desired state.
func (mqt *MessageQueueTriggerManager) markMessageQueueTriggerBound(ctx context.Context, trigger *fv1.MessageQueueTrigger) {
	if mqt.fissionClient == nil {
		return
	}
	wantBind := metav1.Condition{
		Type: fv1.MessageQueueTriggerConditionBindingReady, Status: metav1.ConditionTrue,
		ObservedGeneration: trigger.Generation,
		Reason:             fv1.MessageQueueTriggerReasonSubscribed,
		Message:            "subscribed to topic " + trigger.Spec.Topic,
	}
	wantReady := metav1.Condition{
		Type: fv1.MessageQueueTriggerConditionReady, Status: metav1.ConditionTrue,
		ObservedGeneration: trigger.Generation,
		Reason:             fv1.MessageQueueTriggerReasonSubscribed,
		Message:            "trigger is dispatching messages",
	}
	if conditions.IsAt(trigger.Status.Conditions, wantBind) && conditions.IsAt(trigger.Status.Conditions, wantReady) {
		return
	}
	cur, err := mqt.fissionClient.CoreV1().MessageQueueTriggers(trigger.Namespace).Get(ctx, trigger.Name, metav1.GetOptions{})
	if err != nil {
		mqt.logger.V(1).Info("mqtrigger status: get failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
		return
	}
	wantBind.ObservedGeneration = cur.Generation
	wantReady.ObservedGeneration = cur.Generation
	if conditions.IsAt(cur.Status.Conditions, wantBind) && conditions.IsAt(cur.Status.Conditions, wantReady) {
		return
	}
	conditions.Set(&cur.Status.Conditions, wantBind)
	conditions.Set(&cur.Status.Conditions, wantReady)
	if _, err := mqt.fissionClient.CoreV1().MessageQueueTriggers(trigger.Namespace).UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err != nil {
		mqt.logger.V(1).Info("mqtrigger status: update failed", "name", trigger.Name, "namespace", trigger.Namespace, "error", err)
	}
}
