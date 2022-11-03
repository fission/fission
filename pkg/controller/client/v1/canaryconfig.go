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
	CanaryConfigGetter interface {
		CanaryConfig() CanaryConfigInterface
	}

	CanaryConfigInterface interface {
		Create(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error)
		Get(m *metav1.ObjectMeta) (*fv1.CanaryConfig, error)
		Update(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error)
		Delete(m *metav1.ObjectMeta) error
		List(ns string) ([]fv1.CanaryConfig, error)
	}

	CanaryConfig struct {
		client rest.Interface
	}
)

func newCanaryConfigClient(c *V1) CanaryConfigInterface {
	return &CanaryConfig{client: c.restClient}
}

func (c *CanaryConfig) Create(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error) {
	reqbody, err := json.Marshal(canaryConf)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Create("canaryconfigs", "application/json", reqbody)
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

func (c *CanaryConfig) Get(m *metav1.ObjectMeta) (*fv1.CanaryConfig, error) {
	relativeUrl := fmt.Sprintf("canaryconfigs/%v", m.Name)
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

	var canaryCfg fv1.CanaryConfig
	err = json.Unmarshal(body, &canaryCfg)
	if err != nil {
		return nil, err
	}

	return &canaryCfg, nil
}

func (c *CanaryConfig) Update(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error) {
	reqbody, err := json.Marshal(canaryConf)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("canaryconfigs/%v", canaryConf.ObjectMeta.Name)

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

func (c *CanaryConfig) Delete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("canaryconfigs/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.client.Delete(relativeUrl)
}

func (c *CanaryConfig) List(ns string) ([]fv1.CanaryConfig, error) {
	relativeUrl := fmt.Sprintf("canaryconfigs?namespace=%v", ns)
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	canaryCfgs := make([]fv1.CanaryConfig, 0)
	err = json.Unmarshal(body, &canaryCfgs)
	if err != nil {
		return nil, err
	}

	return canaryCfgs, nil
}
