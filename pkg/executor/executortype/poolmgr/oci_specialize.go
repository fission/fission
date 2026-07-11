// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/httpx"
	otelUtils "github.com/fission/fission/pkg/utils/otel"

	"github.com/fission/fission/pkg/svcinfo"
)

// loadOnlySpecialize specializes a Path B (image-volume) pod: the code is
// already mounted read-only by the kubelet, so there is nothing to fetch —
// POST the FunctionLoadRequest straight to the env container's /v2/specialize.
// Uses the shared connect-refused retry/backoff (the env server can briefly
// refuse connections right after the pod turns Ready). Path B pods only exist
// for v2+ environments (the eligibility check falls v1 back to the fetcher
// path), so the v1 text endpoint is not handled here.
func (gp *GenericPool) loadOnlySpecialize(ctx context.Context, podIP string, fn *fv1.Function) error {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)

	specializeReq := gp.fetcherConfig.NewSpecializeRequest(fn, gp.env)
	loadPayload, err := json.Marshal(specializeReq.LoadReq)
	if err != nil {
		return fmt.Errorf("error encoding load request: %w", err)
	}

	specializeURL := fmt.Sprintf("http://%s:%d/v2/specialize", podIP, svcinfo.PortEnvRuntime)
	if IsIPv6(podIP) {
		specializeURL = fmt.Sprintf("http://[%s]:%d/v2/specialize", podIP, svcinfo.PortEnvRuntime)
	}
	logger.Info("load-only specializing image-volume pod", "function", fn.Name, "url", specializeURL)

	// Unlike the fetcher's loop (which talks to 127.0.0.1), this POST crosses
	// the cluster network to a pod IP; a black-holed destination (pod deleted
	// mid-flight, packet-dropping NetworkPolicy) must fail an attempt promptly
	// rather than hang on the OS dial timeout.
	client := &http.Client{Timeout: 30 * time.Second}

	const maxRetries = 30
	err = httpx.PostWithConnRetry(ctx, client, specializeURL, "application/json", loadPayload, logger, maxRetries, func() {
		otelUtils.SpanTrackEvent(ctx, "loadOnlySpecializeCall", otelUtils.MapToAttributes(map[string]string{
			"url": specializeURL,
		})...)
	})
	if err != nil {
		return fmt.Errorf("error specializing function pod: %w", err)
	}
	otelUtils.SpanTrackEvent(ctx, "specializedPod")
	return nil
}
