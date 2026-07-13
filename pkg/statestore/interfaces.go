// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"time"
)

// KVStore is keyed durable state with compare-and-swap and TTL.
type KVStore interface {
	// Get returns the value for key, or ErrNotFound if it is absent or expired.
	Get(ctx context.Context, s Scope, key string) (Value, error)
	// Set writes val for key subject to the concurrency and TTL semantics in o.
	// A failed CAS returns ErrVersionConflict.
	Set(ctx context.Context, s Scope, key string, val []byte, o SetOptions) error
	// Delete removes key. ifVersion <= 0 deletes unconditionally; a positive
	// ifVersion is a CAS delete that returns ErrVersionConflict on mismatch.
	Delete(ctx context.Context, s Scope, key string, ifVersion int64) error
	// List returns a page of keys under prefix, ordered lexicographically.
	List(ctx context.Context, s Scope, prefix string, page Page) (KeyPage, error)
}

// EventLog is an append-only, ordered, replayable stream with optimistic
// concurrency: Append succeeds only when expectedSeq equals the stream head, so
// concurrent appenders get ErrVersionConflict instead of interleaving.
type EventLog interface {
	// Append atomically appends events when expectedSeq equals the current head
	// sequence, returning the new head sequence. A mismatch returns
	// ErrVersionConflict and appends nothing.
	Append(ctx context.Context, stream string, expectedSeq int64, events []Event) (int64, error)
	// Read returns up to limit events with Seq > fromSeq, in order.
	Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]Event, error)
	// Trim drops events with Seq < belowSeq (history GC).
	Trim(ctx context.Context, stream string, belowSeq int64) error
}

// Queue is an at-least-once work queue with visibility-timeout leases and a
// dead-letter table.
//
// The settle methods (Ack/Nack/Kill) take the lease Receipt, not the durable
// message id. This is the one deliberate rename from the RFC-0021 sketch's
// Ack(id) so the epoch-guard correspondence (queue.tla invariant I2 / RFC-0021
// invariant Q2) is legible in the type system: a Receipt is valid only for the
// lease that produced it. DeadLetters and Redrive operate on durable ids.
type Queue interface {
	// Enqueue adds msg and returns its durable id. With o.DedupKey set, an
	// existing not-yet-settled message with the same key collapses the enqueue
	// and its id is returned.
	Enqueue(ctx context.Context, queue string, msg Message, o EnqueueOptions) (string, error)
	// Lease atomically leases up to n currently-visible messages for leaseFor,
	// making them invisible until the lease expires or they are settled.
	Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]LeasedMessage, error)
	// Ack settles a delivery as succeeded. A stale or malformed receipt returns
	// ErrInvalidReceipt and changes nothing.
	Ack(ctx context.Context, receipt string) error
	// Nack settles a delivery as failed and requeues it after retryAfter, unless
	// the attempt budget is spent, in which case the message is dead-lettered.
	Nack(ctx context.Context, receipt string, retryAfter time.Duration) error
	// Kill dead-letters a delivery immediately with reason (permanent failure).
	Kill(ctx context.Context, receipt string, reason string) error
	// DeadLetters returns a page of dead-lettered messages for queue.
	DeadLetters(ctx context.Context, queue string, page Page) ([]DeadMessage, error)
	// Redrive re-enqueues dead-lettered messages by durable id with attempts
	// reset.
	Redrive(ctx context.Context, queue string, ids []string) error
}

// Capabilities is the driver set a component opens once at start. A consumer asks
// for exactly the capabilities it needs and fails fast at startup if one is not
// configured.
type Capabilities interface {
	// KV returns the KVStore capability or ErrCapabilityUnavailable.
	KV() (KVStore, error)
	// EventLog returns the EventLog capability or ErrCapabilityUnavailable.
	EventLog() (EventLog, error)
	// Queue returns the Queue capability or ErrCapabilityUnavailable.
	Queue() (Queue, error)
	// Ping is a health affordance for a consumer's /readyz gate.
	Ping(ctx context.Context) error
	// Close releases the driver's resources.
	Close() error
}
