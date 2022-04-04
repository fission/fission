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
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	genInformer "github.com/fission/fission/pkg/generated/informers/externalversions"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
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
		fissionClient    *crd.FissionClient
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
	fissionClient *crd.FissionClient, mqType fv1.MessageQueueType, messageQueue messageQueue.MessageQueue) *MessageQueueTriggerManager {
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
	go mqt.serveMetrics()
}

func (mqt *MessageQueueTriggerManager) service() {
	for {
		req := <-mqt.reqChan
		switch req.requestType {
		case ADD_TRIGGER:
			var err error
			k := crd.CacheKey(&req.triggerSub.trigger.ObjectMeta)
			if _, ok := mqt.triggers[k]; ok {
				err = errors.New("trigger already exists")
			} else {
				mqt.triggers[k] = req.triggerSub
			}
			req.respChan <- response{err: err}
		case GET_TRIGGER_SUBSCRIPTION:
			resp := response{triggerSub: nil, err: nil}
			k := crd.CacheKey(&req.triggerSub.trigger.ObjectMeta)
			if _, ok := mqt.triggers[k]; !ok {
				resp.err = errors.New("trigger does not exist")
			} else {
				resp.triggerSub = mqt.triggers[k]
			}
			mqt.logger.Info("Checking for trigger subsription before sending")
			req.respChan <- resp
		case DELETE_TRIGGER:
			delete(mqt.triggers, crd.CacheKey(&req.triggerSub.trigger.ObjectMeta))
		}
	}
}

func (mqt *MessageQueueTriggerManager) serveMetrics() {
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(metricsAddr, nil)

	mqt.logger.Fatal("done listening on metrics endpoint", zap.Error(err))
}

func (mqt *MessageQueueTriggerManager) addTrigger(triggerSub *triggerSubscription) error {
	respChan := make(chan response)
	mqt.reqChan <- request{
		requestType: ADD_TRIGGER,
		triggerSub:  triggerSub,
		respChan:    respChan,
	}
	mqt.logger.Info("respchan struct: ", zap.Any("sub: ", triggerSub))
	r := <-respChan
	return r.err
}

func (mqt *MessageQueueTriggerManager) getTriggerSubscription(m *metav1.ObjectMeta) *triggerSubscription {
	respChan := make(chan response)
	mqt.reqChan <- request{
		requestType: GET_TRIGGER_SUBSCRIPTION,
		triggerSub: &triggerSubscription{
			trigger: fv1.MessageQueueTrigger{
				ObjectMeta: *m,
			},
		},
		respChan: respChan,
	}
	r := <-respChan
	if r.err != nil {
		mqt.logger.Error(r.err.Error())
	}
	return r.triggerSub
}

func (mqt *MessageQueueTriggerManager) checkTrigger(m *metav1.ObjectMeta) bool {
	return mqt.getTriggerSubscription(m) != nil
}

func (mqt *MessageQueueTriggerManager) delTrigger(m *metav1.ObjectMeta) {
	mqt.reqChan <- request{
		requestType: DELETE_TRIGGER,
		triggerSub: &triggerSubscription{
			trigger: fv1.MessageQueueTrigger{
				ObjectMeta: *m,
			},
		},
	}
}

func (mqt *MessageQueueTriggerManager) RegisterTrigger(trigger *fv1.MessageQueueTrigger) {
	mqt.logger.Info("Inside register trigger")
	isPresent := mqt.checkTrigger(&trigger.ObjectMeta)
	if isPresent {
		mqt.logger.Info("message queue trigger already registered", zap.String("trigger_name", trigger.ObjectMeta.Name))
		return
	}

	// actually subscribe using the message queue client impl
	mqt.logger.Info("Trigger", zap.Any("kafka: ", trigger.ObjectMeta))
	sub, err := mqt.messageQueue.Subscribe(trigger)
	if err != nil {
		mqt.logger.Warn("failed to subscribe to message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
		return
	}
	triggerSub := triggerSubscription{
		trigger:      *trigger,
		subscription: sub,
	}
	mqt.logger.Info("Subscription successful", zap.Any("Sub: ", sub))
	// add to our list
	err = mqt.addTrigger(&triggerSub)
	if err != nil {
		mqt.logger.Fatal("adding message queue trigger failed", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
	}
	mqt.logger.Info("message queue trigger created", zap.String("trigger_name", trigger.ObjectMeta.Name))
}

func (mqt *MessageQueueTriggerManager) mqtInformerHandlers() k8sCache.ResourceEventHandlerFuncs {
	mqt.logger.Info("Inside mqtInformerHandlers function")
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mqt.logger.Info("Added mqt")
			trigger := obj.(*fv1.MessageQueueTrigger)
			mqt.RegisterTrigger(trigger)
		},
		DeleteFunc: func(obj interface{}) {
			mqt.logger.Info("Delete mqt")
			trigger := obj.(*fv1.MessageQueueTrigger)
			triggerSubscription := mqt.getTriggerSubscription(&trigger.ObjectMeta)
			if triggerSubscription != nil {
				err := mqt.messageQueue.Unsubscribe(triggerSubscription.subscription)
				if err != nil {
					mqt.logger.Warn("failed to unsubscribe from message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
					return
				}
				mqt.logger.Info("Completed unsubscribe")
				mqt.delTrigger(&trigger.ObjectMeta)
				mqt.logger.Info("message queue trigger deleted", zap.String("trigger_name", trigger.ObjectMeta.Name))
			} else {
				mqt.logger.Info("Unsubscribe failed")
			}
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			mqt.logger.Info("Updated func")
			trigger := newObj.(*fv1.MessageQueueTrigger)
			mqt.RegisterTrigger(trigger)
		},
	}
}
