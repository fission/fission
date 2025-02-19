/*
Copyright 2020 The Fission Authors.

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

package factory

import (
	"fmt"
	"sync"

	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
)

var (
	messageQueueFactories = make(map[fv1.MessageQueueType]MessageQueueFactory)
	lock                  = sync.Mutex{}
)

type (
	MessageQueueFactory interface {
		Create(logger *zap.Logger, config messageQueue.Config, routerURL string) (messageQueue.MessageQueue, error)
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

func Create(logger *zap.Logger, mqType fv1.MessageQueueType, mqConfig messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	factory, registered := messageQueueFactories[mqType]
	if !registered {
		return nil, fmt.Errorf("no supported message queue type found for %q", mqType)
	}
	return factory.Create(logger, mqConfig, routerUrl)
}
