// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import "errors"

var (
	// ErrVersionConflict is returned by KV compare-and-swap (Set/Delete with
	// IfVersion) and by EventLog.Append (expectedSeq mismatch) when the
	// optimistic check fails.
	ErrVersionConflict = errors.New("statestore: version conflict")
	// ErrNotFound is returned by KV Get for an absent or expired key.
	ErrNotFound = errors.New("statestore: not found")
	// ErrCapabilityUnavailable is returned by Capabilities accessors for a
	// capability the configured driver set does not provide.
	ErrCapabilityUnavailable = errors.New("statestore: capability unavailable")
	// ErrQuotaExceeded is returned by the scoped wrapper when a write would
	// exceed a key-count, value-byte, or namespace-byte quota.
	ErrQuotaExceeded = errors.New("statestore: quota exceeded")
	// ErrInvalidReceipt is returned by Queue settle methods for a malformed or
	// stale (wrong-epoch) lease receipt.
	ErrInvalidReceipt = errors.New("statestore: invalid or stale lease receipt")
	// ErrClosed is returned after the store has been closed.
	ErrClosed = errors.New("statestore: store closed")
)
