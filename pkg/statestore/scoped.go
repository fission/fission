// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"errors"
	"time"
)

// NewScoped wraps a Capabilities so that every operation emits RFC-0019 metrics
// and KV writes are checked against the per-Scope quota from resolver. Enforcement
// lives here, above the driver, so tenancy is one interface-layer concern rather
// than a per-driver one. If the underlying Queue reports conservation stats, it
// is registered with the conservation drift gauge.
func NewScoped(inner Capabilities, resolver QuotaResolver) Capabilities {
	if resolver == nil {
		resolver = StaticQuota{}
	}
	// Conservation is a property of where the message accounting physically lives
	// (the store), not of a consumer, so the reporter is the store itself. A
	// networked client legitimately does not implement it — the embedded server
	// observes conservation on its own store.
	deregister := func() {}
	if rep, ok := inner.(ConservationReporter); ok {
		deregister = registerConservationReporter(rep)
	}
	return &scopedCaps{inner: inner, resolver: resolver, deregister: deregister}
}

type scopedCaps struct {
	inner      Capabilities
	resolver   QuotaResolver
	deregister func()
}

func (c *scopedCaps) KV() (KVStore, error) {
	kv, err := c.inner.KV()
	if err != nil {
		return nil, err
	}
	return &scopedKV{inner: kv, resolver: c.resolver}, nil
}

func (c *scopedCaps) EventLog() (EventLog, error) {
	el, err := c.inner.EventLog()
	if err != nil {
		return nil, err
	}
	return &meteredEventLog{inner: el}, nil
}

func (c *scopedCaps) Queue() (Queue, error) {
	q, err := c.inner.Queue()
	if err != nil {
		return nil, err
	}
	return &meteredQueue{inner: q}, nil
}

func (c *scopedCaps) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

func (c *scopedCaps) Close() error {
	c.deregister()
	return c.inner.Close()
}

// isBusinessOutcome reports whether err is an expected control-flow result rather
// than an operational failure, so the errors_total counter tracks real failures
// (IO, closed store) and not routine not-found/conflict/quota outcomes.
func isBusinessOutcome(err error) bool {
	switch {
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrVersionConflict),
		errors.Is(err, ErrQuotaExceeded),
		errors.Is(err, ErrInvalidReceipt):
		return true
	default:
		return false
	}
}

// observe records one operation and, when err is a real failure, one error.
func observe(ctx context.Context, capability, op string, err error) {
	recordOp(ctx, capability, op)
	if err != nil && !isBusinessOutcome(err) {
		recordError(ctx, capability, op)
	}
}

// scopedKV enforces the per-Scope quota on writes and emits metrics.
type scopedKV struct {
	inner    KVStore
	resolver QuotaResolver
}

func (k *scopedKV) Get(ctx context.Context, s Scope, key string) (Value, error) {
	v, err := k.inner.Get(ctx, s, key)
	observe(ctx, "kv", "get", err)
	return v, err
}

func (k *scopedKV) Set(ctx context.Context, s Scope, key string, val []byte, o SetOptions) error {
	q := k.resolver.Resolve(s)
	if q.MaxValueBytes > 0 && int64(len(val)) > q.MaxValueBytes {
		recordOp(ctx, "kv", "set")
		recordQuotaRejection(ctx, "value_bytes")
		return ErrQuotaExceeded
	}
	if q.MaxKeys > 0 {
		// Only a write that creates a new key can grow the count. Query live
		// state so expired keys (which the driver filters) never count. This is a
		// best-effort soft cap: the Get, countKeys, and Set are separate lock
		// acquisitions, so concurrent creates of distinct new keys can overshoot
		// by one. The authoritative bound belongs to the driver / RFC-0023
		// accountant; this catches the common single-writer case.
		if _, gerr := k.inner.Get(ctx, s, key); errors.Is(gerr, ErrNotFound) {
			n, cerr := k.countKeys(ctx, s)
			if cerr == nil && n >= q.MaxKeys {
				recordOp(ctx, "kv", "set")
				recordQuotaRejection(ctx, "keys")
				return ErrQuotaExceeded
			}
		}
	}
	err := k.inner.Set(ctx, s, key, val, o)
	observe(ctx, "kv", "set", err)
	return err
}

