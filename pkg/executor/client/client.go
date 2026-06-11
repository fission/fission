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
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/utils/metrics"
)

// tapFailureEscalation is the consecutive-failure count at which flushTaps
// switches from a V(1) note to an error log (3 failures = ~15s of lost taps).
const tapFailureEscalation = 3

// tapFlushErrors counts failed tap-flush batches. Each failure drops one 5s
// batch of liveness taps; a sustained non-zero rate means index-admitted pods
// are invisible to the executor's idle reaper and at risk of being reaped
// while serving.
var tapFlushErrors = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "fission_router_tap_flush_errors_total",
	Help: "Failed batched tap flushes from the router to the executor.",
})

func init() {
	metrics.Registry.MustRegister(tapFlushErrors)
}

type (
	// ClientInterface is the interface for executor client.
	ClientInterface interface {
		GetServiceForFunction(ctx context.Context, fn *fv1.Function) (string, error)
		TapService(fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL url.URL)
		UnTapService(ctx context.Context, fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL *url.URL) error
		// EnsureCapacity is the RFC-0002 saturation path (POST
		// /v2/ensureCapacity). A 404 from an executor predating it surfaces
		// as an ErrorNotFound ferror; the router degrades to
		// GetServiceForFunction (upgrade-order safety).
		EnsureCapacity(ctx context.Context, fn *fv1.Function, observedReady, observedBusy int) (string, error)
	}
	// client is wrapper on a HTTP client.
	client struct {
		logger      logr.Logger
		executorURL string
		tappedByURL map[string]TapServiceRequest
		requestChan chan TapServiceRequest
		httpClient  *retryablehttp.Client
		// consecutive flushTaps failures; with the RFC-0002 warm path these
		// batched taps are the only liveness signal for index-admitted pods,
		// so persistent failure must escalate (see flushTaps).
		tapFailures atomic.Int64
	}

	// TapServiceRequest represents
	TapServiceRequest struct {
		FnMetadata     metav1.ObjectMeta
		FnExecutorType fv1.ExecutorType
		ServiceURL     string
	}

	// EnsureCapacityRequest is the body of POST /v2/ensureCapacity (RFC-0002):
	// the router reports its observed endpoint counts and asks the executor —
	// still the capacity authority — to specialize one more pod (synchronous
	// address response) or answer 429 at the function's concurrency cap.
	EnsureCapacityRequest struct {
		Function               *fv1.Function `json:"function"`
		ObservedReadyEndpoints int           `json:"observedReadyEndpoints"`
		ObservedBusyEndpoints  int           `json:"observedBusyEndpoints"`
	}
)

// MakeClient initializes and returns a Client instance.
//
// masterSecret enables HMAC-SHA256 request signing per the design at
// docs/internal-auth/00-design.md. The master is the chart-installed
// fission-internal-auth/secret value; this client derives the
// per-service signing key for ServiceExecutor internally so a leak of
// one channel's runtime memory cannot forge requests on a different
// channel. The executor only enforces signatures when its own copy of
// the master is set on the server, so passing nil (or empty) here is
// backwards compatible with installs that have internalAuth disabled.
//
// The router (the only in-cluster caller) should pass
// storagesvcClient.HMACSecretFromEnv(); the same env var
// (FISSION_INTERNAL_AUTH_SECRET) backs every internal channel.
func MakeClient(logger logr.Logger, executorURL string, masterSecret []byte) ClientInterface {
	hc := retryablehttp.NewClient()
	var rt http.RoundTripper = otelhttp.NewTransport(hc.HTTPClient.Transport)
	if len(masterSecret) > 0 {
		rt = hmacauth.ServiceSigner(masterSecret, hmacauth.ServiceExecutor, rt, time.Now)
	}
	hc.HTTPClient.Transport = rt
	c := &client{
		logger:      logger.WithName("executor_client"),
		executorURL: strings.TrimSuffix(executorURL, "/"),
		tappedByURL: make(map[string]TapServiceRequest),
		requestChan: make(chan TapServiceRequest, 100),
		httpClient:  hc,
	}
	go c.service()
	return c
}

// GetServiceForFunction returns the service name for a given function.
func (c *client) GetServiceForFunction(ctx context.Context, fn *fv1.Function) (string, error) {
	executorURL := c.executorURL + "/v2/getServiceForFunction"

	body, err := json.Marshal(fn)
	if err != nil {
		return "", fmt.Errorf("could not marshal request body for getting service for function: %w", err)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", executorURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("could not create request for getting service for function: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error posting to getting service for function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", ferror.MakeErrorFromHTTP(resp)
	}

	svcName, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body from getting service for function: %w", err)
	}

	return string(svcName), nil
}

