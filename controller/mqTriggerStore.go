/*
CopyrigmqTrigger 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    mqTriggertp://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/fission/fission"
	uuid "github.com/satori/go.uuid"
)

type MessageQueueTriggerStore struct {
	ResourceStore
}

func (mqs *MessageQueueTriggerStore) Create(mqTrigger *fission.MessageQueueTrigger) (string, error) {
	mqTrigger.Metadata.Uid = uuid.NewV4().String()
	return mqTrigger.Metadata.Uid, mqs.ResourceStore.create(mqTrigger)
}

func (mqs *MessageQueueTriggerStore) Get(m *fission.Metadata) (*fission.MessageQueueTrigger, error) {
	var mqTrigger fission.MessageQueueTrigger
	err := mqs.ResourceStore.read(m.Name, &mqTrigger)
	if err != nil {
		return nil, err
	}
	return &mqTrigger, nil
}

func (mqs *MessageQueueTriggerStore) Update(mqTrigger *fission.MessageQueueTrigger) (string, error) {
	mqTrigger.Metadata.Uid = uuid.NewV4().String()
	return mqTrigger.Metadata.Uid, mqs.ResourceStore.update(mqTrigger)
}

func (mqs *MessageQueueTriggerStore) Delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.MessageQueueTrigger{})
	if err != nil {
		return err
	}
	return mqs.ResourceStore.delete(typeName, m.Name)
}

func (mqs *MessageQueueTriggerStore) List(mqType string) ([]fission.MessageQueueTrigger, error) {
	typeName, err := getTypeName(fission.MessageQueueTrigger{})
	if err != nil {
		return nil, err
	}
	bufs, err := mqs.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	triggers := make([]fission.MessageQueueTrigger, 0, len(bufs))
	js := JsonSerializer{}
	for _, buf := range bufs {
		var mqTrigger fission.MessageQueueTrigger
		err = js.deserialize([]byte(buf), &mqTrigger)
		if err != nil {
			return nil, err
		}
		if len(mqType) > 0 && mqType != mqTrigger.MessageQueueType {
			continue
		}
		triggers = append(triggers, mqTrigger)
	}

	return triggers, nil
}