func (k *scopedKV) Delete(ctx context.Context, s Scope, key string, ifVersion int64) error {
	err := k.inner.Delete(ctx, s, key, ifVersion)
	observe(ctx, "kv", "delete", err)
	return err
}

func (k *scopedKV) List(ctx context.Context, s Scope, prefix string, page Page) (KeyPage, error) {
	kp, err := k.inner.List(ctx, s, prefix, page)
	observe(ctx, "kv", "list", err)
	return kp, err
}

// countKeys returns the number of live keys in scope, paging through List.
func (k *scopedKV) countKeys(ctx context.Context, s Scope) (int64, error) {
	var n int64
	page := Page{}
	for {
		kp, err := k.inner.List(ctx, s, "", page)
		if err != nil {
			return 0, err
		}
		n += int64(len(kp.Keys))
		if kp.Next == "" {
			return n, nil
		}
		page.Token = kp.Next
	}
}

// meteredEventLog adds metrics to an EventLog.
type meteredEventLog struct{ inner EventLog }

func (e *meteredEventLog) Append(ctx context.Context, stream string, expectedSeq int64, events []Event) (int64, error) {
	head, err := e.inner.Append(ctx, stream, expectedSeq, events)
	observe(ctx, "eventlog", "append", err)
	return head, err
}

func (e *meteredEventLog) Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]Event, error) {
	evs, err := e.inner.Read(ctx, stream, fromSeq, limit)
	observe(ctx, "eventlog", "read", err)
	return evs, err
}

func (e *meteredEventLog) Head(ctx context.Context, stream string) (int64, error) {
	head, err := e.inner.Head(ctx, stream)
	observe(ctx, "eventlog", "head", err)
	return head, err
}

func (e *meteredEventLog) Trim(ctx context.Context, stream string, belowSeq int64) error {
	err := e.inner.Trim(ctx, stream, belowSeq)
	observe(ctx, "eventlog", "trim", err)
	return err
}

// meteredQueue adds metrics to a Queue.
type meteredQueue struct{ inner Queue }

func (q *meteredQueue) Enqueue(ctx context.Context, queue string, msg Message, o EnqueueOptions) (string, error) {
	id, err := q.inner.Enqueue(ctx, queue, msg, o)
	observe(ctx, "queue", "enqueue", err)
	return id, err
}

func (q *meteredQueue) Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]LeasedMessage, error) {
	l, err := q.inner.Lease(ctx, queue, n, leaseFor)
	observe(ctx, "queue", "lease", err)
	return l, err
}

func (q *meteredQueue) Ack(ctx context.Context, receipt string) error {
	err := q.inner.Ack(ctx, receipt)
	observe(ctx, "queue", "ack", err)
	return err
}

func (q *meteredQueue) Nack(ctx context.Context, receipt string, retryAfter time.Duration) error {
	err := q.inner.Nack(ctx, receipt, retryAfter)
	observe(ctx, "queue", "nack", err)
	return err
}

func (q *meteredQueue) Kill(ctx context.Context, receipt string, reason string) error {
	err := q.inner.Kill(ctx, receipt, reason)
	observe(ctx, "queue", "kill", err)
	return err
}

func (q *meteredQueue) DeadLetters(ctx context.Context, queue string, page Page) ([]DeadMessage, error) {
	dl, err := q.inner.DeadLetters(ctx, queue, page)
	observe(ctx, "queue", "deadletters", err)
	return dl, err
}

func (q *meteredQueue) Redrive(ctx context.Context, queue string, ids []string) (int64, error) {
	n, err := q.inner.Redrive(ctx, queue, ids)
	observe(ctx, "queue", "redrive", err)
	return n, err
}

func (q *meteredQueue) Purge(ctx context.Context, queue string) (int64, error) {
	n, err := q.inner.Purge(ctx, queue)
	observe(ctx, "queue", "purge", err)
	return n, err
}

func (q *meteredQueue) Stats(ctx context.Context, queue string) (QueueStats, error) {
	st, err := q.inner.Stats(ctx, queue)
	observe(ctx, "queue", "stats", err)
	return st, err
}
