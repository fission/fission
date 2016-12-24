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

package controller

import (
	"github.com/satori/go.uuid"

	"github.com/fission/fission"
)

type HTTPTriggerStore struct {
	ResourceStore
}

func (hts *HTTPTriggerStore) Create(ht *fission.HTTPTrigger) (string, error) {
	ht.Metadata.Uid = uuid.NewV4().String()
	return ht.Metadata.Uid, hts.ResourceStore.create(ht)
}

func (hts *HTTPTriggerStore) Get(m *fission.Metadata) (*fission.HTTPTrigger, error) {
	var ht fission.HTTPTrigger
	err := hts.ResourceStore.read(m.Name, &ht)
	if err != nil {
		return nil, err
	}
	return &ht, nil
}

func (hts *HTTPTriggerStore) Update(ht *fission.HTTPTrigger) (string, error) {
	ht.Metadata.Uid = uuid.NewV4().String()
	return ht.Metadata.Uid, hts.ResourceStore.update(ht)
}

func (hts *HTTPTriggerStore) Delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.HTTPTrigger{})
	if err != nil {
		return err
	}
	return hts.ResourceStore.delete(typeName, m.Name)
}

func (hts *HTTPTriggerStore) List() ([]fission.HTTPTrigger, error) {
	typeName, err := getTypeName(fission.HTTPTrigger{})
	if err != nil {
		return nil, err
	}

	bufs, err := hts.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	triggers := make([]fission.HTTPTrigger, 0, len(bufs))
	js := JsonSerializer{}
	for _, buf := range bufs {
		var ht fission.HTTPTrigger
		err = js.deserialize([]byte(buf), &ht)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, ht)
	}

	return triggers, nil
}
