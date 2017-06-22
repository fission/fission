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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/fission/fission"
	controllerClient "github.com/fission/fission/controller/client"
)

type MessageQueueConfig struct {
	MQType string
	Url    string
}

const (
	NATS string = "nats-streaming"
)

type (
	MessageQueue interface {
		subscribe(trigger fission.MessageQueueTrigger) (*triggerSubscription, error)
		unsubscribe(triggerSub *triggerSubscription) error
	}

	MessageQueueTriggerManager struct {
		sync.RWMutex
		mqCfg        MessageQueueConfig
		triggerMap   map[string]*triggerSubscription
		controller   *controllerClient.Client
		messageQueue MessageQueue
	}

	triggerSubscription struct {
		fission.Metadata
		funcMeta     fission.Metadata
		subscription interface{}
	}
)

func MakeMessageQueueTriggerManager(ctrlClient *controllerClient.Client,
	routerUrl string, mqConfig MessageQueueConfig) *MessageQueueTriggerManager {

	var messageQueue MessageQueue
	var err error

	mqTriggerMgr := MessageQueueTriggerManager{
		triggerMap: make(map[string]*triggerSubscription),
		controller: ctrlClient,
	}
	switch mqConfig.MQType {
	case NATS:
		fallthrough
	default:
		messageQueue, err = makeNatsMessageQueue(routerUrl, mqConfig)
	}
	if err != nil {
		log.Fatalf("Failed to connect to remote message queue server: %v", err)
	}
	mqTriggerMgr.messageQueue = messageQueue
	go mqTriggerMgr.syncTriggers()
	return &mqTriggerMgr
}

func (mqt *MessageQueueTriggerManager) addTrigger(triggerSub *triggerSubscription) error {
	triggerUid := triggerSub.Uid
	if _, ok := mqt.getTrigger(triggerUid); ok {
		return errors.New("Trigger already exists")
	}
	mqt.Lock()
	defer mqt.Unlock()
	mqt.triggerMap[triggerUid] = triggerSub
	return nil
}

func (mqt *MessageQueueTriggerManager) getTrigger(triggerUid string) (*triggerSubscription, bool) {
	mqt.RLock()
	defer mqt.RUnlock()
	trigger, ok := mqt.triggerMap[triggerUid]
	if !ok {
		return nil, false
	}
	return trigger, true
}

func (mqt *MessageQueueTriggerManager) getAllTriggers() map[string]*triggerSubscription {
	mqt.RLock()
	defer mqt.RUnlock()
	copyTriggers := make(map[string]*triggerSubscription)
	for key, val := range mqt.triggerMap {
		copyTriggers[key] = val
	}
	return copyTriggers
}

func (mqt *MessageQueueTriggerManager) delTrigger(triggerUid string) {
	mqt.Lock()
	defer mqt.Unlock()
	delete(mqt.triggerMap, triggerUid)
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
		currentTriggerMap := mqt.getAllTriggers()

		// register new triggers
		for key, trigger := range newTriggerMap {
			if _, ok := currentTriggerMap[key]; ok {
				continue
			}
			triggerSub, err := mqt.messageQueue.subscribe(trigger)
			if err != nil {
				log.Warnf("Message queue trigger %s created failed: %v", trigger.Name, err)
				continue
			}
			err = mqt.addTrigger(triggerSub)
			if err != nil {
				log.Warnf("Message queue trigger %s created failed: %v", trigger.Name, err)
				continue
			}
			log.Infof("Message queue trigger %s created", trigger.Name)
		}

		// remove old triggers
		for _, trigger := range currentTriggerMap {
			if _, ok := newTriggerMap[trigger.Uid]; ok {
				continue
			}
			if err := mqt.messageQueue.unsubscribe(trigger); err != nil {
				log.Warnf("Message queue trigger %s deleted failed: %v", trigger.Name, err)
			} else {
				mqt.delTrigger(trigger.Uid)
				log.Infof("Message queue trigger %s deleted", trigger.Name)
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
