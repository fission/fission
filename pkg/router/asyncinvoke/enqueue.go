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
	}
	data, err := env.Encode()
	if err != nil {
		return "", fmt.Errorf("asyncinvoke: encoding envelope: %w", err)
	}
	queue := p.QueueName
	if queue == "" {
		queue = DefaultQueue
	}
	id, err := q.Enqueue(ctx, queue, statestore.Message{Body: data}, statestore.EnqueueOptions{DedupKey: p.DedupKey})
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
