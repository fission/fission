// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	eclient "github.com/fission/fission/pkg/executor/client"
)

// Tapper is the liveness/accounting seam between the proxy and the executor:
// Tap marks a service in use (batched atime keepalive, also the streaming
// heartbeat), UnTap releases a poolmgr pod's request slot. One implementation
// (executor RPC). Index-admitted endpoints do NOT release slots through
// Tapper: their accounting is router-local via ResolvedEntry.Release, and the
// two must never mix (see the resolver docs) — UnTap here is only for
// executor-resolved poolmgr entries. Tap (atime liveness) applies to both.
type Tapper interface {
	Tap(fn *fv1.Function, serviceURL *url.URL)
	UnTap(ctx context.Context, fn *fv1.Function, serviceURL *url.URL) error
}

// executorTapper taps/untaps through the executor client (today's behavior).
type executorTapper struct {
	logger       logr.Logger
	executor     eclient.ClientInterface
	unTapTimeout time.Duration
}

// Tap enqueues a batched tap for the service address.
func (t *executorTapper) Tap(fn *fv1.Function, serviceURL *url.URL) {
	if t.executor == nil || serviceURL == nil {
		return
	}
	t.executor.TapService(fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, *serviceURL)
}

// UnTap marks the serviceURL in executor's cache as inactive, so that it can be reused.
func (t *executorTapper) UnTap(ctx context.Context, fn *fv1.Function, serviceUrl *url.URL) error {
	t.logger.V(1).Info("UnTapService Called")
	ctx, cancel := context.WithTimeoutCause(ctx, t.unTapTimeout, fmt.Errorf("unTapService timeout (%f)s exceeded", t.unTapTimeout.Seconds()))
	defer cancel()
	err := t.executor.UnTapService(ctx, fn.ObjectMeta, fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType, serviceUrl)
	if err != nil {
		statusCode, errMsg := ferror.GetHTTPError(err)
		t.logger.Error(err, "error from UnTapService", "error_message", errMsg,
			"function", fn,
			"status_code", statusCode)
		return err
	}
	return nil
}
