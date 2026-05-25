// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"sync"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
)

var (
	messageQueueFactories = make(map[fv1.MessageQueueType]MessageQueueFactory)
	lock                  = sync.Mutex{}
)

type (
	MessageQueueFactory interface {
		Create(logger logr.Logger, config messageQueue.Config, routerURL string) (messageQueue.MessageQueue, error)
	}
)

func Register(mqType fv1.MessageQueueType, factory MessageQueueFactory) {
	lock.Lock()
	defer lock.Unlock()

	if factory == nil {
		panic("Nil message queue factory")
	}

	_, registered := messageQueueFactories[mqType]
	if registered {
		panic("Message queue factory already register")
	}

	messageQueueFactories[mqType] = factory
}

func Create(logger logr.Logger, mqType fv1.MessageQueueType, mqConfig messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	factory, registered := messageQueueFactories[mqType]
	if !registered {
		return nil, fmt.Errorf("no supported message queue type found for %q", mqType)
	}
	return factory.Create(logger, mqConfig, routerUrl)
}
