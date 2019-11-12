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
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/pkg/errors"

	ferror "github.com/fission/fission/pkg/error"
)

var (
	DefaultRequestHeaders map[string]string
)

type (
	Client struct {
		Url     string
		Headers map[string]string
	}
)

func MakeClient(serverUrl string) *Client {
	return &Client{
		Url:     strings.TrimSuffix(serverUrl, "/"),
		Headers: DefaultRequestHeaders,
	}
}

func (c *Client) create(relativeUrl string, contentType string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(http.MethodPost, c.v2CrdUrl(relativeUrl), map[string]string{"Content-type": contentType}, reader)
}

func (c *Client) put(relativeUrl string, contentType string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(http.MethodPut, c.v2CrdUrl(relativeUrl), map[string]string{"Content-type": contentType}, reader)
}

func (c *Client) get(relativeUrl string) (*http.Response, error) {
	return c.sendRequest(http.MethodGet, c.v2CrdUrl(relativeUrl), nil, nil)
}

func (c *Client) delete(relativeUrl string) error {
	resp, err := c.sendRequest(http.MethodDelete, c.v2CrdUrl(relativeUrl), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "error deleting")
		} else {
			return errors.Errorf("failed to delete: %v", string(body))
		}
	}

	return nil
}

func (c *Client) proxy(method string, relativeUrl string, payload []byte) (*http.Response, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	return c.sendRequest(method, c.proxyUrl(relativeUrl), nil, reader)
}

func (c *Client) sendRequest(method string, relativeUrl string, headers map[string]string, reader io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, relativeUrl, reader)
	if err != nil {
		return nil, err
	}
	for _, hs := range []map[string]string{headers, c.Headers} {
		for k, v := range hs {
			req.Header.Set(k, v)
		}
	}
	return http.DefaultClient.Do(req)
}

func (c *Client) v2CrdUrl(relativeUrl string) string {
	return c.Url + "/v2/" + strings.TrimPrefix(relativeUrl, "/")
}

func (c *Client) proxyUrl(relativeUrl string) string {
	return c.Url + "/proxy/" + strings.TrimPrefix(relativeUrl, "/")
}

func (c *Client) handleResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, ferror.MakeErrorFromHTTP(resp)
	}
	body, err := ioutil.ReadAll(resp.Body)
	return body, err
}

func (c *Client) handleCreateResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 201 {
		return nil, ferror.MakeErrorFromHTTP(resp)
	}
	body, err := ioutil.ReadAll(resp.Body)
	return body, err
}
