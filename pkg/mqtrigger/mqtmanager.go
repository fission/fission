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
	"time"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

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
)

type (
	requestType int

	MessageQueueTriggerManager struct {
		logger           *zap.Logger
		reqChan          chan request
		triggers         map[string]*triggerSubscription
		fissionClient    versioned.Interface
		messageQueueType fv1.MessageQueueType
		messageQueue     messageQueue.MessageQueue

		mqtLister       map[string]flisterv1.MessageQueueTriggerLister
		mqtListerSynced map[string]k8sCache.InformerSynced

		mqTriggerCreateUpdateQueue workqueue.RateLimitingInterface
		mqTriggerDeleteQueue       workqueue.RateLimitingInterface
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

func MakeMessageQueueTriggerManager(logger *zap.Logger,
	fissionClient versioned.Interface,
	mqType fv1.MessageQueueType,
	finformerFactory map[string]genInformer.SharedInformerFactory,
	messageQueue messageQueue.MessageQueue) (*MessageQueueTriggerManager, error) {
	mqTriggerMgr := MessageQueueTriggerManager{
		logger:                     logger.Named("message_queue_trigger_manager"),
		reqChan:                    make(chan request),
		triggers:                   make(map[string]*triggerSubscription),
		fissionClient:              fissionClient,
		mqtLister:                  make(map[string]flisterv1.MessageQueueTriggerLister, 0),
		mqtListerSynced:            make(map[string]k8sCache.InformerSynced, 0),
		messageQueueType:           mqType,
		messageQueue:               messageQueue,
		mqTriggerCreateUpdateQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "MqtAddUpdateQueue"),
		mqTriggerDeleteQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "MqtDeleteQueue"),
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
		mqt.logger.Fatal("failed to wait for caches to sync")
	}

	for i := 0; i < 4; i++ {
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
				mqt.logger.Debug("set trigger subscription", zap.String("key", k))
				IncreaseSubscriptionCount()
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
			mqt.logger.Debug("delete trigger", zap.String("key", k))
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

func (mqt *MessageQueueTriggerManager) checkTriggerSubscription(trigger *fv1.MessageQueueTrigger) bool {
	return mqt.getTriggerSubscription(trigger) != nil
}

func (mqt *MessageQueueTriggerManager) delTriggerSubscription(trigger *fv1.MessageQueueTrigger) error {
	resp := mqt.makeRequest(DELETE_TRIGGER, &triggerSubscription{trigger: *trigger})
	return resp.err
}

func (mqt *MessageQueueTriggerManager) RegisterTrigger(trigger *fv1.MessageQueueTrigger) error {
	isPresent := mqt.checkTriggerSubscription(trigger)
	if isPresent {
		mqt.logger.Debug("message queue trigger already registered", zap.String("trigger_name", trigger.ObjectMeta.Name))
		return nil
	}

	// actually subscribe using the message queue client impl
	sub, err := mqt.messageQueue.Subscribe(trigger)
	if err != nil {
		mqt.logger.Warn("failed to subscribe to message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
		return err
	}
	if sub == nil {
		mqt.logger.Warn("subscription is nil", zap.String("trigger_name", trigger.ObjectMeta.Name))
		return nil
	}
	triggerSub := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}
	// add to our list
	err = mqt.addTrigger(&triggerSub)
	if err != nil {
		mqt.logger.Fatal("adding message queue trigger failed", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
		return err
	}
	mqt.logger.Info("message queue trigger created", zap.String("trigger_name", trigger.ObjectMeta.Name))
	return nil
}

func (mqt *MessageQueueTriggerManager) enqueueMqtAdd(obj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		mqt.logger.Error("error retrieving key from object in messageQueueTriggerManager", zap.Any("obj", obj))
		return
	}
	mqt.logger.Debug("enqueue mqt add", zap.String("key", key))
	mqt.mqTriggerCreateUpdateQueue.Add(key)
}

func (mqt *MessageQueueTriggerManager) enqueueMqtUpdate(oldObj, newObj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		mqt.logger.Error("error retrieving key from object in messageQueueTriggerManager", zap.Any("obj", key))
		return
	}
	mqt.logger.Debug("enqueue mqt update", zap.String("key", key))
	mqt.mqTriggerCreateUpdateQueue.Add(key)
}

func (mqt *MessageQueueTriggerManager) enqueueMqtDelete(obj interface{}) {
	mqTrigger, ok := obj.(*fv1.MessageQueueTrigger)
	if !ok {
		mqt.logger.Error("unexpected type when deleting mqt to messageQueueTriggerManager", zap.Any("obj", obj))
		return
	}
	mqt.logger.Debug("enqueue mqt delete", zap.Any("mqTrigger", mqTrigger))
	mqt.mqTriggerDeleteQueue.Add(mqTrigger)
}

func (mqt *MessageQueueTriggerManager) workerRun(ctx context.Context, name string, processFunc func(ctx context.Context) bool) func() {
	return func() {
		mqt.logger.Debug("Starting worker with func", zap.String("name", name))
		for {
			if quit := processFunc(ctx); quit {
				mqt.logger.Info("Shutting down worker", zap.String("name", name))
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
	mqt.logger.Error("no messagequeuetrigger lister found for namespace", zap.String("namespace", namespace))
	return nil, fmt.Errorf("no messagequeuetrigger lister found for namespace %s", namespace)
}

func (mqt *MessageQueueTriggerManager) mqTriggerCreateUpdateQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	obj, quit := mqt.mqTriggerCreateUpdateQueue.Get()
	if quit {
		return false
	}
	key := obj.(string)
	defer mqt.mqTriggerCreateUpdateQueue.Done(key)

	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		mqt.logger.Error("error splitting key", zap.Error(err))
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}
	mqTriggerLister, err := mqt.getMqtLister(namespace)
	if err != nil {
		mqt.logger.Error("error getting messagequeuetrigger lister", zap.Error(err))
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}
	mqTrigger, err := mqTriggerLister.MessageQueueTriggers(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		mqt.logger.Info("mqt not found", zap.String("key", key))
		mqt.mqTriggerCreateUpdateQueue.Forget(key)
		return false
	}

	if err != nil {
		if mqt.mqTriggerCreateUpdateQueue.NumRequeues(key) < maxRetries {
			mqt.mqTriggerCreateUpdateQueue.AddRateLimited(key)
			mqt.logger.Error("error getting mqt, retrying", zap.Error(err))
		} else {
			mqt.mqTriggerCreateUpdateQueue.Forget(key)
			mqt.logger.Error("error getting mqt, max retries reached", zap.Error(err))
		}
		return false
	}

	mqt.logger.Debug("Added mqt", zap.Any("trigger: ", mqTrigger.ObjectMeta))
	err = mqt.RegisterTrigger(mqTrigger)
	if err != nil {
		if mqt.mqTriggerCreateUpdateQueue.NumRequeues(key) < maxRetries {
			mqt.mqTriggerCreateUpdateQueue.AddRateLimited(key)
			mqt.logger.Error("error handling mqt from mqtInformer, retrying", zap.String("key", key), zap.Error(err))
		} else {
			mqt.mqTriggerCreateUpdateQueue.Forget(key)
			mqt.logger.Error("error handling mqt from mqtInformer, max retries reached", zap.String("key", key), zap.Error(err))
		}
		return false
	}
	mqt.mqTriggerCreateUpdateQueue.Forget(key)
	return false
}

func (mqt *MessageQueueTriggerManager) mqTriggerDeleteQueueProcessFunc(ctx context.Context) bool {
	maxRetries := 3
	obj, quit := mqt.mqTriggerDeleteQueue.Get()
	if quit {
		return false
	}
	defer mqt.mqTriggerDeleteQueue.Done(obj)
	mqTrigger, ok := obj.(*fv1.MessageQueueTrigger)
	if !ok {
		mqt.logger.Error("unexpected type when deleting mqt to message queue trigger manager", zap.Any("obj", obj))
		mqt.mqTriggerDeleteQueue.Forget(obj)
		return false
	}

	mqt.logger.Debug("Delete mqt", zap.Any("trigger: ", mqTrigger.ObjectMeta))
	triggerSubscription := mqt.getTriggerSubscription(mqTrigger)
	if triggerSubscription == nil {
		mqt.logger.Info("Unsubscribe failed", zap.String("trigger_name", mqTrigger.ObjectMeta.Name))
		mqt.mqTriggerDeleteQueue.Forget(obj)
		return false
	}

	err := mqt.messageQueue.Unsubscribe(triggerSubscription.subscription)
	if err != nil {
		if mqt.mqTriggerDeleteQueue.NumRequeues(obj) < maxRetries {
			mqt.mqTriggerDeleteQueue.AddRateLimited(obj)
			mqt.logger.Error("failed to unsubscribe from message queue trigger, retrying", zap.Error(err), zap.String("trigger_name", mqTrigger.ObjectMeta.Name))
		} else {
			mqt.mqTriggerDeleteQueue.Forget(obj)
			mqt.logger.Error("failed to unsubscribe from message queue trigger, max retries reached", zap.Error(err))
		}
		return false
	}

	err = mqt.delTriggerSubscription(mqTrigger)
	if err != nil {
		if mqt.mqTriggerDeleteQueue.NumRequeues(obj) < maxRetries {
			mqt.mqTriggerDeleteQueue.AddRateLimited(obj)
			mqt.logger.Error("error deleting mqt, retrying", zap.Any("obj", obj), zap.Error(err))
		} else {
			mqt.mqTriggerDeleteQueue.Forget(obj)
			mqt.logger.Error("deleting message queue trigger failed, max retries reached", zap.Error(err), zap.String("trigger_name", mqTrigger.ObjectMeta.Name))
		}
		return false
	}

	mqt.mqTriggerDeleteQueue.Forget(obj)
	mqt.logger.Info("message queue trigger deleted", zap.String("trigger_name", mqTrigger.ObjectMeta.Name))
	return false
}
