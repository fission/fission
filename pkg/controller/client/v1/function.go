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
	"net/url"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client/rest"
)

type (
	FunctionGetter interface {
		Function() FunctionInterface
	}

	FunctionInterface interface {
		Create(f *fv1.Function) (*metav1.ObjectMeta, error)
		Get(m *metav1.ObjectMeta) (*fv1.Function, error)
		GetRawDeployment(m *metav1.ObjectMeta) ([]byte, error)
		Update(f *fv1.Function) (*metav1.ObjectMeta, error)
		Delete(m *metav1.ObjectMeta) error
		List(functionNamespace string) ([]fv1.Function, error)
		ListPods(m *metav1.ObjectMeta) ([]apiv1.Pod, error)
	}

	Function struct {
		client rest.Interface
	}
)

func newFunctionClient(c *V1) FunctionInterface {
	return &Function{client: c.restClient}
}

func (c *Function) Create(f *fv1.Function) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Function", err)
	}

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Create("functions", "application/json", reqbody)
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

func (c *Function) Get(m *metav1.ObjectMeta) (*fv1.Function, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
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

	var f fv1.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

func (c *Function) GetRawDeployment(m *metav1.ObjectMeta) ([]byte, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	relativeUrl += "&deploymentraw=1"

	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return handleResponse(resp)
}

func (c *Function) Update(f *fv1.Function) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Function", err)
	}

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("functions/%v", f.ObjectMeta.Name)

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

func (c *Function) Delete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.client.Delete(relativeUrl)
}

func (c *Function) List(functionNamespace string) ([]fv1.Function, error) {
	relativeUrl := fmt.Sprintf("functions?namespace=%v", functionNamespace)
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	funcs := make([]fv1.Function, 0)
	err = json.Unmarshal(body, &funcs)
	if err != nil {
		return nil, err
	}

	return funcs, nil
}

func (c *Function) ListPods(m *metav1.ObjectMeta) ([]apiv1.Pod, error) {
	relativeUrl := fmt.Sprintf("functions/%s/pods", m.Name)

	values := url.Values{}
	if len(m.Labels) != 0 {
		if fns, ok := m.Labels[fv1.FUNCTION_NAMESPACE]; ok && len(fns) != 0 {
			values.Add(fv1.FUNCTION_NAMESPACE, fns)
		}
	}

	relativeUrl = fmt.Sprintf("%s?%s", relativeUrl, values.Encode())
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	pods := make([]apiv1.Pod, 0)
	err = json.Unmarshal(body, &pods)
	if err != nil {
		return nil, err
	}

	return pods, nil
}
