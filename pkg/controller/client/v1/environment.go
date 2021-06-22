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

	"github.com/fission/fission/pkg/controller/client/rest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generator/encoder"
	v1generator "github.com/fission/fission/pkg/generator/v1"
)

type (
	EnvironmentGetter interface {
		Environment() EnvironmentInterface
	}

	EnvironmentInterface interface {
		Create(env *fv1.Environment) (*metav1.ObjectMeta, error)
		Get(m *metav1.ObjectMeta) (*fv1.Environment, error)
		Update(env *fv1.Environment) (*metav1.ObjectMeta, error)
		Delete(m *metav1.ObjectMeta) error
		List(ns string) ([]fv1.Environment, error)
	}

	Environment struct {
		client rest.Interface
	}
)

func getEnvEncodingPayload(env *fv1.Environment) ([]byte, error) {
	generator, err := v1generator.CreateEnvironmentGeneratorFromObj(env)
	if err != nil {
		return nil, err
	}
	return generator.StructuredGenerate(encoder.DefaultJSONEncoder())
}

func newEnvironmentClient(c *V1) EnvironmentInterface {
	return &Environment{client: c.restClient}
}

func (c *Environment) Create(env *fv1.Environment) (*metav1.ObjectMeta, error) {
	data, err := getEnvEncodingPayload(env)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Create("environments", "application/json", data)
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

func (c *Environment) Get(m *metav1.ObjectMeta) (*fv1.Environment, error) {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
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

	var env fv1.Environment
	err = json.Unmarshal(body, &env)
	if err != nil {
		return nil, err
	}

	return &env, nil
}

func (c *Environment) Update(env *fv1.Environment) (*metav1.ObjectMeta, error) {
	data, err := getEnvEncodingPayload(env)
	if err != nil {
		return nil, err
	}

	relativeUrl := fmt.Sprintf("environments/%v", env.ObjectMeta.Name)

	resp, err := c.client.Put(relativeUrl, "application/json", data)
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

func (c *Environment) Delete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("environments/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.client.Delete(relativeUrl)
}

func (c *Environment) List(ns string) ([]fv1.Environment, error) {
	relativeUrl := fmt.Sprintf("environments?namespace=%v", ns)
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
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
