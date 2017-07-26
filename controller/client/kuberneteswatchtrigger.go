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
	"fmt"
	"net/http"

	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
)

func (c *Client) WatchCreate(w *tpr.Kuberneteswatchtrigger) (*api.ObjectMeta, error) {
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

	var m api.ObjectMeta
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) WatchGet(m *api.ObjectMeta) (*tpr.Kuberneteswatchtrigger, error) {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var w tpr.Kuberneteswatchtrigger
	err = json.Unmarshal(body, &w)
	if err != nil {
		return nil, err
	}

	return &w, nil
}

func (c *Client) WatchUpdate(w *tpr.Kuberneteswatchtrigger) (*api.ObjectMeta, error) {
	return nil, fission.MakeError(fission.ErrorNotImplmented,
		"watch update not implemented")
}

func (c *Client) WatchDelete(m *api.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.delete(relativeUrl)
}

func (c *Client) WatchList() ([]tpr.Kuberneteswatchtrigger, error) {
	resp, err := http.Get(c.url("watches"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	watches := make([]tpr.Kuberneteswatchtrigger, 0)
	err = json.Unmarshal(body, &watches)
	if err != nil {
		return nil, err
	}

	return watches, err
}
