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
	"io"
	"net/http"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client/rest"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/info"
)

// TODO: we should remove this interface, having this for now is for backward compatibility.
type (
	MiscGetter interface {
		Misc() MiscInterface
	}

	MiscInterface interface {
		SecretExists(m *metav1.ObjectMeta) error
		ConfigMapExists(m *metav1.ObjectMeta) error
		GetSvcURL(label string) (string, error)
		ServerInfo() (*info.ServerInfo, error)
		PodLogs(m *metav1.ObjectMeta) (io.ReadCloser, int, error)
	}

	Misc struct {
		client rest.Interface
	}
)

func newMiscClient(c *V1) MiscInterface {
	return &Misc{client: c.restClient}
}

func (c *Misc) SecretExists(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("secrets/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *Misc) ConfigMapExists(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("configmaps/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *Misc) GetSvcURL(label string) (string, error) {
	resp, err := c.client.Proxy(http.MethodGet, "svcname?"+label, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.Errorf("failed to find service for given label: %v", label)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	storageSvc := string(body)

	return storageSvc, err
}

func (c *Misc) ServerInfo() (*info.ServerInfo, error) {
	resp, err := c.client.ServerInfo()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	info := &info.ServerInfo{}
	err = json.Unmarshal(body, info)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (c *Misc) PodLogs(m *metav1.ObjectMeta) (io.ReadCloser, int, error) {
	uri := fmt.Sprintf("logs/%s", m.Name)
	console.Verbose(2, fmt.Sprintf("Try to get pod logs from controller '%v'", uri))
	resp, err := c.client.Proxy(http.MethodPost, uri, nil)
	if err != nil {
		return nil, 0, errors.Wrap(err, "error executing get logs request")
	}
	return resp.Body, resp.StatusCode, nil
}
