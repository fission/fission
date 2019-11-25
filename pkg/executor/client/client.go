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
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/pkg/errors"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/zap"
	"golang.org/x/net/context/ctxhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type (
	Client struct {
		logger      *zap.Logger
		executorUrl string
		tappedByUrl map[string]TapServiceRequest
		requestChan chan TapServiceRequest
		httpClient  *http.Client
	}
	TapServiceRequest struct {
		FnMetadata     metav1.ObjectMeta
		FnExecutorType fv1.ExecutorType
		ServiceUrl     string
	}
)

func MakeClient(logger *zap.Logger, executorUrl string) *Client {
	c := &Client{
		logger:      logger.Named("executor_client"),
		executorUrl: strings.TrimSuffix(executorUrl, "/"),
		tappedByUrl: make(map[string]TapServiceRequest),
		requestChan: make(chan TapServiceRequest),
		httpClient: &http.Client{
			Transport: &ochttp.Transport{},
		},
	}
	go c.service()
	return c
}

func (c *Client) GetServiceForFunction(ctx context.Context, metadata *metav1.ObjectMeta) (string, error) {
	executorUrl := c.executorUrl + "/v2/getServiceForFunction"

	body, err := json.Marshal(metadata)
	if err != nil {
		return "", errors.Wrap(err, "could not marshal request body for getting service for function")
	}

	resp, err := ctxhttp.Post(ctx, c.httpClient, executorUrl, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", errors.Wrap(err, "error posting to getting service for function")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", ferror.MakeErrorFromHTTP(resp)
	}

	svcName, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "error reading response body from getting service for function")
	}

	return string(svcName), nil
}

func (c *Client) service() {
	ticker := time.NewTicker(time.Second * 5)
	for {
		select {
		case svcReq := <-c.requestChan:
			c.tappedByUrl[svcReq.ServiceUrl] = svcReq
		case <-ticker.C:
			if len(c.tappedByUrl) == 0 {
				continue
			}

			urls := c.tappedByUrl
			c.tappedByUrl = make(map[string]TapServiceRequest)

			go func() {
				svcReqs := []TapServiceRequest{}
				for _, req := range urls {
					svcReqs = append(svcReqs, req)
				}
				c.logger.Debug("tapped services in batch", zap.Int("service_count", len(urls)))

				err := c._tapService(svcReqs)
				if err != nil {
					c.logger.Error("error tapping function service address", zap.Error(err))
				}
			}()
		}
	}
}

func (c *Client) TapService(fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceUrl *url.URL) {
	c.requestChan <- TapServiceRequest{
		FnMetadata: metav1.ObjectMeta{
			Name:            fnMeta.Name,
			Namespace:       fnMeta.Namespace,
			ResourceVersion: fnMeta.ResourceVersion,
			UID:             fnMeta.UID,
		},
		FnExecutorType: executorType,
		// service url is for executor to know which
		// pod/service is currently used to serve user function.
		ServiceUrl: serviceUrl.String(),
	}
}

func (c *Client) _tapService(tapSvcReqs []TapServiceRequest) error {
	executorUrl := c.executorUrl + "/v2/tapServices"

	body, err := json.Marshal(tapSvcReqs)
	if err != nil {
		return err
	}

	resp, err := http.Post(executorUrl, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ferror.MakeErrorFromHTTP(resp)
	}

	return nil
}
