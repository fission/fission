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
	log "github.com/Sirupsen/logrus"

	"github.com/fission/fission"
)

type FunctionStore struct {
	ResourceStore
}

func (fs *FunctionStore) Create(f *fission.Function) (string, error) {
	code := []byte(f.Code)
	_, uid, err := fs.ResourceStore.writeFile(f.Key(), code)
	if err != nil {
		return "", err
	}

	f.Metadata.Uid = uid
	f.Code = ""

	err = fs.ResourceStore.create(f)
	if err != nil {
		fs.ResourceStore.deleteFile(f.Key(), uid) // ignore errors
		return "", err
	}
	return f.Metadata.Uid, nil
}

func (fs *FunctionStore) Get(m *fission.Metadata) (*fission.Function, error) {
	var f fission.Function
	err := fs.ResourceStore.read(m.Name, &f)
	if err != nil {
		return nil, err
	}

	var code []byte
	if len(m.Uid) > 0 {
		log.WithFields(log.Fields{"Uid": m.Uid}).Info("fetching by uid")
		code, err = fs.ResourceStore.readFile(m.Name, &m.Uid)
		f.Metadata = *m
	} else {
		code, err = fs.ResourceStore.readFile(m.Name, nil)
	}
	if err != nil {
		return nil, err
	}

	f.Code = string(code)
	return &f, nil
}

func (fs *FunctionStore) Update(f *fission.Function) (string, error) {
	code := []byte(f.Code)
	_, uid, err := fs.ResourceStore.writeFile(f.Key(), code)
	if err != nil {
		return "", err
	}

	var fnew fission.Function
	err = fs.ResourceStore.read(f.Metadata.Name, &fnew)
	if err != nil {
		fs.ResourceStore.deleteFile(f.Key(), uid) // ignore err
		return "", err
	}

	fnew.Metadata.Uid = uid
	fnew.Environment = f.Environment

	err = fs.ResourceStore.update(fnew)
	if err != nil {
		fs.ResourceStore.deleteFile(f.Key(), uid) // ignore err
		return "", err
	}
	return uid, err
}

func (fs *FunctionStore) Delete(m fission.Metadata) error {
	if len(m.Uid) == 0 {
		err := fs.ResourceStore.deleteAllFiles(m.Name)
		if err != nil {
			return err
		}
	} else {
		err := fs.ResourceStore.deleteFile(m.Name, m.Uid)
		if err != nil {
			return err
		}
	}
	typeName, err := getTypeName(fission.Function{})
	if err != nil {
		return err
	}

	bufs, err := fs.ResourceStore.getAll("file/" + m.Name)
	if err != nil {
		return err
	}
	if len(bufs) == 0 {
		return fs.ResourceStore.delete(typeName, m.Name)
	}

	fnew, err := fs.Get(&fission.Metadata{Name: m.Name})
	if err != nil {
		return err
	}

	latestUid := bufs[len(bufs)-1] // function always tracks the latest version of code
	if latestUid == fnew.Uid {
		return nil
	}
	fnew.Uid = latestUid
	return fs.ResourceStore.update(fnew)
}

func (fs *FunctionStore) List() ([]fission.Function, error) {
	typeName, err := getTypeName(fission.Function{})
	if err != nil {
		return nil, err
	}

	bufs, err := fs.ResourceStore.getAll(typeName)
	if err != nil {
		return nil, err
	}

	js := JsonSerializer{}
	functions := make([]fission.Function, 0, len(bufs))
	for _, buf := range bufs {
		var f fission.Function
		err = js.deserialize([]byte(buf), &f)
		if err != nil {
			return nil, err
		}
		functions = append(functions, f)
	}

	return functions, nil
}
