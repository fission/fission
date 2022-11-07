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

package v1

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client/rest"
)

type (
	TimeTriggerGetter interface {
		TimeTrigger() TimeTriggerInterface
	}

	TimeTriggerInterface interface {
		Create(t *fv1.TimeTrigger) (*metav1.ObjectMeta, error)
		Get(m *metav1.ObjectMeta) (*fv1.TimeTrigger, error)
		Update(t *fv1.TimeTrigger) (*metav1.ObjectMeta, error)
		Delete(m *metav1.ObjectMeta) error
		List(ns string) ([]fv1.TimeTrigger, error)
	}

	TimeTrigger struct {
		client rest.Interface
	}
)

func newTimeTriggerClient(c *V1) TimeTriggerInterface {
	return &TimeTrigger{client: c.restClient}
}

func (c *TimeTrigger) Create(t *fv1.TimeTrigger) (*metav1.ObjectMeta, error) {
	err := t.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("TimeTrigger", err)
	}

	reqbody, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Create("triggers/time", "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleCreateResponse(resp)
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

func (c *TimeTrigger) Get(m *metav1.ObjectMeta) (*fv1.TimeTrigger, error) {
	relativeUrl := fmt.Sprintf("triggers/time/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var t fv1.TimeTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

func (c *TimeTrigger) Update(t *fv1.TimeTrigger) (*metav1.ObjectMeta, error) {
	err := t.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("TimeTrigger", err)
	}

	reqbody, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("triggers/time/%v", t.ObjectMeta.Name)

	resp, err := c.client.Put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
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

func (c *TimeTrigger) Delete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("triggers/time/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.client.Delete(relativeUrl)
}

func (c *TimeTrigger) List(ns string) ([]fv1.TimeTrigger, error) {
	relativeUrl := fmt.Sprintf("triggers/time?namespace=%v", ns)
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	triggers := make([]fv1.TimeTrigger, 0)
	err = json.Unmarshal(body, &triggers)
	if err != nil {
		return nil, err
	}

	return triggers, nil
}