// EnsureCapacity asks the executor to specialize one more pod for the
// function (RFC-0002): the router calls it when every endpoint it knows is
// saturated by its local admission accounting. Returns the new pod's address;
// a 429 (concurrency cap) or 404 (older executor without the endpoint)
// surfaces as a ferror with the corresponding code so the caller can degrade.
func (c *client) EnsureCapacity(ctx context.Context, fn *fv1.Function, observedReady, observedBusy int) (string, error) {
	executorURL := c.executorURL + "/v2/ensureCapacity"

	body, err := json.Marshal(EnsureCapacityRequest{
		Function:               fn,
		ObservedReadyEndpoints: observedReady,
		ObservedBusyEndpoints:  observedBusy,
	})
	if err != nil {
		return "", fmt.Errorf("could not marshal request body for ensuring capacity for function: %w", err)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", executorURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("could not create request for ensuring capacity for function: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error posting to ensuring capacity for function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", ferror.MakeErrorFromHTTP(resp)
	}

	svcName, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body from ensuring capacity for function: %w", err)
	}

	return string(svcName), nil
}

// UnTapService sends a request to /v2/unTapService.
func (c *client) UnTapService(ctx context.Context, fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL *url.URL) error {
	url := c.executorURL + "/v2/unTapService"
	tapSvc := TapServiceRequest{
		FnMetadata:     fnMeta,
		FnExecutorType: executorType,
		ServiceURL:     strings.TrimPrefix(serviceURL.String(), "http://"),
	}

	body, err := json.Marshal(tapSvc)
	if err != nil {
		return fmt.Errorf("could not marshal request body for getting service for function: %w", err)
	}
	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not create request for untap service for function: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error posting to getting service for function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ferror.MakeErrorFromHTTP(resp)
	}

	return nil
}

// service accumulates TapService requests and flushes them to the executor in
// a batch every 5 seconds, so taps cost one RPC per interval regardless of
// request rate.
func (c *client) service() {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()
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

			go c.flushTaps(urls)
		}
	}
}

// flushTaps sends one accumulated batch of tap requests to the executor.
// Best-effort: a 404 just means some entries expired on the executor side.
// A failed batch is dropped, not requeued — acceptable for one interval, but
// with the RFC-0002 warm path these taps are the ONLY liveness signal for
// index-admitted pods (the router never RPCs the executor for them), so a
// persistently failing flush would let the idle reaper age out pods that are
// actively serving. Sustained failure therefore escalates to an error log and
// is always counted in fission_router_tap_flush_errors_total.
func (c *client) flushTaps(urls map[string]TapServiceRequest) {
	svcReqs := []TapServiceRequest{}
	for _, req := range urls {
		svcReqs = append(svcReqs, req)
	}
	c.logger.V(1).Info("tapped services in batch", "service_count", len(urls))
	err := c._tapService(context.Background(), svcReqs)
	if err == nil {
		c.tapFailures.Store(0)
		return
	}
	// A 404 is routine churn, not a liveness failure: it means some tapped
	// addresses already expired/were deleted on the executor side (the
	// executor logs this at V(1) for the same reason). The executor can't be
	// about to wrongly reap a pod it no longer tracks, so a NotFound must NOT
	// count toward the "taps failing, serving pods at risk" escalation —
	// otherwise normal function churn trips a misleading Error alarm. Only
	// genuine failures (unreachable executor, timeout, 5xx) escalate.
	if ferror.IsNotFound(err) {
		c.tapFailures.Store(0)
		c.logger.V(1).Info("tap flush skipped expired entries", "error", err.Error())
		return
	}
	tapFlushErrors.Inc()
	if failures := c.tapFailures.Add(1); failures >= tapFailureEscalation {
		c.logger.Error(err, "tap flush failing persistently; idle reaper may reap serving pods",
			"consecutive_failures", failures, "service_count", len(urls))
	} else {
		c.logger.V(1).Info("error tapping function service address", "error", err.Error())
	}
}

// TapService sends a TapServiceRequest over the request channel.
func (c *client) TapService(fnMeta metav1.ObjectMeta, executorType fv1.ExecutorType, serviceURL url.URL) {
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

func (c *client) _tapService(ctx context.Context, tapSvcReqs []TapServiceRequest) error {
	executorURL := c.executorURL + "/v2/tapServices"

	body, err := json.Marshal(tapSvcReqs)
	if err != nil {
		return err
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", executorURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not create request for tap service request: %w", err)
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
