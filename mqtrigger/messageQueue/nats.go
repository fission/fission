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
	"fmt"

	log "github.com/Sirupsen/logrus"
	ns "github.com/nats-io/go-nats-streaming"
	nsUtil "github.com/nats-io/nats-streaming-server/util"

	"github.com/fission/fission"
)

const (
	natsClusterID  = "fissionMQTrigger"
	natsClientID   = "fission"
	natsProtocol   = "nats://"
	natsQueueGroup = "fission-messageQueueNatsTrigger"
)

type (
	Nats struct {
		MessageQueueTriggerManager
		nsConn ns.Conn
	}
)

func makeNatsTriggerManager(mqTriggerMgr MessageQueueTriggerManager) (MessageQueueTriggerManagerInterface, error) {
	conn, err := ns.Connect(natsClusterID, natsClientID, ns.NatsURL(mqTriggerMgr.mqCfg.Url))
	if err != nil {
		return nil, err
	}
	nats := Nats{
		MessageQueueTriggerManager: mqTriggerMgr,
		nsConn: conn,
	}
	go nats.sync()
	return &nats, nil
}

func (nats *Nats) add(trigger fission.MessageQueueTrigger) error {
	subj := trigger.Topic

	if !isTopicValidForNats(subj) {
		return errors.New(fmt.Sprintf("Not a valid topic: %s", trigger.Topic))
	}

	handler := func(msg *ns.Msg) {
		headers := map[string]string{
			"X-Fission-Timer-Name": trigger.Function.Name,
		}
		// TODO: should we pass message body to function?
		nats.publisher.Publish("", headers, fission.UrlForFunction(&trigger.Function))
	}

	// create a durable subscription to nats, so that triggers could retrieve last unack message.
	// https://github.com/nats-io/go-nats-streaming#durable-subscriptions
	opt := ns.DurableName(trigger.Uid)
	sub, err := nats.nsConn.Subscribe(subj, handler, opt)
	if err != nil {
		return err
	}
	triggerSub := triggerSubscrption{
		Metadata: fission.Metadata{
			Name: trigger.Name,
			Uid:  trigger.Uid,
		},
		funcMeta:     trigger.Function,
		subscription: sub,
	}
	return nats.addTrigger(&triggerSub)
}

func (nats *Nats) delete(triggerUid string) error {
	triggerSub, ok := nats.getTrigger(triggerUid)
	if !ok {
		return errors.New("Trigger not exists")
	}
	defer nats.delTrigger(triggerUid)
	nsSub := triggerSub.subscription.(ns.Subscription)
	err := nsSub.Close()
	return err
}

func (nats *Nats) sync() {
	for {
		newTriggers := <-nats.requestChan
		newTriggerMap := map[string]fission.MessageQueueTrigger{}
		for _, trigger := range newTriggers {
			newTriggerMap[trigger.Uid] = trigger
		}

		currentTriggerMap := nats.getAllTriggers()

		// register new triggers
		for key, trigger := range newTriggerMap {
			if _, ok := currentTriggerMap[key]; ok {
				continue
			}
			if err := nats.add(trigger); err != nil {
				log.Warnf("MessageQueue trigger %s created failed: %v", trigger.Name, err)
			} else {
				log.Infof("MessageQueue trigger %s created", trigger.Name)
			}
		}

		// remove old triggers
		for _, trigger := range currentTriggerMap {
			if _, ok := newTriggerMap[trigger.Uid]; ok {
				continue
			}
			if err := nats.delete(trigger.Uid); err != nil {
				log.Warnf("MessageQueue trigger %s deleted failed: %v", trigger.Name, err)
			} else {
				log.Infof("MessageQueue trigger %s deleted", trigger.Name)
			}
		}
	}
}

func isTopicValidForNats(topic string) bool {
	// nats-streaming does not support wildcard channl.
	return nsUtil.IsSubjectValid(topic, false)
}
