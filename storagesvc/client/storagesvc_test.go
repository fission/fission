/*
Copyright 2017 The Fission Authors.

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

package client

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/dchest/uniuri"

	"github.com/fission/fission/storagesvc"
)

func panicIf(err error) {
	if err != nil {
		log.Panicf("Error: %v", err)
	}
}

func MakeTestFile(size int) *os.File {
	f, err := ioutil.TempFile("", "storagesvc_test_")
	panicIf(err)

	_, err = f.Write(bytes.Repeat([]byte("."), size))
	panicIf(err)

	return f
}

func TestStorageService(t *testing.T) {
	testId := uniuri.NewLen(8)
	port := 8080

	log.Printf("starting storage svc")
	_ = storagesvc.RunStorageService(
		storagesvc.StorageTypeLocal, "/tmp", testId, port)

	client := MakeClient(fmt.Sprintf("http://localhost:%v/", port))

	// generate a test file
	tmpfile := MakeTestFile(10 * 1024 * 1024)
	defer os.Remove(tmpfile.Name())

	// store it
	metadata := make(map[string]string)
	fileId, err := client.Upload(tmpfile.Name(), &metadata)
	panicIf(err)

	// make a temp file for verification
	retrievedfile, err := ioutil.TempFile("", "storagesvc_verify_")
	panicIf(err)
	os.Remove(retrievedfile.Name())

	// retrieve uploaded file
	err = client.Download(fileId, retrievedfile.Name())
	panicIf(err)
	defer os.Remove(retrievedfile.Name())

	// compare contents
	contents1, err := ioutil.ReadFile(tmpfile.Name())
	panicIf(err)
	contents2, err := ioutil.ReadFile(retrievedfile.Name())
	panicIf(err)
	if bytes.Compare(contents1, contents2) != 0 {
		log.Panicf("Contents don't match")
	}

	// delete uploaded file
	err = client.Delete(fileId)
	panicIf(err)

	// cleanup /tmp
	os.RemoveAll(fmt.Sprintf("/tmp/", testId))
}
