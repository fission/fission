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

type EnvironmentStore struct {
	ResourceStore
}

func (es *EnvironmentStore) Create(e *fission.Environment) (string, error) {
	e.Metadata.Uid = uuid.NewV4().String()
	return e.Metadata.Uid, es.ResourceStore.create(e)
}

func (es *EnvironmentStore) Get(m *fission.Metadata) (*fission.Environment, error) {
	var e fission.Environment
	err := es.ResourceStore.read(m.Name, &e)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (es *EnvironmentStore) Update(e *fission.Environment) (string, error) {
	e.Metadata.Uid = uuid.NewV4().String()
	return e.Metadata.Uid, es.ResourceStore.update(e)
}

func (es *EnvironmentStore) Delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.Environment{})
	if err != nil {
		return err
	}
	return es.ResourceStore.delete(typeName, m.Name)
}

func (es *EnvironmentStore) List() ([]fission.Environment, error) {
	typeName, err := getTypeName(fission.Environment{})
	if err != nil {
		return nil, err
	}

	bufs, err := es.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	envs := make([]fission.Environment, 0, len(bufs))
	js := JsonSerializer{}
	for _, buf := range bufs {
		var e fission.Environment
		err = js.deserialize([]byte(buf), &e)
		if err != nil {
			return nil, err
		}
		envs = append(envs, e)
	}

	return envs, nil
}
