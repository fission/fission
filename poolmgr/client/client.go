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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
)

type Client struct {
	poolmgrUrl  string
	tappedByUrl map[string]bool
	requestChan chan string
}

func MakeClient(poolmgrUrl string) *Client {
	c := &Client{
		poolmgrUrl:  strings.TrimSuffix(poolmgrUrl, "/"),
		tappedByUrl: make(map[string]bool),
		requestChan: make(chan string),
	}
	go c.service()
	return c
}

func (c *Client) GetServiceForFunction(metadata *metav1.ObjectMeta) (string, error) {
	poolmgrUrl := c.poolmgrUrl + "/v2/getServiceForFunction"

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

func (c *Client) service() {
	ticker := time.NewTicker(time.Second * 5)
	for {
		select {
		case serviceUrl := <-c.requestChan:
			c.tappedByUrl[serviceUrl] = true
		case <-ticker.C:
			urls := c.tappedByUrl
			c.tappedByUrl = make(map[string]bool)
			if len(urls) > 0 {
				go func() {
					for u := range c.tappedByUrl {
						c._tapService(u)
					}
					log.Printf("Tapped %v services in batch", len(urls))
				}()
				log.Printf("Tapped %v services in batch", len(urls))
			}
		}
	}
}

func (c *Client) TapService(serviceUrl *url.URL) {
	c.requestChan <- serviceUrl.String()
}

func (c *Client) _tapService(serviceUrlStr string) error {
	poolmgrUrl := c.poolmgrUrl + "/v2/tapService"

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
