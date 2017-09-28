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

package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/fission/fission/controller/client"
)

func fatal(msg string) {
	os.Stderr.WriteString(msg + "\n")
	os.Exit(1)
}

func warn(msg string) {
	os.Stderr.WriteString(msg + "\n")
}

func getClient(serverUrl string) *client.Client {

	if len(serverUrl) == 0 {
		fatal("Need --server or FISSION_URL set to your fission server.")
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return client.MakeClient(serverUrl)
}

func checkErr(err error, msg string) {
	if err != nil {
		fatal(fmt.Sprintf("Failed to %v: %v", msg, err))
	}
}

func httpRequest(method, url, body string, headers []string) *http.Response {
	if method == "" {
		method = "GET"
	}

	if method != http.MethodGet &&
		method != http.MethodDelete &&
		method != http.MethodPost &&
		method != http.MethodPut {
		// Fatal causes some stale data for the calling function, needs improvement
		fatal(fmt.Sprintf("Invalid HTTP method '%s'.", method))
	}

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	checkErr(err, "Error creating reuquest to function")

	for _, header := range headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			warn(fmt.Sprintf("Invalid header '%s'. Skipping...", headerKeyValue))
			continue
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fatal("Request failed.")
	}

	return resp
}
