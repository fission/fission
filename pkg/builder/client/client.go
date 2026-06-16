// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/context/ctxhttp"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
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
		logger     logr.Logger
		url        string
		httpClient *retryablehttp.Client
	}
)

// MakeClient creates a builder client.
//
// masterSecret enables HMAC-SHA256 request signing per the design at
// docs/internal-auth/00-design.md. The master is the chart-installed
// fission-internal-auth/secret value; this client derives the
// per-service signing key for ServiceBuilder internally so a leak of
// one channel's runtime memory cannot forge requests on a different
// channel. The builder only enforces signatures when its own copy of
// the master is set on the server, so passing nil (or empty) here is
// backwards compatible with installs that have internalAuth disabled.
//
// The buildermgr controller pod (the only caller) should pass
// storagesvcClient.HMACSecretFromEnv(); the same env var
// (FISSION_INTERNAL_AUTH_SECRET) backs every internal channel.
func MakeClient(logger logr.Logger, builderUrl string, masterSecret []byte) ClientInterface {
	return makeBuilderClient(logger, builderUrl, func(base http.RoundTripper) http.RoundTripper {
		if len(masterSecret) > 0 {
			return hmacauth.ServiceSigner(masterSecret, hmacauth.ServiceBuilder, base, time.Now)
		}
		return base
	})
}

// MakeClientNS is MakeClient for a target builder that verifies /build with its
// per-namespace derived key rather than the master-derived ServiceBuilder key
// (multi-namespace tenancy). The caller still holds the master and derives
// K(builder, namespace) on the fly, so a leak of one tenant pod's key cannot
// forge a build against another tenant's builder. An empty master yields an
// unsigned client (pass-through), matching MakeClient.
func MakeClientNS(logger logr.Logger, builderUrl string, masterSecret []byte, namespace string) ClientInterface {
	return makeBuilderClient(logger, builderUrl, func(base http.RoundTripper) http.RoundTripper {
		if len(masterSecret) > 0 {
			return hmacauth.ServiceSignerNS(masterSecret, hmacauth.ServiceBuilder, namespace, base, time.Now)
		}
		return base
	})
}

// makeBuilderClient builds the retryable HTTP builder client, layering the
// caller's signer (via sign) over the otel-instrumented default transport.
func makeBuilderClient(logger logr.Logger, builderUrl string, sign func(base http.RoundTripper) http.RoundTripper) ClientInterface {
	hc := retryablehttp.NewClient()
	hc.ErrorHandler = retryablehttp.PassthroughErrorHandler
	hc.HTTPClient.Transport = sign(otelhttp.NewTransport(hc.HTTPClient.Transport))
	return &client{
		logger:     logger.WithName("builder_client"),
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
		logger.Error(err, "error reading resp body")
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	pkgBuildResp := builder.PackageBuildResponse{}
	err = json.Unmarshal(rBody, &pkgBuildResp)
	if err != nil {
		logger.Error(err, "error parsing resp body")
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
		logger.Error(err, "error sending clean request")
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
