/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mqtrigger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	flisterv1 "github.com/fission/fission/pkg/generated/listers/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/utils/manager"
	"github.com/fission/fission/pkg/utils/metrics"
)

const (
	ADD_TRIGGER requestType = iota
	DELETE_TRIGGER
	GET_TRIGGER_SUBSCRIPTION
	UPDATE_TRIGGER_SUBSCRIPTION
)

type (
	requestType int

	MessageQueueTriggerManager struct {
		logger           logr.Logger
		reqChan          chan request
		triggers         map[string]*triggerSubscription
		fissionClient    versioned.Interface
		messageQueueType fv1.MessageQueueType
		messageQueue     messageQueue.MessageQueue

		mqtLister       map[string]flisterv1.MessageQueueTriggerLister
		mqtListerSynced map[string]k8sCache.InformerSynced

		mqTriggerCreateUpdateQueue workqueue.TypedRateLimitingInterface[string]
		mqTriggerDeleteQueue       workqueue.TypedRateLimitingInterface[*fv1.MessageQueueTrigger]
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

func MakeMessageQueueTriggerManager(logger logr.Logger,
	fissionClient versioned.Interface,
	mqType fv1.MessageQueueType,
	finformerFactory map[string]genInformer.SharedInformerFactory,
	messageQueue messageQueue.MessageQueue) (*MessageQueueTriggerManager, error) {
	mqTriggerMgr := MessageQueueTriggerManager{
		logger:                     logger.WithName("message_queue_trigger_manager"),
		reqChan:                    make(chan request),
		triggers:                   make(map[string]*triggerSubscription),
		fissionClient:              fissionClient,
		mqtLister:                  make(map[string]flisterv1.MessageQueueTriggerLister, 0),
		mqtListerSynced:            make(map[string]k8sCache.InformerSynced, 0),
		messageQueueType:           mqType,
		messageQueue:               messageQueue,
		mqTriggerCreateUpdateQueue: workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[string](), workqueue.TypedRateLimitingQueueConfig[string]{Name: "MqtAddUpdateQueue"}),
		mqTriggerDeleteQueue:       workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.DefaultTypedControllerRateLimiter[*fv1.MessageQueueTrigger](), workqueue.TypedRateLimitingQueueConfig[*fv1.MessageQueueTrigger]{Name: "MqtDeleteQueue"}),
	}

	for ns, informer := range finformerFactory {
		_, err := informer.Core().V1().MessageQueueTriggers().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc:    mqTriggerMgr.enqueueMqtAdd,
			UpdateFunc: mqTriggerMgr.enqueueMqtUpdate,
			DeleteFunc: mqTriggerMgr.enqueueMqtDelete,
		})
		if err != nil {
			return nil, err
		}
		mqTriggerMgr.mqtLister[ns] = informer.Core().V1().MessageQueueTriggers().Lister()
		mqTriggerMgr.mqtListerSynced[ns] = informer.Core().V1().MessageQueueTriggers().Informer().HasSynced
	}

	return &mqTriggerMgr, nil
}

