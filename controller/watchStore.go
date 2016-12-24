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

type WatchStore struct {
	ResourceStore
}

func (ws *WatchStore) Create(w *fission.Watch) (string, error) {
	w.Metadata.Uid = uuid.NewV4().String()
	return w.Metadata.Uid, ws.ResourceStore.create(w)
}

func (ws *WatchStore) Get(m *fission.Metadata) (*fission.Watch, error) {
	var w fission.Watch
	err := ws.ResourceStore.read(m.Name, &w)
	if err != nil {
		return nil, err
	}
	return &w, err
}

func (ws *WatchStore) Update(w *fission.Watch) (string, error) {
	w.Metadata.Uid = uuid.NewV4().String()
	return w.Metadata.Uid, ws.ResourceStore.update(w)
}

func (ws *WatchStore) Delete(m fission.Metadata) error {
	typeName, err := getTypeName(fission.Watch{})
	if err != nil {
		return err
	}
	return ws.ResourceStore.delete(typeName, m.Name)
}

func (ws *WatchStore) List() ([]fission.Watch, error) {
	typeName, err := getTypeName(fission.Watch{})
	if err != nil {
		return nil, err
	}

	bufs, err := ws.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	watches := make([]fission.Watch, 0, len(bufs))
	js := JsonSerializer{}
	for _, buf := range bufs {
		var w fission.Watch
		err = js.deserialize([]byte(buf), &w)
		if err != nil {
			return nil, err
		}
		watches = append(watches, w)
	}

	return watches, nil
}
