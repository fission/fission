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
	"net/http"
	"strings"

	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"golang.org/x/net/context/ctxhttp"

	"github.com/fission/fission/pkg/builder"
	ferror "github.com/fission/fission/pkg/error"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	ClientInterface interface {
		Build(context.Context, *builder.PackageBuildRequest) (*builder.PackageBuildResponse, error)
		Clean(context.Context, string) error
	}

	client struct {
		logger     *zap.Logger
		url        string
		httpClient *retryablehttp.Client
	}
)

func MakeClient(logger *zap.Logger, builderUrl string) ClientInterface {
	hc := retryablehttp.NewClient()
	hc.ErrorHandler = retryablehttp.PassthroughErrorHandler
	hc.HTTPClient.Transport = otelhttp.NewTransport(hc.HTTPClient.Transport)
	return &client{
		logger:     logger.Named("builder_client"),
		url:        strings.TrimSuffix(builderUrl, "/"),
		httpClient: hc,
	}
}

func (c *client) getCleanUrl(srcPkgFilename string) string {
	return c.url + "/clean" + "?name=" + srcPkgFilename
}

func (c *client) Build(ctx context.Context, req *builder.PackageBuildRequest) (*builder.PackageBuildResponse, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, c.logger)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("error marshaling json: %w", err)
	}

	resp, err := ctxhttp.Post(ctx, c.httpClient.StandardClient(), c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	rBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("error reading resp body", zap.Error(err))
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	pkgBuildResp := builder.PackageBuildResponse{}
	err = json.Unmarshal(rBody, &pkgBuildResp)
	if err != nil {
		logger.Error("error parsing resp body", zap.Error(err))
		return nil, fmt.Errorf("error parsing response body: %w", err)
	}

	return &pkgBuildResp, ferror.MakeErrorFromHTTP(resp)
}

func (c *client) Clean(ctx context.Context, srcPkgFilename string) error {
	logger := otelUtils.LoggerWithTraceID(ctx, c.logger)

	req, err := http.NewRequest(http.MethodDelete, c.getCleanUrl(srcPkgFilename), http.NoBody)
	if err != nil {
		return fmt.Errorf("error creating http request: %w", err)
	}

	resp, err := ctxhttp.Do(ctx, c.httpClient.StandardClient(), req)
	if err != nil {
		logger.Error("error sending clean request", zap.Error(err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return ferror.MakeErrorFromHTTP(resp)
	}

	return nil
}
