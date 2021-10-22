/*
Copyright 2019 The Fission Authors.

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

package rest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/net/context/ctxhttp"
)

type (
	Interface interface {
		Create(relativeUrl string, contentType string, payload []byte) (*http.Response, error)
		Put(relativeUrl string, contentType string, payload []byte) (*http.Response, error)
		Get(relativeUrl string) (*http.Response, error)
		Delete(relativeUrl string) error
		Proxy(method string, relativeUrl string, payload []byte) (*http.Response, error)
		ServerInfo() (*http.Response, error)
		ServerURL() string
	}

	RESTClient struct {
		url string
	}
)

func NewRESTClient(serverUrl string) Interface {
	return &RESTClient{
		url: strings.TrimSuffix(serverUrl, "/"),
	}
}

func (c *RESTClient) Create(relativeUrl string, contentType string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(http.MethodPost, c.v2CrdUrl(relativeUrl), map[string]string{"Content-type": contentType}, reader)
}

func (c *RESTClient) Put(relativeUrl string, contentType string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(http.MethodPut, c.v2CrdUrl(relativeUrl), map[string]string{"Content-type": contentType}, reader)
}

func (c *RESTClient) Get(relativeUrl string) (*http.Response, error) {
	return c.sendRequest(http.MethodGet, c.v2CrdUrl(relativeUrl), nil, nil)
}

func (c *RESTClient) Delete(relativeUrl string) error {
	resp, err := c.sendRequest(http.MethodDelete, c.v2CrdUrl(relativeUrl), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "error deleting")
		} else {
			return errors.Errorf("failed to delete: %v", string(body))
		}
	}

	return nil
}

func (c *RESTClient) Proxy(method string, relativeUrl string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(method, c.proxyUrl(relativeUrl), nil, reader)
}

func (c *RESTClient) ServerInfo() (*http.Response, error) {
	return c.sendRequest(http.MethodGet, c.url, nil, nil)
}

func (c *RESTClient) ServerURL() string {
	return c.url
}

func (c *RESTClient) sendRequest(method string, relativeUrl string, headers map[string]string, reader io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, relativeUrl, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// TODO: accept context
	return ctxhttp.Do(context.Background(), &http.Client{}, req)
}

func (c *RESTClient) v2CrdUrl(relativeUrl string) string {
	return c.url + "/v2/" + strings.TrimPrefix(relativeUrl, "/")
}

func (c *RESTClient) proxyUrl(relativeUrl string) string {
	return c.url + "/proxy/" + strings.TrimPrefix(relativeUrl, "/")
}
