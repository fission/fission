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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/fission/fission"
)

type (
	Client struct {
		Url string
	}
)

func MakeClient(serverUrl string) *Client {
	return &Client{Url: strings.TrimSuffix(serverUrl, "/")}
}

func (c *Client) delete(relativeUrl string) error {
	req, err := http.NewRequest("DELETE", c.url(relativeUrl), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return errors.New("Delete failed")
		} else {
			return errors.New("Delete failed: " + string(body))
		}
	}

	return nil
}

func (c *Client) put(relativeUrl string, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("PUT", c.url(relativeUrl), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-type", contentType)
	return http.DefaultClient.Do(req)
}

func (c *Client) url(relativeUrl string) string {
	return c.Url + "/v1/" + relativeUrl
}

func (c *Client) handleResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, fission.MakeErrorFromHTTP(resp)
	}
	body, err := ioutil.ReadAll(resp.Body)
	return body, err
}

func (c *Client) handleCreateResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 201 {
		return nil, fission.MakeErrorFromHTTP(resp)
	}
	body, err := ioutil.ReadAll(resp.Body)
	return body, err
}

func (c *Client) FunctionCreate(f *fission.Function) (*fission.Metadata, error) {
	orig := f.Code
	f.Code = base64.StdEncoding.EncodeToString([]byte(f.Code))
	defer func() { f.Code = orig }()

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("functions"), "application/json", bytes.NewReader(reqbody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) FunctionGet(m *fission.Metadata) (*fission.Function, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		return nil, err
	}

	dec, err := base64.StdEncoding.DecodeString(f.Code)
	if err != nil {
		return nil, err
	}
	f.Code = string(dec)

	return &f, nil
}

func (c *Client) FunctionGetRaw(m *fission.Metadata) ([]byte, error) {
	relativeUrl := fmt.Sprintf("functions/%v?raw=1", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("&uid=%v", m.Uid)
	}

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.handleResponse(resp)
}

func (c *Client) FunctionUpdate(f *fission.Function) (*fission.Metadata, error) {
	orig := f.Code
	f.Code = base64.StdEncoding.EncodeToString([]byte(f.Code))
	defer func() { f.Code = orig }()

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("functions/%v", f.Metadata.Name)

	resp, err := c.put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) FunctionDelete(m *fission.Metadata) error {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}
	err := c.delete(relativeUrl)
	return err
}

func (c *Client) FunctionList() ([]fission.Function, error) {
	resp, err := http.Get(c.url("functions"))
	if err != nil {
		return nil, err
	}

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	funcs := make([]fission.Function, 0)
	err = json.Unmarshal(body, &funcs)
	if err != nil {
		return nil, err
	}

	return funcs, nil
}

func (c *Client) HTTPTriggerCreate(t *fission.HTTPTrigger) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("triggers/http"), "application/json", bytes.NewReader(reqbody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) HTTPTriggerGet(m *fission.Metadata) (*fission.HTTPTrigger, error) {
	relativeUrl := fmt.Sprintf("triggers/http/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var t fission.HTTPTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

func (c *Client) HTTPTriggerUpdate(t *fission.HTTPTrigger) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("triggers/http/%v", t.Metadata.Name)

	resp, err := c.put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) HTTPTriggerDelete(m *fission.Metadata) error {
	relativeUrl := fmt.Sprintf("triggers/http/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}
	err := c.delete(relativeUrl)
	return err
}

func (c *Client) HTTPTriggerList() ([]fission.HTTPTrigger, error) {
	resp, err := http.Get(c.url("triggers/http"))
	if err != nil {
		return nil, err
	}

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	triggers := make([]fission.HTTPTrigger, 0)
	err = json.Unmarshal(body, &triggers)
	if err != nil {
		return nil, err
	}

	return triggers, nil
}

func (c *Client) EnvironmentCreate(env *fission.Environment) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("environments"), "application/json", bytes.NewReader(reqbody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) EnvironmentGet(m *fission.Metadata) (*fission.Environment, error) {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var env fission.Environment
	err = json.Unmarshal(body, &env)
	if err != nil {
		return nil, err
	}

	return &env, nil
}

func (c *Client) EnvironmentUpdate(env *fission.Environment) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("environments/%v", env.Metadata.Name)

	resp, err := c.put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) EnvironmentDelete(m *fission.Metadata) error {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}
	err := c.delete(relativeUrl)
	return err
}

func (c *Client) EnvironmentList() ([]fission.Environment, error) {
	resp, err := http.Get(c.url("environments"))
	if err != nil {
		return nil, err
	}

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	envs := make([]fission.Environment, 0)
	err = json.Unmarshal(body, &envs)
	if err != nil {
		return nil, err
	}

	return envs, nil
}

func (c *Client) WatchCreate(w *fission.Watch) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("watches"), "application/json", bytes.NewReader(reqbody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m fission.Metadata
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) WatchGet(m *fission.Metadata) (*fission.Watch, error) {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var w fission.Watch
	err = json.Unmarshal(body, &w)
	if err != nil {
		return nil, err
	}

	return &w, nil
}

func (c *Client) WatchUpdate(w *fission.Watch) (*fission.Metadata, error) {
	return nil, fission.MakeError(fission.ErrorNotImplmented,
		"watch update not implemented")
}

func (c *Client) WatchDelete(m *fission.Metadata) error {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	if len(m.Uid) > 0 {
		relativeUrl += fmt.Sprintf("?uid=%v", m.Uid)
	}
	err := c.delete(relativeUrl)
	return err

}

func (c *Client) WatchList() ([]fission.Watch, error) {
	resp, err := http.Get(c.url("watches"))
	if err != nil {
		return nil, err
	}

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	watches := make([]fission.Watch, 0)
	err = json.Unmarshal(body, &watches)
	if err != nil {
		return nil, err
	}

	return watches, err
}
