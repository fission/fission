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

package router

import (
	"fmt"
	"testing"
	"time"

	"github.com/fission/fission"
)

func TestRouter(t *testing.T) {
	fmap := makeFunctionServiceMap(0)
	fn := &fission.Metadata{Name: "foo", Uid: "xxx"}

	testResponseString := "hi"
	testServiceUrl := createBackendService(testResponseString)

	fmap.assign(fn, testServiceUrl)

	triggers := makeHTTPTriggerSet(fmap, nil, nil)
	triggerUrl := "/foo"
	triggers.triggers = append(triggers.triggers, fission.HTTPTrigger{UrlPattern: triggerUrl, Function: *fn, Method: "GET"})

	port := 4242
	go serve(port, triggers)
	time.Sleep(100 * time.Millisecond)

	testUrl := fmt.Sprintf("http://localhost:%v%v", port, triggerUrl)
	testRequest(testUrl, testResponseString)
}
