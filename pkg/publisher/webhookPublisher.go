/*
Copyright 2017 The Fission Authors.

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

package publisher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"golang.org/x/net/context/ctxhttp"

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// WebhookPublisher for a single URL. Satisfies the Publisher interface.
	WebhookPublisher struct {
		logger logr.Logger

		requestChannel chan *publishRequest

		maxRetries int
		retryDelay time.Duration

		baseURL string
		timeout time.Duration

		// httpClient is the per-publisher transport. We keep it on the
		// struct (rather than reusing the package-level WebhookHttpClient)
		// so each publisher can carry its own HMAC signer configured at
		// construction time.
		httpClient *http.Client
	}
	publishRequest struct {
		ctx        context.Context
		body       string
		headers    map[string]string
		method     string
		target     string
		retries    int
		retryDelay time.Duration
	}
)

// MakeWebhookPublisher creates a WebhookPublisher object for the given baseURL.
//
// If ROUTER_INTERNAL_URL is set in the environment, baseURL is overridden
// with that value — this points kubewatcher / timer / mqtrigger at the
// router's internal listener (port 8889) instead of the public listener
// (port 8888). The internal listener is the only route registration for
// /fission-function/<ns>/<name> after GHSA-3g33-6vg6-27m8.
//
// If FISSION_INTERNAL_AUTH_SECRET is set, the publisher's HTTP transport
// is wrapped with hmacauth.NewSigner so each /fission-function/...
// invocation carries the X-Fission-Auth-* headers that the router's
// internal-listener verifier expects. An unset secret leaves the
// transport unsigned, matching the verifier's pass-through mode for
// first-deploy installs.
func MakeWebhookPublisher(logger logr.Logger, baseURL string) *WebhookPublisher {
	if internal := os.Getenv("ROUTER_INTERNAL_URL"); internal != "" {
		baseURL = internal
	}
	p := &WebhookPublisher{
		logger:         logger.WithName("webhook_publisher"),
		baseURL:        baseURL,
		httpClient:     newWebhookHTTPClient(),
		requestChannel: make(chan *publishRequest, 32), // buffered channel
		// TODO make this configurable
		timeout: 60 * time.Minute,
		// TODO make this configurable
		maxRetries: 10,
		retryDelay: 500 * time.Millisecond,
	}
	go p.svc()
	return p
}

// newWebhookHTTPClient constructs the HTTP client used to invoke
// /fission-function/<ns>/<name> on the router's internal listener.
// The transport stack is hmacauth (outermost, when secret set) ->
// otelhttp -> http.DefaultTransport: the signer runs first and
// computes the canonical form over (method, path, body, timestamp);
// otelhttp then injects trace headers on the inner transport. OTEL
// trace headers are intentionally NOT part of the signed canonical
// form, which keeps the signature stable across tracing-config
// changes and avoids re-signing per request retry.
func newWebhookHTTPClient() *http.Client {
	var rt http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); secret != "" {
		rt = hmacauth.NewSigner([]byte(secret), rt, time.Now)
	}
	return &http.Client{Transport: rt}
}

// Publish sends a request to the target with payload having given body and headers
func (p *WebhookPublisher) Publish(ctx context.Context, body string, headers map[string]string, method, target string) {
	tracer := otel.Tracer("WebhookPublisher")
	ctx, span := tracer.Start(ctx, "WebhookPublisher/Publish")
	defer span.End()

	// serializing the request gives user a guarantee that the request is sent in sequence order
	p.requestChannel <- &publishRequest{
		ctx:        ctx,
		body:       body,
		headers:    headers,
		method:     method,
		target:     target,
		retries:    p.maxRetries,
		retryDelay: p.retryDelay,
	}
}

func (p *WebhookPublisher) svc() {
	for {
		r := <-p.requestChannel
		p.makeHTTPRequest(r)
	}
}

// WebhookHttpClient is retained for backwards compatibility with any
// external callers that may have referenced it. New code should use a
// *WebhookPublisher (which carries its own per-instance http.Client).
var WebhookHttpClient = &http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}

func (p *WebhookPublisher) makeHTTPRequest(r *publishRequest) {
	url := p.baseURL + "/" + strings.TrimPrefix(r.target, "/")

	msg := fmt.Sprintf("making HTTP %s request", r.method)
	msgType := "error"
	logger := otelUtils.LoggerWithTraceID(r.ctx, p.logger).WithValues("url", url, "type", "publish_request")

	// log once for this request
	defer func() {
		switch msgType {
		case "info":
			logger.Info(msg)
		case "error":
			logger.Error(nil, msg)
		}
	}()

	var buf bytes.Buffer
	buf.WriteString(r.body)

	// Create request
	req, err := http.NewRequest(r.method, url, &buf)
	if err != nil {
		logger = logger.WithValues("error", err)
		return
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	// Make the request
	ctx, cancel := context.WithTimeoutCause(r.ctx, p.timeout, fmt.Errorf("webhook request timed out (%f)s exceeded ", p.timeout.Seconds()))
	defer cancel()
	resp, err := ctxhttp.Do(ctx, p.httpClient, req)
	if err != nil {
		logger = logger.WithValues("request", r)
	} else {
		var body []byte
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			logger = logger.WithValues("request", r)
			msg = "read response body error"
		} else {
			logger = logger.WithValues("status_code", resp.StatusCode, "body", string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				msgType = "info"
			} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				msg = "request returned bad request status code"
			} else {
				msg = "request returned failure status code"
			}
			return
		}
	}

	// Schedule a retry, or give up if out of retries
	r.retries--
	if r.retries > 0 {
		r.retryDelay *= time.Duration(2)
		time.AfterFunc(r.retryDelay, func() {
			p.requestChannel <- r
		})
	} else {
		msg = "final retry failed, giving up"
		// Event dropped
	}
}
