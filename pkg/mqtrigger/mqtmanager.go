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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/utils"
)

const (
	ADD_TRIGGER requestType = iota
	DELETE_TRIGGER
	GET_ALL_TRIGGERS
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
		err      error
		triggers *map[string]*triggerSubscription
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

func (mqt *MessageQueueTriggerManager) Run() {
	go mqt.service()
	go mqt.syncTriggers()
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
		case GET_ALL_TRIGGERS:
			copyTriggers := make(map[string]*triggerSubscription)
			for key, val := range mqt.triggers {
				copyTriggers[key] = val
			}
			req.respChan <- response{triggers: &copyTriggers}
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
	r := <-respChan
	return r.err
}

func (mqt *MessageQueueTriggerManager) getAllTriggers() *map[string]*triggerSubscription {
	respChan := make(chan response)
	mqt.reqChan <- request{
		requestType: GET_ALL_TRIGGERS,
		respChan:    respChan,
	}
	r := <-respChan
	return r.triggers
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

func (mqt *MessageQueueTriggerManager) syncTriggers() {
	for {
		// get new set of triggers
		newTriggers, err := mqt.fissionClient.CoreV1().MessageQueueTriggers(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			if utils.IsNetworkError(err) {
				mqt.logger.Error("encountered network error, will retry", zap.Error(err))
				time.Sleep(5 * time.Second)
				continue
			}
			mqt.logger.Fatal("failed to read message queue trigger list", zap.Error(err))
		}
		newTriggerMap := make(map[string]*fv1.MessageQueueTrigger)
		for index := range newTriggers.Items {
			newTrigger := &newTriggers.Items[index]
			if newTrigger.Spec.MessageQueueType == mqt.messageQueueType {
				newTriggerMap[crd.CacheKey(&newTrigger.ObjectMeta)] = newTrigger
			}
		}

		// get current set of triggers
		currentTriggers := mqt.getAllTriggers()

		// register new triggers
		for key, trigger := range newTriggerMap {
			if _, ok := (*currentTriggers)[key]; ok {
				continue
			}

			// actually subscribe using the message queue client impl
			sub, err := mqt.messageQueue.Subscribe(trigger)
			if err != nil {
				mqt.logger.Warn("failed to subscribe to message queue trigger", zap.Error(err), zap.String("trigger_name", trigger.ObjectMeta.Name))
				continue
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

		// remove old triggers
		for key, triggerSub := range *currentTriggers {
			if _, ok := newTriggerMap[key]; ok {
				continue
			}
			err := mqt.messageQueue.Unsubscribe(triggerSub.subscription)
			if err != nil {
				mqt.logger.Warn("failed to unsubscribe from message queue trigger", zap.Error(err), zap.String("trigger_name", triggerSub.trigger.ObjectMeta.Name))
				continue
			}
			mqt.delTrigger(&triggerSub.trigger.ObjectMeta)
			mqt.logger.Info("message queue trigger deleted", zap.String("trigger_name", triggerSub.trigger.ObjectMeta.Name))
		}

		// TODO replace with a watch
		time.Sleep(time.Second)
	}
}
