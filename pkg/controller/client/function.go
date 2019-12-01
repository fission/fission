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
	"io"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/console"
)

func (c *Client) FunctionCreate(f *fv1.Function) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Function", err)
	}

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	resp, err := c.create("functions", "application/json", reqbody)
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

func (c *Client) FunctionGet(m *metav1.ObjectMeta) (*fv1.Function, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
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

	var f fv1.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

func (c *Client) FunctionGetRawDeployment(m *metav1.ObjectMeta) ([]byte, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	relativeUrl += fmt.Sprintf("&deploymentraw=1")

	resp, err := c.get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return c.handleResponse(resp)
}

func (c *Client) FunctionUpdate(f *fv1.Function) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Function", err)
	}

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

	var m metav1.ObjectMeta
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) FunctionDelete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.delete(relativeUrl)
}

func (c *Client) FunctionList(functionNamespace string) ([]fv1.Function, error) {
	relativeUrl := fmt.Sprintf("functions?namespace=%v", functionNamespace)
	resp, err := c.get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := c.handleResponse(resp)
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

func (c *Client) FunctionPodLogs(m *metav1.ObjectMeta) (io.ReadCloser, int, error) {
	relativeUrl := fmt.Sprintf("functions/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	queryURL, err := url.Parse(c.Url)
	if err != nil {
		return nil, 0, errors.Wrapf(err, "error parsing the base URL '%v'", c.Url)
	}
	queryURL.Path = fmt.Sprintf("/proxy/logs/%s", m.Name)

	console.Verbose(2, fmt.Sprintf("Try to get pod logs from controller '%v'", queryURL.String()))

	req, err := http.NewRequest(http.MethodPost, queryURL.String(), nil)
	if err != nil {
		return nil, 0, errors.Wrap(err, "error creating logs request")
	}

	httpClient := http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, errors.Wrap(err, "error executing get logs request")
	}

	return resp.Body, resp.StatusCode, nil
}
