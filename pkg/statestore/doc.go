// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package statestore is a small, pluggable interface layer for durable
// transactional state in the Fission control plane (RFC-0021).
//
// It exposes three narrow capability interfaces — [KVStore] (keyed get/set/CAS
// with TTL), [EventLog] (append-only ordered streams with optimistic
// concurrency), and [Queue] (at-least-once work queue with visibility-timeout
// leases and a dead-letter table) — above swappable drivers. Consumers hold the
// capability interfaces and are byte-identical across deployment modes, never
// knowing whether an in-memory map, Postgres, an embedded SQLite store, or an
// HTTP client to that store is behind them.
//
// The in-memory driver (pkg/statestore/memory) is the executable specification:
// the shared conformance suite (pkg/statestore/statestoretest) and the
// property-based tests treat it as ground truth, so every other driver's
// observable behavior is checked to equal it.
//
// Two of the protocols are genuinely concurrent and are pinned by machine-checked
// TLA+ specifications in docs/rfc/specs/:
//
//   - The queue lease/settle lifecycle (queue.tla). Settles are guarded by a
//     lease epoch — a stale delivery can never decide a newer lease's outcome
//     (invariant Q2). This is why [LeasedMessage.Receipt] embeds the lease epoch
//     and [Queue.Ack]/[Queue.Nack]/[Queue.Kill] take that receipt, not the
//     durable message id.
//   - The EventLog CAS-append fold (workflowfold.tla). [EventLog.Append] admits
//     exactly one writer per sequence slot, so streams are gap-free and
//     append-ordered (invariant E1).
//
// This package is the shared substrate for durable workflows (RFC-0022),
// stateful functions (RFC-0023), and async invocation with retries/DLQ
// (RFC-0024). It is deliberately not built on pkg/storagesvc, which is a
// blob-shaped archive service.
package statestore
