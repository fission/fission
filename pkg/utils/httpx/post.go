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

		if isStartupConnError(err) && i < maxRetries-1 {
			logger.Error(network.Adapter(err), "error connecting, retrying", "url", url)
			if berr := backoff(ctx, i); berr != nil {
				return fmt.Errorf("post %s: %w", url, berr)
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

// WaitReady polls url with GET until the server returns any HTTP response (i.e.
// it is up), retrying the same connection-level failures as PostWithConnRetry
// while the server starts. Used to gate a server that has no specialize/load call
// to drive readiness (e.g. a container-executor function's own image).
func WaitReady(ctx context.Context, client *http.Client, url string, maxRetries int) error {
	var err error
	for i := range maxRetries {
		var resp *http.Response
		resp, err = ctxhttp.Get(ctx, client, url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if isStartupConnError(err) && i < maxRetries-1 {
			if berr := backoff(ctx, i); berr != nil {
				return fmt.Errorf("get %s: %w", url, berr)
			}
			continue
		}
		return fmt.Errorf("get %s: %w", url, err)
	}
	return fmt.Errorf("get %s after %d retries: %w", url, maxRetries, err)
}

// isStartupConnError reports whether err is the kind of connection-level failure
// a not-yet-ready server produces (refused / dial / reset / premature-EOF), and
// so is worth retrying for an idempotent request.
func isStartupConnError(err error) bool {
	netErr := network.Adapter(err)
	return netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError() || netErr.IsConnResetError())
}

// backoff waits the i-th attempt's delay, returning ctx.Err() if canceled first.
func backoff(ctx context.Context, i int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Duration(2*i+1) * time.Millisecond):
		return nil
	}
}
