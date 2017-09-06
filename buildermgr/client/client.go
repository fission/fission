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
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/fission/fission"
	"github.com/fission/fission/buildermgr"
)

type (
	Client struct {
		url string
	}
)

func MakeClient(builderUrl string) *Client {
	return &Client{
		url: strings.TrimSuffix(builderUrl, "/") + "/v1",
	}
}

func (c *Client) PackageBuild(req *buildermgr.BuildRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.doHttpReq("POST", c.url+"/build", body)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) EnvBuilderCreate(req *buildermgr.EnvBuilderRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.doHttpReq("POST", c.url+"/builder", body)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) EnvBuilderUpdate(req *buildermgr.EnvBuilderRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.doHttpReq("PUT", c.url+"/builder", body)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) EnvBuilderDelete(req *buildermgr.EnvBuilderRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.doHttpReq("DELETE", c.url+"/builder", body)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) doHttpReq(method string, url string, body []byte) ([]byte, error) {
	client := http.Client{}
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	return c.handleResponse(resp)
}

func (c *Client) handleResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, fission.MakeErrorFromHTTP(resp)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	return body, err
}