func (mqt *MessageQueueTriggerManager) Run(ctx context.Context, stopCh <-chan struct{}, mgr manager.Interface) {
	defer utilruntime.HandleCrash()
	defer mqt.mqTriggerCreateUpdateQueue.ShutDown()
	defer mqt.mqTriggerDeleteQueue.ShutDown()
	go mqt.service()

	mqt.logger.Info("Waiting for informer caches to sync")

	waitSynced := make([]k8sCache.InformerSynced, 0)
	for _, synced := range mqt.mqtListerSynced {
		waitSynced = append(waitSynced, synced)
	}
	if ok := k8sCache.WaitForCacheSync(stopCh, waitSynced...); !ok {
		mqt.logger.Info("failed to wait for caches to sync")
		os.Exit(1)
	}

	for range 4 {
		mgr.Add(ctx, func(ctx context.Context) {
			wait.Until(mqt.workerRun(ctx, "mqTriggerCreateUpdate", mqt.mqTriggerCreateUpdateQueueProcessFunc), time.Second, stopCh)
		})
	}
	mgr.Add(ctx, func(ctx context.Context) {
		wait.Until(mqt.workerRun(ctx, "mqTriggerDeleteQueue", mqt.mqTriggerDeleteQueueProcessFunc), time.Second, stopCh)
	})

	mgr.Add(ctx, func(ctx context.Context) {
		metrics.ServeMetrics(ctx, "mqtrigger", mqt.logger, mgr)
	})

	<-stopCh
	mqt.logger.Info("Shutting down workers for messageQueueTriggerManager")
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
				resp.err = errors.New("trigger already exists")
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
				resp.err = errors.New("trigger subscription does not exists")
			}
			req.respChan <- resp
		case GET_TRIGGER_SUBSCRIPTION:
			if _, ok := mqt.triggers[k]; !ok {
				resp.err = errors.New("trigger does not exist")
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
		return errors.New("trigger does not exist")
	}

	// unsubscribe the messagequeue
	err := mqt.messageQueue.Unsubscribe(oldTriggerSubscription.subscription)
	if err != nil {
		mqt.logger.Error(err, "failed to unsubscribe from message queue trigger", "trigger_name", trigger.Name)
		return err
	}

	// subscribe using the updated message queue trigger
	sub, err := mqt.messageQueue.Subscribe(trigger)
	if err != nil {
		mqt.logger.Error(err, "failed to re-subscribe to message queue trigger", "trigger_name", trigger.Name)
		return err
	}
	if sub == nil {
		mqt.logger.Error(nil, "subscription is nil", "trigger_name", trigger.Name)
		return nil
	}
	newTriggerSubscription := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}

	// update our list
	err = mqt.updateTriggerSubscription(&newTriggerSubscription)
	if err != nil {
		mqt.logger.Error(err, "updating message queue trigger failed", "trigger_name", trigger.Name)
		os.Exit(1)
		return err
	}
	mqt.logger.Info("message queue trigger updated", "trigger_name", trigger.Name)
	return nil
}

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
	sub, err := mqt.messageQueue.Subscribe(trigger)
	if err != nil {
		mqt.logger.Error(err, "failed to subscribe to message queue trigger", "trigger_name", trigger.Name)
		return err
	}
	if sub == nil {
		mqt.logger.Error(nil, "subscription is nil", "trigger_name", trigger.Name)
		return nil
	}
	triggerSub := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}
	// add to our list
	err = mqt.addTrigger(&triggerSub)
	if err != nil {
		mqt.logger.Error(err, "adding message queue trigger failed", "trigger_name", trigger.Name)
		os.Exit(1)
		return err
	}
	mqt.logger.Info("message queue trigger created", "trigger_name", trigger.Name)
	return nil
}

func (mqt *MessageQueueTriggerManager) enqueueMqtAdd(obj any) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		mqt.logger.Error(nil, "error retrieving key from object in messageQueueTriggerManager", "obj", obj)
		return
	}
	mqt.logger.V(1).Info("enqueue mqt add", "key", key)
	mqt.mqTriggerCreateUpdateQueue.Add(key)
}

func (mqt *MessageQueueTriggerManager) enqueueMqtUpdate(oldObj, newObj any) {
	key, err := k8sCache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		mqt.logger.Error(err, "error retrieving key from object in messageQueueTriggerManager", "obj", newObj)
		return
	}
	mqt.logger.V(1).Info("enqueue mqt update", "key", key)
	mqt.mqTriggerCreateUpdateQueue.Add(key)
}

func (mqt *MessageQueueTriggerManager) enqueueMqtDelete(obj any) {
	mqTrigger, ok := obj.(*fv1.MessageQueueTrigger)
	if !ok {
		mqt.logger.Error(nil, "unexpected type when deleting mqt to messageQueueTriggerManager", "obj", obj)
		return
	}
	mqt.logger.V(1).Info("enqueue mqt delete", "mqTrigger", mqTrigger)
	mqt.mqTriggerDeleteQueue.Add(mqTrigger)
}

func (mqt *MessageQueueTriggerManager) workerRun(ctx context.Context, name string, processFunc func(ctx context.Context) bool) func() {
	return func() {
		mqt.logger.V(1).Info("Starting worker with func", "name", name)
		for {
			if quit := processFunc(ctx); quit {
				mqt.logger.Info("Shutting down worker", "name", name)
				return
			}
		}
	}
}

