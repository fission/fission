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
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	log "github.com/Sirupsen/logrus"

	"github.com/platform9/fission"
)

type (
	Client struct {
		Url string
	}
)

func New(serverUrl string) *Client {
	return &Client{Url: serverUrl}
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
	return c.Url + "/" + relativeUrl
}

func (c *Client) FunctionCreate(f *fission.Function) (*fission.Metadata, error) {
	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("functions"), "application/json", bytes.NewReader(reqbody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		log.WithFields(log.Fields{
			"function": f.Metadata.Name,
			"status":   resp.StatusCode,
		}).Error("Failed to create function")
		return nil, errors.New("failed to create function")
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

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

func (c *Client) FunctionUpdate(f *fission.Function) (*fission.Metadata, error) {
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

	body, err := ioutil.ReadAll(resp.Body)
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

	body, err := ioutil.ReadAll(resp.Body)
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

// func (c *Client) HTTPTriggerCreate(f *fission.Function) (string, error) {
// }
// func (c *Client) HTTPTriggerGet(m *fission.Metadata) (*fission.HTTPTrigger, error) {
// }
// func (c *Client) HTTPTriggerUpdate(f *fission.HTTPTrigger) (string, error) {
// }
// func (c *Client) HTTPTriggerDelete(m *fission.Metadata) error {
// }
// func (c *Client) HTTPTriggerList() ([]fission.HTTPTrigger, error) {
// }

// func (c *Client) EnvironmentCreate(f *fission.Environment) (string, error) {
// }
// func (c *Client) EnvironmentGet(m *fission.Metadata) (*fission.Environment, error) {
// }
// func (c *Client) EnvironmentUpdate(f *fission.Environment) (string, error) {
// }
// func (c *Client) EnvironmentDelete(m *fission.Metadata) error {
// }
// func (c *Client) EnvironmentList() ([]fission.Environment, error) {
// }
