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
	"log"
	"sync"
	"time"

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
	MessageQueueTriggerManagerInterface interface {
		add(trigger fission.MessageQueueTrigger) error
		delete(string) error
		syncTriggers()
	}

	MessageQueueTriggerManager struct {
		sync.RWMutex
		mqCfg       MessageQueueConfig
		triggerMap  map[string]*triggerSubscrption
		requestChan chan []fission.MessageQueueTrigger
		routerUrl   string
		controller  *controllerClient.Client
	}

	triggerSubscrption struct {
		fission.Metadata
		funcMeta     fission.Metadata
		subscription interface{}
	}
)

func MakeMessageQueueManager(ctrlClient *controllerClient.Client,
	routerUrl string, mqConfig MessageQueueConfig) MessageQueueTriggerManagerInterface {

	var messageQueueMgr MessageQueueTriggerManagerInterface
	var err error

	mqTriggerMgr := MessageQueueTriggerManager{
		mqCfg:       mqConfig,
		triggerMap:  make(map[string]*triggerSubscrption),
		controller:  ctrlClient,
		routerUrl:   routerUrl,
		requestChan: make(chan []fission.MessageQueueTrigger),
	}
	switch mqConfig.MQType {
	case NATS:
		fallthrough
	default:
		messageQueueMgr, err = makeNatsTriggerManager(mqTriggerMgr)
	}
	if err != nil {
		log.Fatalf("Failed to connect to remote message queue server: %v", err)
	}
	go messageQueueMgr.syncTriggers()
	return messageQueueMgr
}

func (mqt *MessageQueueTriggerManager) addTrigger(triggerSub *triggerSubscrption) error {
	triggerUid := triggerSub.Uid
	if _, ok := mqt.getTrigger(triggerUid); ok {
		return errors.New("Trigger already exists")
	}
	mqt.Lock()
	defer mqt.Unlock()
	mqt.triggerMap[triggerUid] = triggerSub
	return nil
}

func (mqt *MessageQueueTriggerManager) getTrigger(triggerUid string) (*triggerSubscrption, bool) {
	mqt.RLock()
	defer mqt.RUnlock()
	trigger, ok := mqt.triggerMap[triggerUid]
	if !ok {
		return nil, false
	}
	return trigger, true
}

func (mqt *MessageQueueTriggerManager) getAllTriggers() map[string]*triggerSubscrption {
	mqt.RLock()
	defer mqt.RUnlock()
	copyTriggers := make(map[string]*triggerSubscrption)
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
			log.Println(err)
		}
		// sync trigger from controller
		mqt.requestChan <- newTriggers
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
