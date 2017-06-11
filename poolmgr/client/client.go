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
	"net/http"
	"strings"

	"bytes"
	"encoding/json"
	"github.com/fission/fission"
	"io/ioutil"
	"log"
	"net/url"
	"sync"
	"time"
)

type Client struct {
	sync.RWMutex
	poolmgrUrl  string
	tappedByUrl map[string]bool
}

func MakeClient(poolmgrUrl string) *Client {
	c := &Client{
		poolmgrUrl:  strings.TrimSuffix(poolmgrUrl, "/"),
		tappedByUrl: make(map[string]bool),
	}
	go c.TapServiceEveryInterval()
	return c
}

func (c *Client) GetServiceForFunction(metadata *fission.Metadata) (string, error) {
	poolmgrUrl := c.poolmgrUrl + "/v1/getServiceForFunction"
	body, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(poolmgrUrl, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fission.MakeErrorFromHTTP(resp)
	}

	svcName, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(svcName), nil
}

func (c *Client) TapServiceEveryInterval() {
	for {
		c.Lock()
		tappedByUrl := c.tappedByUrl
		c.tappedByUrl = make(map[string]bool)
		c.Unlock()
		for u := range tappedByUrl {
			c._tapService(u)
		}
		if len(tappedByUrl) > 0 {
			log.Printf("Tapped %v services in batch", len(tappedByUrl))
		}
		time.Sleep(5 * time.Second)
	}
}

func (c *Client) TapService(serviceUrl *url.URL) {
	serviceUrlStr := serviceUrl.String()
	c.Lock()
	c.tappedByUrl[serviceUrlStr] = true
	c.Unlock()
}

func (c *Client) _tapService(serviceUrlStr string) error {
	poolmgrUrl := c.poolmgrUrl + "/v1/tapService"

	resp, err := http.Post(poolmgrUrl, "application/octet-stream", bytes.NewReader([]byte(serviceUrlStr)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fission.MakeErrorFromHTTP(resp)
	}
	return nil
}
