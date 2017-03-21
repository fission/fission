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

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
)

var g struct {
	client *client.Client
}

func assertNameReuseFails(err error, name string) {
	assert(err != nil, "recreating "+name+" with same name must fail")
	fe, ok := err.(fission.Error)
	assert(ok, "error must be a fission Error")
	assert(fe.Code == fission.ErrorNameExists, "error must be a name exists error")
}

func assertNotFoundFails(err error, name string) {
	assert(err != nil, "requesting a non-existent "+name+" must fail")
	fe, ok := err.(fission.Error)
	assert(ok, "error must be a fission Error")
	assert(fe.Code == fission.ErrorNotFound, "error must be a not found error")
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
	_, err := g.client.FunctionGet(&fission.Metadata{Name: "foo"})
	assertNotFoundFails(err, "function")

	m, err := g.client.FunctionCreate(testFunc)
	panicIf(err)
	uid1 := m.Uid
	//log.Printf("Created function %v: %v", m.Name, m.Uid)

	_, err = g.client.FunctionCreate(testFunc)
	assertNameReuseFails(err, "function")

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

	funcs_url := g.client.Url + "/v1/functions"
	resp, err := http.Get(funcs_url)
	panicIf(err)
	defer resp.Body.Close()
	assert(resp.StatusCode == 200, "http get status code on /v1/functions")

	var found bool = false
	for _, b := range resp.Header["Content-Type"] {
		if b == "application/json; charset=utf-8" {
			found = true
		}
	}
	assert(found, "incorrect response content type")

	err = g.client.FunctionDelete(&fission.Metadata{Name: "foo"})
	panicIf(err)
	err = g.client.FunctionDelete(&fission.Metadata{Name: "bar"})
	panicIf(err)
}

func TestFunctionVersionApi(t *testing.T) {
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

	testFunc.Code = "code1"
	m, err := g.client.FunctionCreate(testFunc)
	panicIf(err)
	uid1 := m.Uid

	testFunc.Code = "code2"
	m, err = g.client.FunctionUpdate(testFunc)
	panicIf(err)
	uid2 := m.Uid

	err = g.client.FunctionDelete(&fission.Metadata{Name: "foo", Uid: uid1})
	panicIf(err)

	f, err := g.client.FunctionGet(&fission.Metadata{Name: "foo"})
	panicIf(err)
	assert(f.Metadata.Uid == uid2, "deleted version1, but version2 does not exist")

	testFunc.Code = "code3"
	m, err = g.client.FunctionUpdate(testFunc)
	panicIf(err)
	uid3 := m.Uid

	err = g.client.FunctionDelete(&fission.Metadata{Name: "foo", Uid: uid3})
	panicIf(err)

	f, err = g.client.FunctionGet(&fission.Metadata{Name: "foo"})
	panicIf(err)
	assert(f.Metadata.Uid == uid2, "deleted version3, but version2 does not exist")

	testFunc.Code = "code4"
	m, err = g.client.FunctionUpdate(testFunc)
	panicIf(err)

	err = g.client.FunctionDelete(&fission.Metadata{Name: "foo"})
	panicIf(err)

	funcs, err := g.client.FunctionList()
	panicIf(err)
	assert(len(funcs) == 0,
		"created one function with two versions(2 and 4), delete without uid but cannot delete them all")
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
	_, err := g.client.HTTPTriggerGet(&fission.Metadata{Name: "foo"})
	assertNotFoundFails(err, "trigger")

	m, err := g.client.HTTPTriggerCreate(testTrigger)
	panicIf(err)
	defer g.client.HTTPTriggerDelete(m)

	_, err = g.client.HTTPTriggerCreate(testTrigger)
	assertNameReuseFails(err, "trigger")

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
	assert(err != nil, "duplicate trigger should not be allowed")

	testTrigger.UrlPattern = "/hi2"
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
	_, err := g.client.EnvironmentGet(&fission.Metadata{Name: "foo"})
	assertNotFoundFails(err, "environment")

	m, err := g.client.EnvironmentCreate(testEnv)
	panicIf(err)
	defer g.client.EnvironmentDelete(m)

	_, err = g.client.EnvironmentCreate(testEnv)
	assertNameReuseFails(err, "environment")

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
	_, err := g.client.WatchGet(&fission.Metadata{Name: "foo"})
	assertNotFoundFails(err, "watch")

	m, err := g.client.WatchCreate(testWatch)
	panicIf(err)
	defer g.client.WatchDelete(m)

	_, err = g.client.WatchCreate(testWatch)
	assertNameReuseFails(err, "watch")

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

	var found bool = false
	for _, b := range resp.Header["Content-Type"] {
		if b == "application/json; charset=utf-8" {
			found = true
		}
	}
	assert(found, "incorrect response content type")

	_, err = ioutil.ReadAll(resp.Body)
	panicIf(err)

	os.Exit(m.Run())
}
