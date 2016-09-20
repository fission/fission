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

	"github.com/platform9/fission"
)

type HTTPTriggerStore struct {
	resourceStore
}

func (hts *HTTPTriggerStore) create(ht *fission.HTTPTrigger) error {
	ht.Metadata.Uid = uuid.NewV4().String()
	return hts.resourceStore.create(ht)
}

func (hts *HTTPTriggerStore) read(m fission.Metadata) (*fission.HTTPTrigger, error) {
	var ht fission.HTTPTrigger
	err := hts.resourceStore.read(m.Name, &ht)
	if err != nil {
		return nil, err
	}
	return &ht, nil
}

func (hts *HTTPTriggerStore) update(ht *fission.HTTPTrigger) error {
	err := validateHTTPTrigger(ht)
	if err != nil {
		return err
	}
	ht.Metadata.Uid = uuid.NewV4().String()
	return hts.resourceStore.update(ht)
}

func (hts *HTTPTriggerStore) delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.HTTPTrigger{})
	if err != nil {
		return err
	}
	return hts.resourceStore.delete(typeName, m.Name)
}

func (hts *HTTPTriggerStore) list() ([]fission.HTTPTrigger, error) {
	typeName, err := getTypeName(fission.HTTPTrigger{})
	if err != nil {
		return nil, err
	}

	bufs, err := hts.resourceStore.getAll(typeName)
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
