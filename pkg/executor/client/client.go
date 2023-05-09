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
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	pb "github.com/fission/fission/pkg/executor/proto"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type (
	// Client is wrapper on a HTTP client.
	Client struct {
		logger      *zap.Logger
		executorURL string
		tappedByURL map[string]TapServiceRequest
		requestChan chan TapServiceRequest
		httpClient  *retryablehttp.Client
		grpcClient  pb.ExecutorClient
	}

	// TapServiceRequest represents
	TapServiceRequest struct {
		FnMetadata     metav1.ObjectMeta
		FnExecutorType fv1.ExecutorType
		ServiceURL     string
	}
)

// MakeClient initializes and returns a Client instance.
func MakeClient(logger *zap.Logger, executorURL string) *Client {
	hc := retryablehttp.NewClient()
	hc.HTTPClient.Transport = otelhttp.NewTransport(hc.HTTPClient.Transport)

	addr := "executor.fission:50051" // Hardcoded. To be removed.
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error(fmt.Sprintf("grpc client did not connect: %v", err))
	}

	ec := pb.NewExecutorClient(conn)

	c := &Client{
		logger:      logger.Named("executor_client"),
		executorURL: strings.TrimSuffix(executorURL, "/"),
		tappedByURL: make(map[string]TapServiceRequest),
		requestChan: make(chan TapServiceRequest, 100),
		httpClient:  hc,
		grpcClient:  ec,
	}
	go c.service()
	return c
}

// GetServiceForFunction returns the service name for a given function.
func (c *Client) GetServiceForFunction(ctx context.Context, fn *fv1.Function) (string, error) {
	executorURL := c.executorURL + "/v2/getServiceForFunction"

	body, err := json.Marshal(fn)
	if err != nil {
		return "", errors.Wrap(err, "could not marshal request body for getting service for function")
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", executorURL, bytes.NewReader(body))
	if err != nil {
		return "", errors.Wrap(err, "could not create request for getting service for function")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "error posting to getting service for function")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", ferror.MakeErrorFromHTTP(resp)
	}

	svcName, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "error reading response body from getting service for function")
	}

	return string(svcName), nil
}

// UnTapService sends a request to /v2/unTapService.
func (c *Client) UnTapService(ctx context.Context, fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL *url.URL) error {
	url := c.executorURL + "/v2/unTapService"
	tapSvc := TapServiceRequest{
		FnMetadata:     fnMeta,
		FnExecutorType: executorType,
		ServiceURL:     strings.TrimPrefix(serviceURL.String(), "http://"),
	}

	body, err := json.Marshal(tapSvc)
	if err != nil {
		return errors.Wrap(err, "could not marshal request body for getting service for function")
	}
	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(err, "could not create request for untap service for function")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "error posting to getting service for function")
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ferror.MakeErrorFromHTTP(resp)
	}

	return nil
}

func (c *Client) service() {
	ticker := time.NewTicker(time.Second * 5)
	for {
		select {
		case svcReq := <-c.requestChan:
			c.tappedByURL[svcReq.ServiceURL] = svcReq
		case <-ticker.C:
			if len(c.tappedByURL) == 0 {
				continue
			}

			urls := c.tappedByURL
			c.tappedByURL = make(map[string]TapServiceRequest)

			go func() {
				svcReqs := []TapServiceRequest{}
				for _, req := range urls {
					svcReqs = append(svcReqs, req)
				}
				c.logger.Debug("tapped services in batch", zap.Int("service_count", len(urls)))
				err := c._tapService(context.Background(), svcReqs)
				if err != nil {
					c.logger.Error("error tapping function service address", zap.Error(err))
				}
			}()
		}
	}
}

// TapService sends a TapServiceRequest over the request channel.
func (c *Client) TapService(fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL url.URL) {
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
		ServiceURL: serviceURL.String(),
	}
}

func (c *Client) _tapService(ctx context.Context, tapSvcReqs []TapServiceRequest) error {
	executorURL := c.executorURL + "/v2/tapServices"

	body, err := json.Marshal(tapSvcReqs)
	if err != nil {
		return err
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", executorURL, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(err, "could not create request for tap service request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ferror.MakeErrorFromHTTP(resp)
	}
	return nil
}

func (c *Client) GetServiceForFunctionGRPC(ctx context.Context, fn *fv1.Function) (string, error) {
	fmt.Println("SHUBHAM: CODE REACHED HERE")
	svc, err := c.grpcClient.GetServiceForFunction(ctx, fn)
	status, ok := status.FromError(err)
	if !ok {
		return "", errors.Wrap(status.Err(), status.Message())
	}
	fmt.Printf("SHUBHAM: %s\n", svc.GetUrl())
	return svc.GetUrl(), nil
}

// Client for testing purpose. Will be removed.
func (c *Client) CallUnaryEcho(ctx context.Context, message string) (string, error) {
	er := pb.EchoRequest{Message: message}
	r, err := c.grpcClient.UnaryEcho(ctx, &er)
	if err != nil {
		return "", err
	}
	s := "Echo: " + r.GetMessage()
	return s, nil
}
