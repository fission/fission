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
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"testing"
	"time"

	log "github.com/Sirupsen/logrus"
	etcdClient "github.com/coreos/etcd/client"
	"golang.org/x/net/context"

	"github.com/platform9/fission"
	"github.com/platform9/fission/controller/client"
)

var g struct {
	client *client.Client
}

func TestFunctionApi(t *testing.T) {
	log.SetFormatter(&log.TextFormatter{DisableColors: true})

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

	m, err := g.client.FunctionCreate(testFunc)
	panicIf(err)
	uid1 := m.Uid
	//log.Printf("Created function %v: %v", m.Name, m.Uid)

	code, err := g.client.FunctionGetRaw(m)
	panicIf(err)
	assert(string(code) == testFunc.Code, "code from FunctionGetRaw must match created function")

	testFunc.Code = "code2"
	m, err = g.client.FunctionUpdate(testFunc)
	panicIf(err)
	uid2 := m.Uid
	//log.Printf("Updated function %v: %v", m.Name, m.Uid)

	m.Uid = uid1
	testFunc.Code = "code1"
	f, err := g.client.FunctionGet(m)
	panicIf(err)

	testFunc.Metadata.Uid = m.Uid
	//log.Printf("f = %#v", f)
	//log.Printf("testFunc = %#v", testFunc)
	assert(*f == *testFunc, "first version should match when read by uid")

	m.Uid = uid2
	testFunc.Metadata.Uid = m.Uid
	testFunc.Code = "code2"
	f, err = g.client.FunctionGet(m)
	panicIf(err)

	assert(*f == *testFunc, "second version should match when read by uid")

	m.Uid = ""
	testFunc.Metadata.Uid = uid2
	testFunc.Code = "code2"
	f, err = g.client.FunctionGet(m)
	panicIf(err)

	assert(*f == *testFunc, "second version should match when read as latest")

	testFunc.Metadata.Name = "bar"
	m, err = g.client.FunctionCreate(testFunc)
	panicIf(err)

	funcs, err := g.client.FunctionList()
	panicIf(err)
	assert(len(funcs) == 2,
		"created two functions, but didn't find them")

	err = g.client.FunctionDelete(&fission.Metadata{Name: "foo"})
	panicIf(err)
	err = g.client.FunctionDelete(&fission.Metadata{Name: "bar"})
	panicIf(err)
}

func TestHTTPTriggerApi(t *testing.T) {
	testTrigger := &fission.HTTPTrigger{
		Metadata: fission.Metadata{
			Name: "xxx",
			Uid:  "yyy",
		},
		UrlPattern: "/hello",
		Function: fission.Metadata{
			Name: "foo",
			Uid:  "",
		},
	}
	m, err := g.client.HTTPTriggerCreate(testTrigger)
	panicIf(err)
	defer g.client.HTTPTriggerDelete(m)

	tr, err := g.client.HTTPTriggerGet(m)
	panicIf(err)
	testTrigger.Metadata.Uid = m.Uid
	assert(*testTrigger == *tr, "trigger should match after reading")

	testTrigger.UrlPattern = "/hi"
	m2, err := g.client.HTTPTriggerUpdate(testTrigger)
	panicIf(err)

	m.Uid = m2.Uid
	tr, err = g.client.HTTPTriggerGet(m)
	panicIf(err)
	testTrigger.Metadata.Uid = m.Uid
	assert(*testTrigger == *tr, "trigger should match after reading")

	testTrigger.Metadata.Name = "yyy"
	m, err = g.client.HTTPTriggerCreate(testTrigger)
	panicIf(err)
	defer g.client.HTTPTriggerDelete(m)

	ts, err := g.client.HTTPTriggerList()
	panicIf(err)
	assert(len(ts) == 2, "created two triggers, but didn't find them")
}

func TestEnvironmentApi(t *testing.T) {
	testEnv := &fission.Environment{
		Metadata: fission.Metadata{
			Name: "xxx",
			Uid:  "yyy",
		},
		RunContainerImageUrl: "gcr.io/xyz",
	}
	m, err := g.client.EnvironmentCreate(testEnv)
	panicIf(err)
	defer g.client.EnvironmentDelete(m)

	tr, err := g.client.EnvironmentGet(m)
	panicIf(err)
	testEnv.Metadata.Uid = m.Uid
	assert(*testEnv == *tr, "env should match after reading")

	testEnv.RunContainerImageUrl = "/hi"
	m2, err := g.client.EnvironmentUpdate(testEnv)
	panicIf(err)

	m.Uid = m2.Uid
	tr, err = g.client.EnvironmentGet(m)
	panicIf(err)
	testEnv.Metadata.Uid = m.Uid
	assert(*testEnv == *tr, "env should match after reading")

	testEnv.Metadata.Name = "yyy"
	m, err = g.client.EnvironmentCreate(testEnv)
	panicIf(err)
	defer g.client.EnvironmentDelete(m)

	ts, err := g.client.EnvironmentList()
	panicIf(err)
	assert(len(ts) == 2, "created two envs, but didn't find them")
}

func TestWatchApi(t *testing.T) {
	testWatch := &fission.Watch{
		Metadata: fission.Metadata{
			Name: "xxx",
			Uid:  "yyy",
		},
		Namespace:     "default",
		ObjType:       "pod",
		LabelSelector: "",
		FieldSelector: "",
		Function: fission.Metadata{
			Name: "foo",
			Uid:  "",
		},
		Target: "",
	}
	m, err := g.client.WatchCreate(testWatch)
	panicIf(err)
	defer g.client.WatchDelete(m)

	w, err := g.client.WatchGet(m)
	panicIf(err)
	testWatch.Metadata.Uid = m.Uid
	w.Target = ""
	assert(*testWatch == *w, "watch should match after reading")

	testWatch.Metadata.Name = "yyy"
	m2, err := g.client.WatchCreate(testWatch)
	panicIf(err)
	defer g.client.WatchDelete(m2)

	ws, err := g.client.WatchList()
	panicIf(err)
	assert(len(ws) == 2, "created two envs, but didn't find them")
}

func TestMain(m *testing.M) {
	flag.Parse()

	fileStore, ks, rs := getTestResourceStore()
	defer os.RemoveAll(fileStore.root)

	api := MakeAPI(rs)
	g.client = client.MakeClient("http://localhost:8888")

	ks.Delete(context.Background(), "Function", &etcdClient.DeleteOptions{Recursive: true})
	ks.Delete(context.Background(), "HTTPTrigger", &etcdClient.DeleteOptions{Recursive: true})
	ks.Delete(context.Background(), "Environment", &etcdClient.DeleteOptions{Recursive: true})
	ks.Delete(context.Background(), "Watch", &etcdClient.DeleteOptions{Recursive: true})

	go api.Serve(8888)
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Get("http://localhost:8888/")
	panicIf(err)
	assert(resp.StatusCode == 200, "http get status code on root")
	_, err = ioutil.ReadAll(resp.Body)
	panicIf(err)

	os.Exit(m.Run())
}
