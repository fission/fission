// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// proxyPolicy is the resolved, immutable per-request proxy behavior. It is the
// single mapping from CRD intent to transport behavior (see
// docs/streaming P1): the RetryingRoundTripper and ReverseProxy consume this and
// never re-read the CRD. Future RFCs extend behavior by extending
// resolveProxyPolicy, not the transport internals.
type proxyPolicy struct {
	streaming   bool
	protocol    fv1.StreamingProtocol
	idleTimeout time.Duration // 0 unless streaming
	maxDuration time.Duration // classic total deadline; streaming hard ceiling (0 = none)
}

// resolveProxyPolicy maps a Function's Streaming config to a proxyPolicy.
// fnTimeout is the function's resolved FunctionTimeout (0 = none). defIdle is the
// cluster default idle timeout (DefaultStreamIdleSeconds, possibly overridden by
// ROUTER_STREAM_IDLE_TIMEOUT).
func resolveProxyPolicy(fn *fv1.Function, fnTimeout, defIdle time.Duration) proxyPolicy {
	p := proxyPolicy{maxDuration: fnTimeout}
	sc := fn.Spec.Streaming
	if sc == nil || !sc.Enabled {
		return p // classic: zero-streaming, today's behavior
	}
	p.streaming = true
	p.protocol = sc.Protocol
	if p.protocol == "" {
		p.protocol = fv1.StreamingAuto
	}
	p.idleTimeout = defIdle
	if sc.IdleTimeoutSeconds > 0 {
		p.idleTimeout = time.Duration(sc.IdleTimeoutSeconds) * time.Second
	}
	if sc.MaxDurationSeconds > 0 {
		p.maxDuration = time.Duration(sc.MaxDurationSeconds) * time.Second
	} else if fnTimeout <= 0 {
		p.maxDuration = 0 // no ceiling; idle timeout governs
	}
	return p
}
