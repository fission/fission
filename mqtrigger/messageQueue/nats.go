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
	"github.com/fission/fission/publisher"
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
		nsConn      *ns.Conn
		nsPublisher *publisher.NatsPublisher
	}
)

// To enable nats authentication, please follow the instruction described in
// http://nats.io/documentation/server/gnatsd-authentication/.
// And dont forget to change the MESSAGE_QUEUE_URL in mqtrigger deployment.

func makeNatsTriggerManager(mqTriggerMgr MessageQueueTriggerManager) (MessageQueueTriggerManagerInterface, error) {
	conn, err := ns.Connect(natsClusterID, natsClientID, ns.NatsURL(mqTriggerMgr.mqCfg.Url))
	if err != nil {
		return nil, err
	}
	nsPublisher := publisher.MakeNatsPublisher(&conn)
	nats := Nats{
		MessageQueueTriggerManager: mqTriggerMgr,
		nsConn:      &conn,
		nsPublisher: nsPublisher,
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
		url := nats.routerUrl + "/" + strings.TrimPrefix(fission.UrlForFunction(&trigger.Function), "/")
		log.Printf("Making HTTP request to %v", url)

		// Create request
		resp, err := http.Post(url, "", bytes.NewReader(msg.Data))
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
		if len(trigger.ResponseTopic) > 0 {
			nats.nsPublisher.Publish(string(body), nil, trigger.ResponseTopic)
		}
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
	sub, err := (*nats.nsConn).Subscribe(subj, handler, opts...)
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
