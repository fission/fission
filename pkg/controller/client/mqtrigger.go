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
)

func (c *Client) MessageQueueTriggerCreate(t *fv1.MessageQueueTrigger) (*metav1.ObjectMeta, error) {
	err := t.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("MessageQueueTrigger", err)
	}

	reqbody, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}

	resp, err := c.create("triggers/messagequeue", "application/json", reqbody)
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

func (c *Client) MessageQueueTriggerGet(m *metav1.ObjectMeta) (*fv1.MessageQueueTrigger, error) {
	relativeUrl := fmt.Sprintf("triggers/messagequeue/%v", m.Name)
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

	var t fv1.MessageQueueTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

func (c *Client) MessageQueueTriggerUpdate(mqTrigger *fv1.MessageQueueTrigger) (*metav1.ObjectMeta, error) {
	err := mqTrigger.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("MessageQueueTrigger", err)
	}

	reqbody, err := json.Marshal(mqTrigger)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("triggers/messagequeue/%v", mqTrigger.Metadata.Name)

	resp, err := c.put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
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

func (c *Client) MessageQueueTriggerDelete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("triggers/messagequeue/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.delete(relativeUrl)
}

func (c *Client) MessageQueueTriggerList(mqType string, ns string) ([]fv1.MessageQueueTrigger, error) {
	relativeUrl := "triggers/messagequeue"
	if len(mqType) > 0 {
		// TODO remove this, replace with field selector
		relativeUrl += fmt.Sprintf("?mqtype=%v&namespace=%v", mqType, ns)
	}

	resp, err := c.get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	triggers := make([]fv1.MessageQueueTrigger, 0)
	err = json.Unmarshal(body, &triggers)
	if err != nil {
		return nil, err
	}

	return triggers, nil
}
