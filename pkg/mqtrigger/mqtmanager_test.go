// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	updatedTopicName = "new-topic"
)

// fakeSubscription implements messageQueue.Subscription for testing.
type fakeSubscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newFakeSubscription(ctx context.Context) *fakeSubscription {
	subCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		<-subCtx.Done()
		close(done)
	}()
	return &fakeSubscription{
		ctx:    subCtx,
		cancel: cancel,
		done:   done,
	}
}

func (s *fakeSubscription) Stop() error {
	s.cancel()
	return nil
}

func (s *fakeSubscription) Done() <-chan struct{} {
	return s.done
}

type fakeMessageQueue struct{}

func (f fakeMessageQueue) Subscribe(ctx context.Context, trigger *fv1.MessageQueueTrigger) (messageQueue.Subscription, error) {
	return newFakeSubscription(ctx), nil
}

func (f fakeMessageQueue) Unsubscribe(sub messageQueue.Subscription) error {
	return sub.Stop()
}

func TestMqtManager(t *testing.T) {
	logger := loggerfactory.GetLogger()
	msgQueue := fakeMessageQueue{}
	ctx := t.Context()
	mgr := MakeMessageQueueTriggerManager(logger, nil, fv1.MessageQueueTypeKafka, msgQueue)
	mgr.bind(ctx)

	trigger := fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
	}
	if mgr.checkTriggerSubscription(&trigger) {
		t.Errorf("checkTrigger should return false")
	}
	sub, err := msgQueue.Subscribe(ctx, &trigger)
	if err != nil {
		t.Errorf("Subscribe should not return error")
	}
	triggerSub := triggerSubscription{
		trigger:      trigger,
		subscription: sub,
	}
	err = mgr.addTrigger(&triggerSub)
	if err != nil {
		t.Errorf("addTrigger should not return error")
	}
	if !mgr.checkTriggerSubscription(&trigger) {
		t.Errorf("checkTrigger should return true")
	}
	getSub := mgr.getTriggerSubscription(&trigger)
	if getSub == nil {
		t.Fatal("getTriggerSubscription should return triggerSub")
		return
	}
	if getSub.trigger.Name != trigger.Name {
		t.Errorf("getTriggerSubscription should return triggerSub with trigger name %s", trigger.Name)
	}
	// Stop the subscription properly
	_ = getSub.subscription.Stop()
	trigger.Spec.Topic = updatedTopicName
	newSub, err := msgQueue.Subscribe(ctx, &trigger)
	if err != nil {
		t.Errorf("Subscribe should not return error")
	}
	newTriggerSub := triggerSubscription{
		trigger:      trigger,
		subscription: newSub,
	}
	err = mgr.updateTriggerSubscription(&newTriggerSub)
	if err != nil {
		t.Errorf("updateTriggerSubscription should not return error")
	}
	if !mgr.checkTriggerSubscription(&trigger) {
		t.Errorf("checkTrigger should return true")
	}
	getNewSub := mgr.getTriggerSubscription(&trigger)
	if getNewSub == nil {
		t.Fatal("getTriggerSubscription should return triggerSub")
		return
	}
	if getNewSub.trigger.Spec.Topic != updatedTopicName {
		t.Errorf("getTriggerSubscription returns trigger with incorrect topic-name, expected %s got %s", updatedTopicName, getNewSub.trigger.Spec.Topic)
	}
	// Stop the subscription properly
	_ = getNewSub.subscription.Stop()
	err = mgr.delTriggerSubscription(&trigger)
	if err != nil {
		t.Errorf("delTriggerSubscription should not return error")
	}
	if mgr.checkTriggerSubscription(&trigger) {
		t.Errorf("checkTrigger should return false")
	}
}

