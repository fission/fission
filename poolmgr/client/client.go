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
	"net/http"
	"strings"

	"bytes"
	"encoding/json"
	"github.com/fission/fission"
	"io/ioutil"
	"net/url"
)

type Client struct {
	poolmgrUrl string
}

func MakeClient(poolmgrUrl string) *Client {
	return &Client{poolmgrUrl: strings.TrimSuffix(poolmgrUrl, "/")}
}

func (c *Client) GetServiceForFunction(metadata *fission.Metadata) (string, error) {
	url := c.poolmgrUrl + "/v1/getServiceForFunction"
	body, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fission.MakeErrorFromHTTP(resp)
	}

	svcName, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(svcName), nil
}

func (c *Client) TapService(serviceUrl *url.URL) error {
	url := c.poolmgrUrl + "/v1/tapService"

	serviceUrlStr := serviceUrl.String()

	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader([]byte(serviceUrlStr)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fission.MakeErrorFromHTTP(resp)
	}
	return nil
}
