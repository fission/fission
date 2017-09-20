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

	"github.com/fission/fission/crd"
)

type Asq struct {
}

func makeAsqMessageQueue(routerURL string, mqCfg MessageQueueConfig) (MessageQueue, error) {
	// TODO: implement
	return &Asq{}, nil
}

func (asq Asq) subscribe(trigger *crd.MessageQueueTrigger) (messageQueueSubscription, error) {
	// TODO: implement
	return nil, errors.New("not yet implemented")
}

func (asq Asq) unsubscribe(subscription messageQueueSubscription) error {
	// TODO: implement
	return errors.New("not yet implemented")
}

func isTopicValidForAzure(topic string) bool {
	// TODO: implement
	return true
}
