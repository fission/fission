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
	"log"
	"os"
	"testing"

	"github.com/coreos/etcd/client"
	"golang.org/x/net/context"
)

type TestResource struct {
	A string
	B int
}

func (tr TestResource) Key() string {
	return tr.A
}

func panicIf(err error) {
	if err != nil {
		log.Panicf("err: %v", err)
	}
}

func assert(b bool, msg string) {
	if !b {
		log.Panic("assertion failed: " + msg)
	}
}

func getTestResourceStore() (*FileStore, client.KeysAPI, *ResourceStore) {
	// make a tmp dir
	dir, err := ioutil.TempDir("", "testFileStore")
	panicIf(err)
	fs := MakeFileStore(dir)

	rs, err := MakeResourceStore(fs, []string{"http://localhost:2379"})
	panicIf(err)

	return fs, rs.KeysAPI, rs
}

func TestResourceStore(t *testing.T) {
	fs, ks, rs := getTestResourceStore()
	defer os.RemoveAll(fs.root)

	s := JsonSerializer{}

	tr := TestResource{A: "hello", B: 1}

	// Delete the key first, in case of a panic'd previous test run; ignore errors
	_ = rs.delete("TestResource", tr.Key())
	ks.Delete(context.Background(), "/TestResource", &client.DeleteOptions{Dir: true})

	// Verify getAll for empty db
	trs, err := rs.getAll("TestResource")
	panicIf(err)
	if len(trs) != 0 {
		log.Fatalf("Expected zero length slice, got %v", trs)
	}

	// Create
	err = rs.create(tr)
	panicIf(err)
	defer ks.Delete(context.Background(), "/TestResource", &client.DeleteOptions{Dir: true})
	defer rs.delete("TestResource", tr.Key())

	// Etcd key /TestResource/hello should exist
	_, err = ks.Get(context.Background(), "TestResource/hello", nil)
	panicIf(err)

	// Read
	tr1 := TestResource{}
	err = rs.read(tr.Key(), &tr1)
	panicIf(err)
	assert(tr1 == tr, "retrieved value must equal created value")

	// Update and Read
	tr.B += 1
	err = rs.update(tr)
	panicIf(err)
	err = rs.read(tr.Key(), &tr1)
	panicIf(err)
	assert(tr1 == tr, "retrieved value must equal updated value")

	// Get list
	results, err := rs.getAll("TestResource")
	panicIf(err)
	res := make([]TestResource, 0, 0)
	for _, r := range results {
		tmp := TestResource{}
		err = s.deserialize([]byte(r), &tmp)
		panicIf(err)
		res = append(res, tmp)
	}
	assert(res[0] == tr, "value from retrieved list must equal updated value")

	// file tests
	fileKey := "ResourceStoreTest"
	fileContents1 := []byte("hello")
	fileContents2 := []byte("world")
	key, uid1, err := rs.writeFile(fileKey, fileContents1)
	panicIf(err)
	defer rs.deleteFile(fileKey, uid1)
	log.Printf("key = %v, uid = %v", key, uid1)

	// read latest
	contents, err := rs.readFile(fileKey, nil)
	panicIf(err)
	assert(string(contents) == string(fileContents1), "retrieved file contents must match written value")

	// update-- same key new contents
	_, uid2, err := rs.writeFile(fileKey, fileContents2)
	panicIf(err)
	defer rs.deleteFile(fileKey, uid2)

	// read latest
	contents, err = rs.readFile(fileKey, nil)
	panicIf(err)
	assert(string(contents) == string(fileContents2), "retrieved file contents must match updated value")

	// read by uid
	// 1
	contents, err = rs.readFile(fileKey, &uid1)
	panicIf(err)
	assert(string(contents) == string(fileContents1), "retrieved file contents must match updated value")

	// 2
	contents, err = rs.readFile(fileKey, &uid2)
	panicIf(err)
	assert(string(contents) == string(fileContents2), "retrieved file contents must match updated value")

}
