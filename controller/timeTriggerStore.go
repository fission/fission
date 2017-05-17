/*
Copyrigtt 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    tttp://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/satori/go.uuid"

	"github.com/fission/fission"
)

type TimeTriggerStore struct {
	ResourceStore
}

func (tts *TimeTriggerStore) Create(tt *fission.TimeTrigger) (string, error) {
	tt.Metadata.Uid = uuid.NewV4().String()
	return tt.Metadata.Uid, tts.ResourceStore.create(tt)
}

func (tts *TimeTriggerStore) Get(m *fission.Metadata) (*fission.TimeTrigger, error) {
	var tt fission.TimeTrigger
	err := tts.ResourceStore.read(m.Name, &tt)
	if err != nil {
		return nil, err
	}
	return &tt, nil
}

func (tts *TimeTriggerStore) Update(tt *fission.TimeTrigger) (string, error) {
	tt.Metadata.Uid = uuid.NewV4().String()
	return tt.Metadata.Uid, tts.ResourceStore.update(tt)
}

func (tts *TimeTriggerStore) Delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.TimeTrigger{})
	if err != nil {
		return err
	}
	return tts.ResourceStore.delete(typeName, m.Name)
}

func (tts *TimeTriggerStore) List() ([]fission.TimeTrigger, error) {
	typeName, err := getTypeName(fission.TimeTrigger{})
	if err != nil {
		return nil, err
	}

	bufs, err := tts.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	triggers := make([]fission.TimeTrigger, 0, len(bufs))
	js := JsonSerializer{}
	for _, buf := range bufs {
		var tt fission.TimeTrigger
		err = js.deserialize([]byte(buf), &tt)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, tt)
	}

	return triggers, nil
}
