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
	"time"

	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
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
	fissionClient versioned.Interface, mqType fv1.MessageQueueType, messageQueue messageQueue.MessageQueue) *MessageQueueTriggerManager {
	mqTriggerMgr := MessageQueueTriggerManager{
		logger:           logger.Named("message_queue_trigger_manager"),
		reqChan:          make(chan request),
		triggers:         make(map[string]*triggerSubscription),
		fissionClient:    fissionClient,
		messageQueueType: mqType,
		messageQueue:     messageQueue,
	}
	return &mqTriggerMgr
}

func (mqt *MessageQueueTriggerManager) Run(ctx context.Context) {
	go mqt.service()
	informerFactory := genInformer.NewSharedInformerFactory(mqt.fissionClient, time.Minute*30)
	mqTriggerInformer := informerFactory.Core().V1().MessageQueueTriggers().Informer()
	mqTriggerInformer.AddEventHandler(mqt.mqtInformerHandlers())
	go mqTriggerInformer.Run(ctx.Done())
	if ok := k8sCache.WaitForCacheSync(ctx.Done(), mqTriggerInformer.HasSynced); !ok {
		mqt.logger.Fatal("failed to wait for caches to sync")
	}
	go metrics.ServeMetrics(ctx, mqt.logger)
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

func (mqt *MessageQueueTriggerManager) RegisterTrigger(trigger *fv1.MessageQueueTrigger) {
	isPresent := mqt.checkTriggerSubscription(trigger)
	if isPresent {
		mqt.logger.Info("message queue trigger already registered", zap.String("trigger_name", trigger.ObjectMeta.Name))
		return
	}

	// actually subscribe using the message queue client impl
	sub, err := mqt.messageQueue.Subscribe(trigger)
	if err != nil {
		mqt.logger.Warn("failed to subscribe to message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
		return
	}
	if sub == nil {
		mqt.logger.Warn("subscription is nil", zap.String("trigger_name", trigger.ObjectMeta.Name))
		return
	}
	triggerSub := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}
	// add to our list
	err = mqt.addTrigger(&triggerSub)
	if err != nil {
		mqt.logger.Fatal("adding message queue trigger failed", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
	}
	mqt.logger.Info("message queue trigger created", zap.String("trigger_name", trigger.ObjectMeta.Name))
}

func (mqt *MessageQueueTriggerManager) mqtInformerHandlers() k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			trigger := obj.(*fv1.MessageQueueTrigger)
			mqt.logger.Debug("Added mqt", zap.Any("trigger: ", trigger.ObjectMeta))
			mqt.RegisterTrigger(trigger)
		},
		DeleteFunc: func(obj interface{}) {
			trigger := obj.(*fv1.MessageQueueTrigger)
			mqt.logger.Debug("Delete mqt", zap.Any("trigger: ", trigger.ObjectMeta))
			triggerSubscription := mqt.getTriggerSubscription(trigger)
			if triggerSubscription == nil {
				mqt.logger.Info("Unsubscribe failed", zap.String("trigger_name", trigger.ObjectMeta.Name))
				return
			}

			err := mqt.messageQueue.Unsubscribe(triggerSubscription.subscription)
			if err != nil {
				mqt.logger.Warn("failed to unsubscribe from message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
				return
			}
			err = mqt.delTriggerSubscription(trigger)
			if err != nil {
				mqt.logger.Warn("deleting message queue trigger failed", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
			}
			mqt.logger.Info("message queue trigger deleted", zap.String("trigger_name", trigger.ObjectMeta.Name))
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			trigger := newObj.(*fv1.MessageQueueTrigger)
			mqt.logger.Debug("Updated mqt", zap.Any("trigger: ", trigger.ObjectMeta))
			mqt.RegisterTrigger(trigger)
		},
	}
}
