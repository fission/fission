// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// ErrBodyTooLarge is returned by Enqueue when the request body exceeds the cap;
// the router maps it to 413. Any OTHER Enqueue error is an enqueue failure the
// router maps to 503 — invariant A1: a 202 is returned only after the message is
// durably enqueued, never a silently dropped request.
var ErrBodyTooLarge = errors.New("asyncinvoke: request body exceeds limit")

// Params are the per-request enqueue inputs the router resolves before the async
// branch. Namespace/Function/FunctionTimeout come from the resolved backend, the
// dedup key and depth from replay headers.
type Params struct {
	Namespace       string
	Function        string
	FunctionTimeout int // seconds; 0 = platform default
	Depth           int
	DedupKey        string
	// Policy is the resolved retry/age policy stamped into the envelope (zero
	// fields take dispatcher defaults).
	Policy Policy
	// OnSuccess/OnFailure are the resolved destinations stamped into the envelope
	// (nil = none), so the dispatcher fires them without re-reading the Function.
	OnSuccess *Destination
	OnFailure *Destination
	// QueueName defaults to DefaultQueue when empty. MaxBodyBytes defaults to
	// DefaultMaxBodyBytes when <= 0.
	QueueName    string
	MaxBodyBytes int64
}

// Enqueue reads the request body under a cap, builds the durable Envelope, and
// enqueues it, returning the durable invocation id (the statestore message id).
// The caller writes the HTTP response: 202 {invocationId} on success, 413 on
// ErrBodyTooLarge, 503 on any other error.
//
// The body is read under http.MaxBytesReader BEFORE the envelope is built, so an
// oversized request is rejected without buffering it whole, and a mid-body read
// failure produces an error rather than a partial enqueue (invariant A1).
func Enqueue(ctx context.Context, q statestore.Queue, w http.ResponseWriter, r *http.Request, p Params) (string, error) {
	body, err := readCappedBody(w, r, p.MaxBodyBytes)
	if err != nil {
		return "", err
	}
	env := Envelope{
		Version:         EnvelopeVersion,
		Namespace:       p.Namespace,
		Function:        p.Function,
		Method:          r.Method,
		Path:            r.URL.Path,
		Query:           r.URL.RawQuery,
		Headers:         allowedHeaders(r.Header),
		Body:            body,
		EnqueueTime:     time.Now(),
		Depth:           p.Depth,
		FunctionTimeout: p.FunctionTimeout,
		Policy:          p.Policy,
		OnSuccess:       p.OnSuccess,
		OnFailure:       p.OnFailure,
	}
	queue := p.QueueName
	if queue == "" {
		queue = DefaultQueue
	}
	return encodeAndEnqueue(ctx, q, queue, env, statestore.EnqueueOptions{DedupKey: p.DedupKey})
}

// encodeAndEnqueue is the single write path for the durable envelope wire-shape:
// it encodes env and enqueues it on queueName, returning the durable message id.
// Both the initial enqueue and the dispatcher's destination fire go through it so
// the persist step lives in one place. The wrapped error distinguishes an encode
// failure from an enqueue failure for the caller's log/metric.
func encodeAndEnqueue(ctx context.Context, q statestore.Queue, queueName string, env Envelope, opts statestore.EnqueueOptions) (string, error) {
	data, err := env.Encode()
	if err != nil {
		return "", fmt.Errorf("asyncinvoke: encoding envelope: %w", err)
	}
	id, err := q.Enqueue(ctx, queueName, statestore.Message{Body: data}, opts)
	if err != nil {
		return "", fmt.Errorf("asyncinvoke: enqueue: %w", err)
	}
	return id, nil
}

// readCappedBody reads r.Body under a MaxBytesReader, mapping an over-limit read
// to ErrBodyTooLarge and any other read failure to a wrapped error (never a
// partial body). A nil ResponseWriter is accepted (unit tests) — MaxBytesReader
// only uses it to flag the connection, and tolerates nil.
func readCappedBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	if r.Body == nil {
		return nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, ErrBodyTooLarge
		}
		return nil, fmt.Errorf("asyncinvoke: reading request body: %w", err)
	}
	return body, nil
}
