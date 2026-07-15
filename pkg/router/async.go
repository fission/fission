// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"

	// Register the statestore drivers the router opens for async invocation: the
	// HTTP client (embedded statestore mode → svc/statestore) and Postgres
	// (external mode → the DB directly). STATESTORE_DRIVER selects at runtime.
	_ "github.com/fission/fission/pkg/statestore/client"
	_ "github.com/fission/fission/pkg/statestore/postgres"
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
	cfg := funcConfigFromSpec(fn)
	p := asyncinvoke.Params{
		Namespace:       fn.Namespace,
		Function:        fn.Name,
		FunctionTimeout: cfg.FunctionTimeout,
		DedupKey:        r.Header.Get(asyncinvoke.HeaderDedupKey),
		// Depth stays 0: a public caller must not seed the destination-chain depth
		// (it is derived from the signed internal replay, not the request), so the
		// loop guard cannot be defeated by an external X-Fission-Invocation-Depth.
		Policy:    cfg.Policy,
		OnSuccess: cfg.OnSuccess,
		OnFailure: cfg.OnFailure,
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

// funcConfigFromSpec is the single mapper from a Function to its resolved async
// config (policy + destinations + timeout). Both the initial enqueue (handle) and
// each destination-chain hop (newFunctionConfigResolver) go through it, so the
// fv1↔asyncinvoke translation lives in one place and cannot drift between them.
func funcConfigFromSpec(fn *fv1.Function) asyncinvoke.FunctionConfig {
	onSuccess, onFailure := destinationsFromSpec(fn.Spec.Invocation, fn.Namespace)
	return asyncinvoke.FunctionConfig{
		Policy:          policyFromSpec(fn.Spec.Invocation),
		OnSuccess:       onSuccess,
		OnFailure:       onFailure,
		FunctionTimeout: fn.Spec.FunctionTimeout,
	}
}

// newFunctionConfigResolver resolves a destination function's async config from the
// controller-runtime Function cache, so each hop of a destination chain stamps its
// own policy + onward destinations (and the depth cap is reachable). A genuinely
// missing function → found=false → the destination is dropped rather than looping;
// a transient lookup error is logged (not silently conflated with absence) so a
// lost destination is diagnosable.
func newFunctionConfigResolver(c client.Client, logger logr.Logger) asyncinvoke.FunctionConfigResolver {
	return func(ctx context.Context, ns, name string) (asyncinvoke.FunctionConfig, bool) {
		var fn fv1.Function
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &fn); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "resolving async destination function config; dropping destination",
					"namespace", ns, "function", name)
			}
			return asyncinvoke.FunctionConfig{}, false
		}
		return funcConfigFromSpec(&fn), true
	}
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

// destinationsFromSpec maps a function's InvocationConfig destinations to the
// flat envelope form. Function destinations are same-namespace (FunctionReference
// has no namespace), so they inherit the source function's namespace.
func destinationsFromSpec(ic *fv1.InvocationConfig, fnNamespace string) (onSuccess, onFailure *asyncinvoke.Destination) {
	if ic == nil {
		return nil, nil
	}
	return destFromRef(ic.OnSuccess, fnNamespace), destFromRef(ic.OnFailure, fnNamespace)
}

func destFromRef(ref *fv1.DestinationRef, fnNamespace string) *asyncinvoke.Destination {
	switch {
	case ref == nil:
		return nil
	case ref.Function != nil:
		return &asyncinvoke.Destination{FunctionNamespace: fnNamespace, FunctionName: ref.Function.Name}
	case ref.Topic != nil:
		// Topics are namespace-scoped (RFC-0027): the destination inherits the
		// source function's namespace, exactly like function destinations (R6).
		return &asyncinvoke.Destination{FunctionNamespace: fnNamespace, Topic: ref.Topic.Topic, MQType: string(ref.Topic.MessageQueueType)}
	default:
		return nil
	}
}
