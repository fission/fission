// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"

	// Register the statestore HTTP client driver so Start can Open(driver=client)
	// to reach the embedded statestore service for async invocation.
	_ "github.com/fission/fission/pkg/statestore/client"
)

// asyncInvoker is the router's RFC-0024 async-enqueue entry point, wired into the
// public function handler. It holds the statestore queue and maps a function's
// InvocationConfig to the durable policy — the fv1 coupling lives here so the
// asyncinvoke package stays a pure library. A nil asyncInvoker (or nil queue)
// means the feature is off, and an async-mode request then gets 501.
type asyncInvoker struct {
	queue  statestore.Queue
	logger logr.Logger
}

func (a *asyncInvoker) enabled() bool { return a != nil && a.queue != nil }

// handle enqueues an async invocation for fn and writes the HTTP response:
// 202 {invocationId} on success, 413 on an oversized body, 503 when the store is
// unreachable (fail loud — invariant A1: never a silently dropped 202), and 501
// when async invocation is not enabled on this cluster.
func (a *asyncInvoker) handle(w http.ResponseWriter, r *http.Request, fn *fv1.Function) {
	if !a.enabled() {
		http.Error(w, "async invocation is not enabled on this cluster", http.StatusNotImplemented)
		return
	}
	p := asyncinvoke.Params{
		Namespace:       fn.ObjectMeta.Namespace,
		Function:        fn.ObjectMeta.Name,
		FunctionTimeout: fn.Spec.FunctionTimeout,
		DedupKey:        r.Header.Get(asyncinvoke.HeaderDedupKey),
		Depth:           parseDepthHeader(r.Header.Get(asyncinvoke.HeaderInvocationDepth)),
		Policy:          policyFromSpec(fn.Spec.Invocation),
	}
	id, err := asyncinvoke.Enqueue(r.Context(), a.queue, w, r, p)
	if err != nil {
		if errors.Is(err, asyncinvoke.ErrBodyTooLarge) {
			http.Error(w, "request body exceeds the async invocation limit", http.StatusRequestEntityTooLarge)
			return
		}
		a.logger.Error(err, "async enqueue failed", "namespace", p.Namespace, "function", p.Function)
		http.Error(w, "async invocation store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(asyncinvoke.HeaderInvocationID, id)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"invocationId": id})
}

// parseDepthHeader reads the destination-chain depth from the replay header,
// defaulting to 0 on absence or a malformed/negative value.
func parseDepthHeader(raw string) int {
	if raw == "" {
		return 0
	}
	d, err := strconv.Atoi(raw)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// policyFromSpec maps a function's InvocationConfig to the durable async policy
// stamped into the envelope. A nil config yields the zero policy (dispatcher
// defaults). A false Jitter pointer disables jitter; nil leaves it enabled.
func policyFromSpec(ic *fv1.InvocationConfig) asyncinvoke.Policy {
	if ic == nil {
		return asyncinvoke.Policy{}
	}
	p := asyncinvoke.Policy{}
	if ic.Retry.MaxAttempts != nil {
		p.MaxAttempts = *ic.Retry.MaxAttempts
	}
	if ic.Retry.BackoffBase != nil {
		p.BackoffBase = ic.Retry.BackoffBase.Duration
	}
	if ic.Retry.BackoffCap != nil {
		p.BackoffCap = ic.Retry.BackoffCap.Duration
	}
	if ic.Retry.Jitter != nil && !*ic.Retry.Jitter {
		p.NoJitter = true
	}
	if ic.MaxAge != nil {
		p.MaxAge = ic.MaxAge.Duration
	}
	return p
}
