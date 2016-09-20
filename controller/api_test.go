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
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	log "github.com/Sirupsen/logrus"
	etcdClient "github.com/coreos/etcd/client"
	"golang.org/x/net/context"

	"github.com/platform9/fission"
	"github.com/platform9/fission/controller/client"
)

func TestFunctionApi(t *testing.T) {
	log.SetFormatter(&log.TextFormatter{DisableColors: true})

	_, ks, rs := getTestResourceStore()
	fs := &FunctionStore{resourceStore: *rs}
	hts := &HTTPTriggerStore{resourceStore: *rs}
	es := &EnvironmentStore{resourceStore: *rs}

	api := &API{
		FunctionStore:    *fs,
		HTTPTriggerStore: *hts,
		EnvironmentStore: *es,
	}

	testFunc := &fission.Function{
		Metadata: fission.Metadata{
			Name: "foo",
			Uid:  "",
		},
		Environment: fission.Metadata{
			Name: "nodejs",
			Uid:  "xxx",
		},
		Code: "code1",
	}

	go api.serve(8888)
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Get("http://localhost:8888/")
	panicIf(err)
	_, err = ioutil.ReadAll(resp.Body)
	panicIf(err)

	client := client.New("http://localhost:8888")

	_, err = ks.Delete(context.Background(), "Function", &etcdClient.DeleteOptions{Recursive: true})
	if err != nil {
		log.Printf("failed to delete: %v", err)
	}

	m, err := client.FunctionCreate(testFunc)
	panicIf(err)
	uid1 := m.Uid
	log.Printf("Created function %v: %v", m.Name, m.Uid)

	testFunc.Code = "code2"
	m, err = client.FunctionUpdate(testFunc)
	panicIf(err)
	uid2 := m.Uid
	log.Printf("Updated function %v: %v", m.Name, m.Uid)

	m.Uid = uid1
	testFunc.Code = "code1"
	f, err := client.FunctionGet(m)
	panicIf(err)

	testFunc.Metadata.Uid = m.Uid
	log.Printf("f = %#v", f)
	log.Printf("testFunc = %#v", testFunc)
	assert(*f == *testFunc, "first version should match when read by uid")

	m.Uid = uid2
	testFunc.Metadata.Uid = m.Uid
	testFunc.Code = "code2"
	f, err = client.FunctionGet(m)
	panicIf(err)

	assert(*f == *testFunc, "second version should match when read by uid")

	m.Uid = ""
	testFunc.Metadata.Uid = uid2
	testFunc.Code = "code2"
	f, err = client.FunctionGet(m)
	panicIf(err)

	assert(*f == *testFunc, "second version should match when read as latest")

	testFunc.Metadata.Name = "bar"
	m, err = client.FunctionCreate(testFunc)
	panicIf(err)

	funcs, err := client.FunctionList()
	panicIf(err)
	assert(len(funcs) == 2,
		"created two functions, but didn't find them")

	err = client.FunctionDelete(&fission.Metadata{Name: "foo"})
	panicIf(err)
	err = client.FunctionDelete(&fission.Metadata{Name: "bar"})
	panicIf(err)
}
