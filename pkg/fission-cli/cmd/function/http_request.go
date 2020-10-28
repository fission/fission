
/*
Copyright 2020 The Fission Authors.

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

package main

import (
"io/ioutil"
"log"
"net/http"
"net/http/httputil"

)

func main() {
var body []byte
var response *http.Response
var request *http.Request

url := http://localhost/
request, err := http.NewRequest("GET", url, nil)
if err == nil {
request.Header.Add("Content-Type", "application/json")
debug(httputil.DumpRequestOut(request, true))
response, err = (&http.Client{}).Do(request)
}

if err == nil {
defer response.Body.Close()
debug(httputil.DumpResponse(response, true))
body, err = ioutil.ReadAll(response.Body)
}

if err == nil {
fmt.Printf("%s", body)
} else {
log.Fatalf("ERROR: %s", err)
}
}
