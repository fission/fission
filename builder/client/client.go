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

package client

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/fission/fission"
	builder "github.com/fission/fission/builder"
)

type (
	Client struct {
		url string
	}
)

func MakeClient(builderUrl string) *Client {
	return &Client{
		url: strings.TrimSuffix(builderUrl, "/"),
	}
}

func (c *Client) Build(req *builder.PackageBuildRequest) (*builder.PackageBuildResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading resp body: %v", err)
		return nil, err
	}

	pkgBuildResp := builder.PackageBuildResponse{}
	err = json.Unmarshal([]byte(rBody), &pkgBuildResp)
	if err != nil {
		log.Printf("Error parsing resp body: %v", err)
		return nil, err
	}

	return &pkgBuildResp, fission.MakeErrorFromHTTP(resp)
}
