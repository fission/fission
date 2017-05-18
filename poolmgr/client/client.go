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
	"github.com/opentracing/opentracing-go"
	"io"
	"io/ioutil"
	"log"
	"net/url"
)

type Client struct {
	poolmgrUrl string
}

func MakeClient(poolmgrUrl string) *Client {
	return &Client{poolmgrUrl: strings.TrimSuffix(poolmgrUrl, "/")}
}

func doPostWithTracing(span opentracing.Span, url string, contentType string, reader io.Reader) (*http.Response, error) {
	req, _ := http.NewRequest("POST", url, reader)
	req.Header.Add("Content-Type", contentType)
	err := span.Tracer().Inject(span.Context(),
		opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(req.Header))
	if err != nil {
		log.Printf("Could not inject span context into header: %v", err)
	}

	return http.DefaultClient.Do(req)
}

func (c *Client) GetServiceForFunction(metadata *fission.Metadata, span opentracing.Span) (string, error) {
	url := c.poolmgrUrl + "/v1/getServiceForFunction"
	body, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}

	resp, err := doPostWithTracing(span, url, "application/json", bytes.NewReader(body))
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

func (c *Client) TapService(serviceUrl *url.URL, span opentracing.Span) error {
	url := c.poolmgrUrl + "/v1/tapService"

	serviceUrlStr := serviceUrl.String()

	resp, err := doPostWithTracing(span, url, "application/octet-stream", bytes.NewReader([]byte(serviceUrlStr)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fission.MakeErrorFromHTTP(resp)
	}
	return nil
}
