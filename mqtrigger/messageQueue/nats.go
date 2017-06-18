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
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	ns "github.com/nats-io/go-nats-streaming"
	nsUtil "github.com/nats-io/nats-streaming-server/util"
	log "github.com/sirupsen/logrus"

	"github.com/fission/fission"
)

const (
	natsClusterID  = "fissionMQTrigger"
	natsProtocol   = "nats://"
	natsClientID   = "fission"
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

	opts := []ns.SubscriptionOption{
		// Create a durable subscription to nats, so that triggers could retrieve last unack message.
		// https://github.com/nats-io/go-nats-streaming#durable-subscriptions
		ns.DurableName(trigger.Uid),

		// Nats-streaming server is auto-ack mode by default. Since we want nats-streaming server to
		// resend a message if the trigger does not ack it, we need to enable the manual ack mode, so that
		// trigger could choose to ack message or simply drop it depend on the response of function pod.
		ns.SetManualAckMode(),
	}
	sub, err := nats.nsConn.Subscribe(subj, msgHandler(nats, trigger), opts...)
	if err != nil {
		return err
	}
	triggerSub := triggerSubscription{
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
				log.Warnf("Message queue trigger %s created failed: %v", trigger.Name, err)
			} else {
				log.Infof("Message queue trigger %s created", trigger.Name)
			}
		}

		// remove old triggers
		for _, trigger := range currentTriggerMap {
			if _, ok := newTriggerMap[trigger.Uid]; ok {
				continue
			}
			if err := nats.delete(trigger.Uid); err != nil {
				log.Warnf("Message queue trigger %s deleted failed: %v", trigger.Name, err)
			} else {
				log.Infof("Message queue trigger %s deleted", trigger.Name)
			}
		}
	}
}

func isTopicValidForNats(topic string) bool {
	// nats-streaming does not support wildcard channel.
	return nsUtil.IsSubjectValid(topic)
}

func msgHandler(nats *Nats, trigger fission.MessageQueueTrigger) func(*ns.Msg) {
	return func(msg *ns.Msg) {
		url := nats.routerUrl + "/" + strings.TrimPrefix(fission.UrlForFunction(&trigger.Function), "/")
		log.Printf("Making HTTP request to %v", url)

		headers := map[string]string{
			"X-Fission-MQTrigger-Topic":     trigger.Topic,
			"X-Fission-MQTrigger-RespTopic": trigger.ResponseTopic,
		}

		// Create request
		req, err := http.NewRequest("POST", url, bytes.NewReader(msg.Data))
		for k, v := range headers {
			req.Header.Add(k, v)
		}

		// Make the request
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Warningf("Request failed: %v", url)
			return
		}
		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Warningf("Request body error: %v", string(body))
			return
		}
		if resp.StatusCode != 200 {
			log.Printf("Request returned failure: %v", resp.StatusCode)
			return
		}
		// trigger acks message only if a request done successfully
		err = msg.Ack()
		if err != nil {
			log.Warningf("Failed to ack message: %v", err)
		}
		err = nats.nsConn.Publish(trigger.ResponseTopic, body)
		if err != nil {
			log.Warningf("Failed to publish message to topic %s: %v", trigger.ResponseTopic, err)
		}
	}
}
