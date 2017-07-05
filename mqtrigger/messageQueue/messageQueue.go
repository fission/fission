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

package messageQueue

import (
	"errors"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/fission/fission"
	controllerClient "github.com/fission/fission/controller/client"
)

const (
	NATS string = "nats-streaming"
)

const (
	ADD_TRIGGER requestType = iota
	DELETE_TRIGGER
	GET_ALL_TRIGGERS
)

type (
	messageQueueSubscription interface{}

	requestType int

	MessageQueueConfig struct {
		MQType string
		Url    string
	}

	MessageQueue interface {
		subscribe(trigger fission.MessageQueueTrigger) (messageQueueSubscription, error)
		unsubscribe(triggerSub messageQueueSubscription) error
	}

	MessageQueueTriggerManager struct {
		reqChan      chan request
		mqCfg        MessageQueueConfig
		triggers     map[string]*triggerSubscription
		controller   *controllerClient.Client
		messageQueue MessageQueue
	}

	triggerSubscription struct {
		fission.Metadata
		funcMeta     fission.Metadata
		subscription messageQueueSubscription
	}

	request struct {
		requestType
		triggerSub *triggerSubscription
		respChan   chan response
	}
	response struct {
		err      error
		triggers *map[string]messageQueueSubscription
	}
)

func MakeMessageQueueTriggerManager(ctrlClient *controllerClient.Client,
	routerUrl string, mqConfig MessageQueueConfig) *MessageQueueTriggerManager {

	var messageQueue MessageQueue
	var err error

	mqTriggerMgr := MessageQueueTriggerManager{
		reqChan:    make(chan request),
		triggers:   make(map[string]*triggerSubscription),
		controller: ctrlClient,
	}
	switch mqConfig.MQType {
	case NATS:
		messageQueue, err = makeNatsMessageQueue(routerUrl, mqConfig)
	default:
		err = errors.New("No matched message queue type found")
	}
	if err != nil {
		log.Fatalf("Failed to connect to remote message queue server: %v", err)
	}
	mqTriggerMgr.messageQueue = messageQueue
	go mqTriggerMgr.service()
	go mqTriggerMgr.syncTriggers()
	return &mqTriggerMgr
}

func (mqt *MessageQueueTriggerManager) service() {
	for {
		req := <-mqt.reqChan
		switch req.requestType {
		case ADD_TRIGGER:
			var err error
			triggerUid := req.triggerSub.Uid
			if _, ok := mqt.triggers[triggerUid]; ok {
				err = errors.New("Trigger already exists")
			} else {
				mqt.triggers[triggerUid] = req.triggerSub
			}
			req.respChan <- response{err: err}
		case GET_ALL_TRIGGERS:
			copyTriggers := make(map[string]messageQueueSubscription)
			for key, val := range mqt.triggers {
				copyTriggers[key] = val
			}
			req.respChan <- response{triggers: &copyTriggers}
		case DELETE_TRIGGER:
			triggerUid := req.triggerSub.Uid
			delete(mqt.triggers, triggerUid)
		}
	}
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

func (mqt *MessageQueueTriggerManager) getAllTriggers() *map[string]messageQueueSubscription {
	respChan := make(chan response)
	mqt.reqChan <- request{
		requestType: GET_ALL_TRIGGERS,
		respChan:    respChan,
	}
	r := <-respChan
	return r.triggers
}

func (mqt *MessageQueueTriggerManager) delTrigger(triggerUid string) {
	mqt.reqChan <- request{
		requestType: DELETE_TRIGGER,
		triggerSub: &triggerSubscription{
			Metadata: fission.Metadata{
				Uid: triggerUid,
			},
		},
	}
}

func (mqt *MessageQueueTriggerManager) syncTriggers() {
	for {
		// TODO: handle error
		newTriggers, err := mqt.controller.MessageQueueTriggerList(mqt.mqCfg.MQType)
		if err != nil {
			log.Warnf("Failed to sync message queue trigger from controller: %v", err)
		}
		// sync trigger from controller
		newTriggerMap := map[string]fission.MessageQueueTrigger{}
		for _, trigger := range newTriggers {
			newTriggerMap[trigger.Uid] = trigger
		}
		currentTriggerSubMap := mqt.getAllTriggers()

		// register new triggers
		for key, trigger := range newTriggerMap {
			if _, ok := (*currentTriggerSubMap)[key]; ok {
				continue
			}
			sub, err := mqt.messageQueue.subscribe(trigger)
			if err != nil {
				log.Warnf("Message queue trigger %s created failed: %v", trigger.Name, err)
				continue
			}
			triggerSub := triggerSubscription{
				Metadata: fission.Metadata{
					Name: trigger.Name,
					Uid:  trigger.Uid,
				},
				funcMeta:     trigger.Function,
				subscription: sub,
			}
			err = mqt.addTrigger(&triggerSub)
			if err != nil {
				log.Warnf("Message queue trigger %s created failed: %v", trigger.Name, err)
				continue
			}
			log.Infof("Message queue trigger %s created", trigger.Name)
		}

		// remove old triggers
		for _, ts := range *currentTriggerSubMap {
			triggerSub := ts.(*triggerSubscription)
			if _, ok := newTriggerMap[triggerSub.Uid]; ok {
				continue
			}
			if err := mqt.messageQueue.unsubscribe(triggerSub.subscription); err != nil {
				log.Warnf("Message queue trigger %s deleted failed: %v", triggerSub.Name, err)
			} else {
				mqt.delTrigger(triggerSub.Uid)
				log.Infof("Message queue trigger %s deleted", triggerSub.Name)
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func IsTopicValid(mqType string, topic string) bool {
	switch mqType {
	case NATS:
		return isTopicValidForNats(topic)
	}
	return false
}
