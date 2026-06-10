// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/net/context/ctxhttp"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// loadOnlySpecialize specializes a Path B (image-volume) pod: the code is
// already mounted read-only by the kubelet, so there is nothing to fetch —
// POST the FunctionLoadRequest straight to the env container's /v2/specialize.
// Mirrors the retry/backoff loop of fetcher.SpecializePod (the env server can
// briefly refuse connections right after the pod turns Ready). Path B pods
// only exist for v2+ environments (the eligibility check falls v1 back to the
// fetcher path), so the v1 text endpoint is not handled here.
func (gp *GenericPool) loadOnlySpecialize(ctx context.Context, podIP string, fn *fv1.Function) error {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)

	specializeReq := gp.fetcherConfig.NewSpecializeRequest(fn, gp.env)
	loadPayload, err := json.Marshal(specializeReq.LoadReq)
	if err != nil {
		return fmt.Errorf("error encoding load request: %w", err)
	}

	specializeURL := fmt.Sprintf("http://%s:8888/v2/specialize", podIP)
	if IsIPv6(podIP) {
		specializeURL = fmt.Sprintf("http://[%s]:8888/v2/specialize", podIP)
	}
	logger.Info("load-only specializing image-volume pod", "function", fn.Name, "url", specializeURL)

	// Unlike the fetcher's loop (which talks to 127.0.0.1), this POST
	// crosses the cluster network to a pod IP; a black-holed destination
	// (pod deleted mid-flight, packet-dropping NetworkPolicy) must fail an
	// attempt promptly rather than hang on the OS dial timeout.
	client := &http.Client{Timeout: 30 * time.Second}

	const maxRetries = 30
	for i := range maxRetries {
		otelUtils.SpanTrackEvent(ctx, "loadOnlySpecializeCall", otelUtils.MapToAttributes(map[string]string{
			"url": specializeURL,
		})...)
		var resp *http.Response
		resp, err = ctxhttp.Post(ctx, client, specializeURL, "application/json", bytes.NewReader(loadPayload))
		if err == nil && resp.StatusCode < 300 {
			resp.Body.Close()
			otelUtils.SpanTrackEvent(ctx, "specializedPod")
			return nil
		}

		netErr := network.Adapter(err)
		if netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError()) {
			if i < maxRetries-1 {
				logger.Error(netErr, "error connecting to function environment pod for specialization request, retrying")
				select {
				case <-ctx.Done():
					return fmt.Errorf("error specializing function pod: %w", ctx.Err())
				case <-time.After(500 * time.Duration(2*i+1) * time.Millisecond):
				}
				continue
			}
		}

		if err == nil {
			err = ferror.MakeErrorFromHTTP(resp)
		}
		return fmt.Errorf("error specializing function pod: %w", err)
	}
	return fmt.Errorf("error specializing function pod after %v retries: %w", maxRetries, err)
}
