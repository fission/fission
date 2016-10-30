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
)

func TestFileStore(t *testing.T) {
	// tmp dir
	dir, err := ioutil.TempDir("", "testFileStore")
	if err != nil {
		t.Fatalf("error creating temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	log.Printf("temp dir at %v", dir)

	// file store
	fs := MakeFileStore(dir)

	_, err = fs.read("nonexistent")
	if err == nil {
		t.Fatalf("expected an error")
	}

	path := "fileStoreTest"
	contents := []byte("bar")
	err = fs.write(path, contents)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	observedContents, err := fs.read(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if string(observedContents) != string(contents) {
		t.Fatalf("contents don't match")
	}

	err = fs.delete(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
}
