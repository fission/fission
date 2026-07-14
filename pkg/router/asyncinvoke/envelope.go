// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package asyncinvoke implements RFC-0024 asynchronous invocation for the router:
// the enqueue branch that durably accepts an async request (202 + invocation id)
// and the dispatcher that leases, delivers, and settles it at-least-once against
// the RFC-0021 statestore Queue.
//
// The invocation id returned to the caller is the statestore Queue's durable
// message id, so a dedup-collapsed enqueue returns the original id and the id is
// stable across retries and DLQ redrive. The Envelope therefore does not carry an
// id of its own; the dispatcher reads it from the leased message.
package asyncinvoke

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	// EnvelopeVersion is the wire-format version of the durable envelope. Phase 2
	// (destinations, result envelope) extends the format additively.
	EnvelopeVersion = "1.0"

	// DefaultQueue is the single global async-invocation queue. The namespace
	// travels in the envelope, so one queue serves every namespace: statestore's
	// SKIP LOCKED lets all router replicas lease it concurrently, and there is no
	// per-namespace queue discovery or orphan-queue draining problem.
	DefaultQueue = "asyncinv"

	// DefaultMaxBodyBytes is the async request-body cap (Lambda-parity 256KiB).
	// Oversized requests are rejected with 413, never spilled (RFC-0024 non-goal).
	DefaultMaxBodyBytes = 256 << 10
)

// Request/replay headers. The enqueue branch reads the first three from the
// incoming request; the dispatcher sets the last three on each delivery so the
// function (and downstream correlation) can see the invocation identity.
const (
	HeaderInvokeMode        = "X-Fission-Invoke-Mode"        // "async" opts a request into async mode
	HeaderDedupKey          = "X-Fission-Dedup-Key"          // idempotency key for enqueue collapse
	HeaderInvocationID      = "X-Fission-Invocation-Id"      // durable invocation id, replayed on delivery
	HeaderInvocationAttempt = "X-Fission-Invocation-Attempt" // 1-based delivery attempt, replayed on delivery
	HeaderInvocationDepth   = "X-Fission-Invocation-Depth"   // destination-chain depth, replayed on delivery
)

// InvokeModeAsync is the HeaderInvokeMode value that requests async invocation.
const InvokeModeAsync = "async"

// Policy is the resolved async delivery policy carried in the envelope, stamped
// from the function's InvocationConfig at enqueue so the dispatcher is
// self-contained (it never re-reads the Function, which may have changed or been
// deleted). Durations are nanoseconds on the wire; a zero field means the
// dispatcher's platform default.
type Policy struct {
	MaxAttempts int           `json:"maxAttempts,omitempty"`
	BackoffBase time.Duration `json:"backoffBase,omitempty"`
	BackoffCap  time.Duration `json:"backoffCap,omitempty"`
	MaxAge      time.Duration `json:"maxAge,omitempty"`
	NoJitter    bool          `json:"noJitter,omitempty"`
}

// Envelope is the durable, self-contained record of one asynchronous invocation.
// It carries everything the dispatcher needs to replay the request to the
// function's internal listener without re-resolving the trigger, so a delivery
// survives a router crash or redeploy. It is JSON-encoded into a statestore Queue
// message body.
type Envelope struct {
	// Version is EnvelopeVersion at enqueue time.
	Version string `json:"version"`
	// Namespace and Function identify the resolved backend (the async branch runs
	// after trigger resolution, so this is the concrete function, not a trigger).
	Namespace string `json:"namespace"`
	Function  string `json:"function"`
	// Method, Path, Query, Headers, and Body reproduce the original request. Path
	// and Query are captured for fidelity; Headers is the replay allowlist
	// (allowedHeaders). Body is opaque bytes (base64 on the wire).
	Method  string            `json:"method"`
	Path    string            `json:"path,omitempty"`
	Query   string            `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
	// EnqueueTime is when the request was accepted; the dispatcher measures MaxAge
	// from it.
	EnqueueTime time.Time `json:"enqueueTime"`
	// Depth is the destination-chain depth (0 for a direct caller); phase 2's
	// depth cap enforces against it. Carried now so phase 2 is additive.
	Depth int `json:"depth"`
	// FunctionTimeout is FunctionSpec.FunctionTimeout in seconds (0 = platform
	// default). The dispatcher bounds the per-delivery timeout by it and caps that
	// timeout strictly below the (fixed) lease duration, so a delivery's context
	// always expires before its lease (invariant A7).
	FunctionTimeout int `json:"functionTimeout"`
	// Policy is the resolved retry/age policy for this invocation, stamped from
	// the function's InvocationConfig at enqueue. Zero fields take dispatcher
	// defaults.
	Policy Policy `json:"policy,omitzero"`
}

// Encode marshals the envelope for a statestore Queue message body.
func (e Envelope) Encode() ([]byte, error) { return json.Marshal(e) }

// Decode parses an envelope from a leased statestore Queue message body.
func Decode(data []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(data, &e)
	return e, err
}

// allowedHeaders returns the subset of request headers to replay on async
// delivery. It is an allowlist, not a denylist: Content-Type and Accept (so the
// function can parse the body), plus caller-set X-* headers EXCEPT internal
// X-Fission-* control headers (which the dispatcher sets itself). Host,
// Content-Length, hop-by-hop, Cookie, and Authorization are intentionally dropped
// — an async invocation is decoupled from the caller's session. Multi-valued
// headers are comma-joined (the HTTP-canonical single-line form).
func allowedHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for name, vals := range h {
		if len(vals) == 0 {
			continue
		}
		canon := http.CanonicalHeaderKey(name)
		if !replayableHeader(canon) {
			continue
		}
		out[canon] = strings.Join(vals, ",")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func replayableHeader(canonicalName string) bool {
	switch canonicalName {
	case "Content-Type", "Accept":
		return true
	}
	if strings.HasPrefix(canonicalName, "X-Fission-") {
		return false
	}
	return strings.HasPrefix(canonicalName, "X-")
}
