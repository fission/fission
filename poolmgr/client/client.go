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
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fission/fission"
)

const (
	TOUCH int = iota
	SYNC
)

type Client struct {
	poolmgrUrl  string
	tappedByUrl map[string]bool
	requestChan chan clientRequest
	tapUrlChan  chan string
}

type clientRequest struct {
	requestType int
	serviceUrl  string
}

func MakeClient(poolmgrUrl string) *Client {
	c := &Client{
		poolmgrUrl:  strings.TrimSuffix(poolmgrUrl, "/"),
		tappedByUrl: make(map[string]bool),
		requestChan: make(chan clientRequest, 100),
		tapUrlChan:  make(chan string, 100),
	}
	go c.tapWorker()
	go c.timer()
	go c.tapService()
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

func (c *Client) tapService() {
	for {
		req := <-c.requestChan
		switch req.requestType {
		case TOUCH:
			c.tappedByUrl[req.serviceUrl] = true
		case SYNC:
			for u := range c.tappedByUrl {
				c.tapUrlChan <- u
			}
			if len(c.tappedByUrl) > 0 {
				log.Printf("Tapped %v services in batch", len(c.tappedByUrl))
			}
			c.tappedByUrl = make(map[string]bool)
		}
	}
}

func (c *Client) tapWorker() {
	for {
		u := <-c.tapUrlChan
		c._tapService(u)
	}
}

func (c *Client) timer() {
	for {
		c.requestChan <- clientRequest{requestType: SYNC}
		time.Sleep(5 * time.Second)
	}
}

func (c *Client) TapService(serviceUrl *url.URL) {
	c.requestChan <- clientRequest{
		requestType: TOUCH,
		serviceUrl:  serviceUrl.String(),
	}
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