func (mqt *MessageQueueTriggerManager) getMqtLister(namespace string) (flisterv1.MessageQueueTriggerLister, error) {
	lister, ok := mqt.mqtLister[namespace]
	if ok {
		return lister, nil
	}
	for ns, lister := range mqt.mqtLister {
		if ns == namespace {
			return lister, nil
		}
	}
	mqt.logger.Error(nil, "no messagequeuetrigger lister found for namespace", "namespace", namespace)
	return nil, fmt.Errorf("no messagequeuetrigger lister found for namespace %s", namespace)
}

func (mqt *MessageQueueTriggerManager) mqTriggerCreateUpdateQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	key, quit := mqt.mqTriggerCreateUpdateQueue.Get()
	if quit {
		return false
	}
	defer mqt.mqTriggerCreateUpdateQueue.Done(key)

	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		mqt.logger.Error(err, "error splitting key")
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}
	mqTriggerLister, err := mqt.getMqtLister(namespace)
	if err != nil {
		mqt.logger.Error(err, "error getting messagequeuetrigger lister")
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}
	mqTrigger, err := mqTriggerLister.MessageQueueTriggers(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		mqt.logger.Info("mqt not found", "key", key)
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}

	if err != nil {
		if mqt.mqTriggerCreateUpdateQueue.NumRequeues(key) < maxRetries {
			mqt.mqTriggerCreateUpdateQueue.AddRateLimited(key)
			mqt.logger.Error(err, "error getting mqt, retrying")
		} else {
			mqt.mqTriggerCreateUpdateQueue.Forget(key)
			mqt.logger.Error(err, "error getting mqt, max retries reached")
		}
		return false
	}

	mqt.logger.V(1).Info("Added mqt", "trigger: ", mqTrigger.ObjectMeta)
	err = mqt.RegisterTrigger(mqTrigger)
	if err != nil {
		if mqt.mqTriggerCreateUpdateQueue.NumRequeues(key) < maxRetries {
			mqt.mqTriggerCreateUpdateQueue.AddRateLimited(key)
			mqt.logger.Error(err, "error handling mqt from mqtInformer, retrying", "key", key)
		} else {
			mqt.mqTriggerCreateUpdateQueue.Forget(key)
			mqt.logger.Error(err, "error handling mqt from mqtInformer, max retries reached", "key", key)
		}
		return false
	}
	mqt.mqTriggerCreateUpdateQueue.Forget(key)
	return false
}

func (mqt *MessageQueueTriggerManager) mqTriggerDeleteQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	mqTrigger, quit := mqt.mqTriggerDeleteQueue.Get()
	if quit {
		return false
	}
	defer mqt.mqTriggerDeleteQueue.Done(mqTrigger)

	mqt.logger.V(1).Info("Delete mqt", "trigger: ", mqTrigger.ObjectMeta)
	triggerSubscription := mqt.getTriggerSubscription(mqTrigger)
	if triggerSubscription == nil {
		mqt.logger.Info("Unsubscribe failed", "trigger_name", mqTrigger.Name)
		mqt.mqTriggerDeleteQueue.Forget(mqTrigger)
		return false
	}

	err := mqt.messageQueue.Unsubscribe(triggerSubscription.subscription)
	if err != nil {
		if mqt.mqTriggerDeleteQueue.NumRequeues(mqTrigger) < maxRetries {
			mqt.mqTriggerDeleteQueue.AddRateLimited(mqTrigger)
			mqt.logger.Error(err, "failed to unsubscribe from message queue trigger, retrying", "trigger_name", mqTrigger.Name)
		} else {
			mqt.mqTriggerDeleteQueue.Forget(mqTrigger)
			mqt.logger.Error(err, "failed to unsubscribe from message queue trigger, max retries reached")
		}
		return false
	}

	err = mqt.delTriggerSubscription(mqTrigger)
	if err != nil {
		if mqt.mqTriggerDeleteQueue.NumRequeues(mqTrigger) < maxRetries {
			mqt.mqTriggerDeleteQueue.AddRateLimited(mqTrigger)
			mqt.logger.Error(err, "error deleting mqt, retrying", "obj", mqTrigger)
		} else {
			mqt.mqTriggerDeleteQueue.Forget(mqTrigger)
			mqt.logger.Error(err, "deleting message queue trigger failed, max retries reached", "trigger_name", mqTrigger.Name)
		}
		return false
	}

	mqt.mqTriggerDeleteQueue.Forget(mqTrigger)
	mqt.logger.Info("message queue trigger deleted", "trigger_name", mqTrigger.Name)
	return false
}
