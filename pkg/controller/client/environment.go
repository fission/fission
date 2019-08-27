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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/generator/encoder"
	v1generator "github.com/fission/fission/pkg/generator/v1"
)

func getEnvEncodingPayload(env *fv1.Environment) ([]byte, error) {
	generator, err := v1generator.CreateEnvironmentGeneratorFromObj(env)
	if err != nil {
		return nil, err
	}
	return generator.StructuredGenerate(encoder.DefaultJSONEncoder())
}

func (c *Client) EnvironmentCreate(env *fv1.Environment) (*metav1.ObjectMeta, error) {
	data, err := getEnvEncodingPayload(env)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(c.url("environments"), "application/json", bytes.NewReader(data))
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

func (c *Client) EnvironmentGet(m *metav1.ObjectMeta) (*fv1.Environment, error) {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
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

	var env fv1.Environment
	err = json.Unmarshal(body, &env)
	if err != nil {
		return nil, err
	}

	return &env, nil
}

func (c *Client) EnvironmentUpdate(env *fv1.Environment) (*metav1.ObjectMeta, error) {
	data, err := getEnvEncodingPayload(env)
	if err != nil {
		return nil, err
	}

	relativeUrl := fmt.Sprintf("environments/%v", env.Metadata.Name)

	resp, err := c.put(relativeUrl, "application/json", data)
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

func (c *Client) EnvironmentDelete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	return c.delete(relativeUrl)
}

func (c *Client) EnvironmentList(ns string) ([]fv1.Environment, error) {
	relativeUrl := fmt.Sprintf("environments?namespace=%v", ns)
	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	envs := make([]fv1.Environment, 0)
	err = json.Unmarshal(body, &envs)
	if err != nil {
		return nil, err
	}

	return envs, nil
}
