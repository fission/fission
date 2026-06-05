// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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

// MakeWebhookPublisher creates a WebhookPublisher object for the given
// baseURL. The publisher uses baseURL verbatim; callers that want to
// override with a router-internal address must resolve it themselves
// (typically at fission-bundle dispatch time, where the
// ROUTER_INTERNAL_URL env var is consulted before the routerUrl flag
// is forwarded into kubewatcher / timer / mqtrigger Start). Keeping
// MakeWebhookPublisher deterministic lets unit tests construct a
// publisher against an httptest.Server URL without having to scrub
// env state.
//
// If FISSION_INTERNAL_AUTH_SECRET is set, the publisher's HTTP transport
// is wrapped with hmacauth.ServiceSigner using ServiceRouterInternal,
// so each /fission-function/... invocation carries the X-Fission-Auth-*
// headers that the router's internal-listener verifier expects (with
// the per-service derived key, not the master). An unset secret leaves
// the transport unsigned, matching the verifier's pass-through mode
// for first-deploy installs.
func MakeWebhookPublisher(logger logr.Logger, baseURL string) *WebhookPublisher {
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
//
// The signing key is derived from the master via HKDF-SHA256 for
// ServiceRouterInternal so a leak of this caller's runtime memory
// cannot forge requests on other Fission internal channels
// (storagesvc, fetcher, builder, executor). See
// docs/internal-auth/00-design.md.
func newWebhookHTTPClient() *http.Client {
	var rt http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if master := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); master != "" {
		rt = hmacauth.ServiceSigner([]byte(master), hmacauth.ServiceRouterInternal, rt, time.Now)
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
		case "retry":
			// A retry is scheduled; quiet per-attempt log. The final
			// give-up (or a terminal failure) logs at error level.
			logger.V(1).Info(msg)
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
		defer resp.Body.Close()
		var body []byte
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			logger = logger.WithValues("request", r)
			msg = "read response body error"
		} else {
			logger = logger.WithValues("status_code", resp.StatusCode, "body", string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				msgType = "info"
				return
			} else if resp.StatusCode == http.StatusNotFound {
				// The router returns 404 while a freshly created trigger's
				// route is still propagating to the mux; treat it as
				// transient and retry (bounded by maxRetries) instead of
				// dropping the event. A genuinely deleted function burns
				// the bounded retries and is then dropped, logging a single
				// error on the final attempt — per-attempt logs stay at
				// V(1) to avoid flooding the error log (a deleted function
				// would otherwise emit maxRetries errors per event).
				msg = "request returned not found, will retry"
				msgType = "retry"
				// fall through to retry scheduling below
			} else if resp.StatusCode < 500 {
				msg = "request returned bad request status code"
				return
			} else {
				msg = "request returned failure status code"
				return
			}
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
		msgType = "error" // dropped events always surface at error level
		// Event dropped
	}
}
