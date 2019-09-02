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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/context/ctxhttp"

	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/info"
)

type (
	Client struct {
		Url string
	}
)

func MakeClient(serverUrl string) *Client {
	return &Client{Url: strings.TrimSuffix(serverUrl, "/")}
}

func (c *Client) delete(relativeUrl string) error {
	req, err := http.NewRequest("DELETE", c.url(relativeUrl), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return errors.New("Delete failed")
		} else {
			return errors.New("Delete failed: " + string(body))
		}
	}

	return nil
}

func (c *Client) put(relativeUrl string, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("PUT", c.url(relativeUrl), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-type", contentType)
	return http.DefaultClient.Do(req)
}

func (c *Client) url(relativeUrl string) string {
	return c.Url + "/v2/" + relativeUrl
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

func (c *Client) ServerInfo() (*info.ServerInfo, error) {
	url := fmt.Sprintf(c.Url)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := ctxhttp.Get(ctx, &http.Client{}, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
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
