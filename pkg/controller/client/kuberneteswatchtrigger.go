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
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func (c *Client) WatchCreate(w *fv1.KubernetesWatchTrigger) (*metav1.ObjectMeta, error) {
	err := w.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("KubernetesWatchTrigger", err)
	}

	reqbody, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}

	resp, err := c.create("watches", "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m metav1.ObjectMeta
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Client) WatchGet(m *metav1.ObjectMeta) (*fv1.KubernetesWatchTrigger, error) {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := c.get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var w fv1.KubernetesWatchTrigger
	err = json.Unmarshal(body, &w)
	if err != nil {
		return nil, err
	}

	return &w, nil
}

func (c *Client) WatchUpdate(w *fv1.KubernetesWatchTrigger) (*metav1.ObjectMeta, error) {
	return nil, ferror.MakeError(ferror.ErrorNotImplmented, "watch update not implemented")
}

func (c *Client) WatchDelete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("watches/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.delete(relativeUrl)
}

func (c *Client) WatchList(ns string) ([]fv1.KubernetesWatchTrigger, error) {
	relativeUrl := fmt.Sprintf("watches?namespace=%v", ns)
	resp, err := c.get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	watches := make([]fv1.KubernetesWatchTrigger, 0)
	err = json.Unmarshal(body, &watches)
	if err != nil {
		return nil, err
	}

	return watches, err
}
