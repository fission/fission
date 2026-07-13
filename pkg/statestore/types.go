// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import "time"

// Scope carries tenancy. Every operation is namespaced to a Fission namespace,
// an owner object ("<kind>/<name>", e.g. "function/orders" or
// "workflowrun/abc123"), and a keyspace. Quota and authz are enforced above the
// driver, keyed on Scope.
type Scope struct {
	Namespace string
	Owner     string
	Keyspace  string
}

// Value is a KV read result: the stored bytes plus its monotonic version.
type Value struct {
	Data    []byte
	Version int64
}

// SetOptions controls a KV write.
//
// IfVersion selects concurrency behavior. Think of an absent key as version 0
// and each successful write as incrementing the version by one:
//
//   - nil            unconditional set (last write wins)
//   - non-nil, == 0  create-only (fails ErrVersionConflict if the key exists)
//   - non-nil, >  0  compare-and-swap (fails ErrVersionConflict unless the
//     current version equals *IfVersion)
//
// TTL == 0 means no expiry; otherwise the key expires TTL after the write and is
// never returned after expiry, even before a sweeper runs (invariant K2).
type SetOptions struct {
	IfVersion *int64
	TTL       time.Duration
}

// Page is an opaque forward-only pagination cursor.
type Page struct {
	Token string // "" means the first page.
	Limit int    // 0 means the driver default.
}

// KeyPage is a page of KV keys under a prefix.
type KeyPage struct {
	Keys []string
	Next string // "" means the last page.
}

// Event is one entry in an EventLog stream. On Append, Seq and At are assigned by
// the store (callers leave them zero). Payload is opaque bytes (JSON for the
// jsonb-backed drivers); Type is a short domain discriminator.
type Event struct {
	Seq     int64
	Type    string
	Payload []byte
	At      time.Time
}

// Message is an item to enqueue. Body is opaque; the consumer encodes its own
// envelope (function reference, headers, depth, ...) into it.
type Message struct {
	Body []byte
}

// LeasedMessage is a message delivered to a consumer under a lease.
//
// Receipt is a lease-scoped settle handle (the SQS ReceiptHandle model): it
// embeds the durable id AND the lease epoch, so Ack/Nack/Kill guard on the epoch
// and a stale delivery's settle cannot decide a newer lease's outcome (invariant
// Q2). ID is the durable message id (the value Enqueue returned); use it for
// correlation and idempotency, never for settling.
type LeasedMessage struct {
	ID       string
	Receipt  string
	Body     []byte
	Attempts int // Deliveries started so far, including this one.
}

// DeadMessage is a dead-lettered message, for DLQ inspection and redrive.
type DeadMessage struct {
	ID         string
	Body       []byte
	Reason     string
	Attempts   int
	EnqueuedAt time.Time
	DiedAt     time.Time
}

// EnqueueOptions controls an enqueue.
//
//   - Delay: the earliest lease time is now+Delay (0 means immediately leasable).
//   - DedupKey: if a not-yet-settled message with the same (queue, DedupKey)
//     already exists, Enqueue is a no-op that returns that message's id.
type EnqueueOptions struct {
	Delay    time.Duration
	DedupKey string
}
