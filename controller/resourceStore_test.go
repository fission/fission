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
	//	"github.com/coreos/etcd/client"
	"golang.org/x/net/context"
	"io/ioutil"
	"log"
	"os"
	"testing"
)

type TestResource struct {
	A string
	B int
}

func (tr TestResource) key() string {
	return tr.A
}

func check(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func assert(b bool, msg string) {
	if !b {
		log.Panic("assertion failed: " + msg)
	}
}

func TestResourceStore(t *testing.T) {
	// make a tmp dir
	dir, err := ioutil.TempDir("", "testFileStore")
	check(err)
	defer os.RemoveAll(dir)

	fs := makeFileStore(dir)

	// assume etcd is running, connect to it
	ks := getEtcdKeyAPI([]string{"http://localhost:2379"})
	s := JsonSerializer{}
	rs := makeResourceStore(fs, ks, s)

	tr := TestResource{A: "hello", B: 1}

	// Delete the key first, in case of a panic'd previous test run; ignore errors
	_ = rs.delete("TestResource", tr.key())

	// Create
	err = rs.create(tr)
	check(err)
	defer rs.delete("TestResource", tr.key())

	// Etcd key /TestResource/hello should exist
	_, err = ks.Get(context.Background(), "TestResource/hello", nil)
	check(err)

	// Read
	tr1 := TestResource{}
	err = rs.read(tr.key(), &tr1)
	check(err)
	assert(tr1 == tr, "retrieved value must equal created value")

	// Update and Read
	tr.B += 1
	err = rs.update(tr)
	check(err)
	err = rs.read(tr.key(), &tr1)
	check(err)
	assert(tr1 == tr, "retrieved value must equal updated value")

	// Get list
	results, err := rs.getAll("TestResource")
	check(err)
	res := make([]TestResource, 0, 0)
	for _, r := range results {
		tmp := TestResource{}
		err = s.deserialize([]byte(r), &tmp)
		check(err)
		res = append(res, tmp)
	}
	assert(res[0] == tr, "value from retrieved list must equal updated value")

	// file tests
	fileKey := "foo"
	fileContents1 := []byte("hello")
	fileContents2 := []byte("world")
	key, uid1, err := rs.writeFile(fileKey, fileContents1)
	check(err)
	defer rs.deleteFile(fileKey, uid1)
	log.Printf("key = %v, uid = %v", key, uid1)

	// read latest
	contents, err := rs.readFile(fileKey, nil)
	check(err)
	assert(string(contents) == string(fileContents1), "retrieved file contents must match written value")

	// update-- same key new contents
	_, uid2, err := rs.writeFile(fileKey, fileContents2)
	check(err)
	defer rs.deleteFile(fileKey, uid2)

	// read latest
	contents, err = rs.readFile(fileKey, nil)
	check(err)
	assert(string(contents) == string(fileContents2), "retrieved file contents must match updated value")

	// read by uid
	// 1
	contents, err = rs.readFile(fileKey, &uid1)
	check(err)
	assert(string(contents) == string(fileContents1), "retrieved file contents must match updated value")

	// 2
	contents, err = rs.readFile(fileKey, &uid2)
	check(err)
	assert(string(contents) == string(fileContents2), "retrieved file contents must match updated value")
}
