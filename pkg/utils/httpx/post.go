// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package httpx holds small HTTP helpers shared across Fission.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/net/context/ctxhttp"

	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
)

// PostWithConnRetry POSTs body to url, retrying on connection-refused / dial /
// connection-reset (or premature-EOF) errors with the same exponential backoff
// the in-cluster specialize path uses — for talking to a server that may not
// have finished starting (e.g. an env container's /v2/specialize right after
// the container turns ready, or one reached through Docker's port-proxy which
// accepts the connection before the in-container server is listening). It
// returns nil on a 2xx response and the last error otherwise. The request must
// be idempotent, since a reset/EOF may occur after the server received it.
// onAttempt, when non-nil, is called before each attempt (used to emit an OTel
// span event in-cluster).
func PostWithConnRetry(ctx context.Context, client *http.Client, url, contentType string, body []byte, logger logr.Logger, maxRetries int, onAttempt func()) error {
	var err error
	for i := range maxRetries {
		if onAttempt != nil {
			onAttempt()
		}
		var resp *http.Response
		resp, err = ctxhttp.Post(ctx, client, url, contentType, bytes.NewReader(body))
		if err == nil && resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}

		netErr := network.Adapter(err)
		if netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError() || netErr.IsConnResetError()) && i < maxRetries-1 {
			logger.Error(netErr, "error connecting, retrying", "url", url)
			select {
			case <-ctx.Done():
				return fmt.Errorf("post %s: %w", url, ctx.Err())
			case <-time.After(500 * time.Duration(2*i+1) * time.Millisecond):
			}
			continue
		}

		if err == nil {
			err = ferror.MakeErrorFromHTTP(resp)
		}
		return fmt.Errorf("post %s: %w", url, err)
	}
	return fmt.Errorf("post %s after %d retries: %w", url, maxRetries, err)
}