func TestMessageQueueTriggerReconciler(t *testing.T) {
	logger := loggerfactory.GetLogger()
	ctx := t.Context()
	mqt := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "mqt1", Namespace: metav1.NamespaceDefault, Generation: 1},
		Spec:       fv1.MessageQueueTriggerSpec{Topic: "topic-a", MessageQueueType: fv1.MessageQueueTypeKafka},
	}
	c := crfake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(mqt).
		WithStatusSubresource(&fv1.MessageQueueTrigger{}).
		Build()

	// nil fissionClient → markMessageQueueTriggerBound is a no-op, keeping the
	// test focused on subscription state.
	mgr := MakeMessageQueueTriggerManager(logger, nil, fv1.MessageQueueTypeKafka, fakeMessageQueue{})
	mgr.bind(ctx)

	r := NewMessageQueueTriggerReconciler(logger, c, mgr)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "mqt1"}}

	// Create: subscribes and records the trigger.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.True(t, mgr.checkTriggerSubscription(mqt), "subscription should exist after reconcile")

	// Reconcile again is idempotent (update path; still subscribed).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.True(t, mgr.checkTriggerSubscription(mqt))

	// Delete: a NotFound get tears the subscription down.
	require.NoError(t, c.Delete(ctx, mqt))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.False(t, mgr.checkTriggerSubscription(mqt), "subscription should be gone after delete")

	// Unsubscribing an unknown trigger is a no-op, not an error.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
}

// TestReconcilerOwnership: every classic head watches the same CRD, so each
// must subscribe only its own MQ type and never a KEDA-kind trigger — and a
// spec update that moves a trigger away from this head must tear its
// subscription down.
func TestReconcilerOwnership(t *testing.T) {
	logger := loggerfactory.GetLogger()
	ctx := t.Context()
	kafkaMqt := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka-mqt", Namespace: metav1.NamespaceDefault, Generation: 1},
		Spec:       fv1.MessageQueueTriggerSpec{Topic: "topic-a", MessageQueueType: fv1.MessageQueueTypeKafka},
	}
	otherType := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "ss-mqt", Namespace: metav1.NamespaceDefault, Generation: 1},
		Spec:       fv1.MessageQueueTriggerSpec{Topic: "topic-b", MessageQueueType: fv1.MessageQueueTypeStatestore},
	}
	kedaKind := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "keda-mqt", Namespace: metav1.NamespaceDefault, Generation: 1},
		Spec:       fv1.MessageQueueTriggerSpec{Topic: "topic-c", MessageQueueType: fv1.MessageQueueTypeKafka, MqtKind: MqtKindKeda},
	}
	c := crfake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(kafkaMqt, otherType, kedaKind).
		WithStatusSubresource(&fv1.MessageQueueTrigger{}).
		Build()

	mgr := MakeMessageQueueTriggerManager(logger, nil, fv1.MessageQueueTypeKafka, fakeMessageQueue{})
	mgr.bind(ctx)
	r := NewMessageQueueTriggerReconciler(logger, c, mgr)

	reconcile := func(name string) {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: name}})
		require.NoError(t, err)
	}

	reconcile("kafka-mqt")
	reconcile("ss-mqt")
	reconcile("keda-mqt")
	assert.True(t, mgr.checkTriggerSubscription(kafkaMqt), "own type: subscribed")
	assert.False(t, mgr.checkTriggerSubscription(otherType), "another head's MQ type: not subscribed")
	assert.False(t, mgr.checkTriggerSubscription(kedaKind), "KEDA-kind: the scaler manager owns it")

	// A spec update that changes the MQ type moves the trigger to another head:
	// this head must drop its subscription.
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "kafka-mqt"}, kafkaMqt))
	kafkaMqt.Spec.MessageQueueType = fv1.MessageQueueTypeStatestore
	kafkaMqt.Generation = 2
	require.NoError(t, c.Update(ctx, kafkaMqt))
	reconcile("kafka-mqt")
	assert.False(t, mgr.checkTriggerSubscription(kafkaMqt), "type change away from this head unsubscribes")
}
