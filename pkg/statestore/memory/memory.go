// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package memory is the in-memory statestore driver: all three capabilities
// (KVStore, EventLog, Queue) behind plain mutex-guarded maps.
//
// It is the executable specification for the substrate — the shared conformance
// suite and the property-based tests treat it as ground truth — and it powers
// unit tests and the RFC-0018 `fission function run` local loop, so stateful
// functions work offline. It is single-process and non-durable by design.
package memory

import (
	"context"
	"sync"

	"github.com/fission/fission/pkg/statestore"
)

// register the driver so statestore.Open(ctx, Config{Driver: "memory"}) works.
// A process (or test) enables it with a blank import of this package.
func init() {
	statestore.Register("memory", func(context.Context, statestore.Config) (statestore.Capabilities, error) {
		return New()
	})
}

// Store is an in-memory Capabilities implementation. One mutex guards all three
// capabilities' state.
type Store struct {
	mu     sync.Mutex
	closed bool

	kv      map[kvKey]kvEntry
	streams map[string]*streamState
	// queues (Queue) state is added by its task.
}

// newStore returns an initialized, empty Store.
func newStore() *Store {
	return &Store{
		kv:      make(map[kvKey]kvEntry),
		streams: make(map[string]*streamState),
	}
}

// New returns an in-memory Capabilities. The error return keeps the signature
// uniform with drivers that can fail to open.
func New() (statestore.Capabilities, error) {
	return newStore(), nil
}

// KV returns the in-memory KVStore.
func (s *Store) KV() (statestore.KVStore, error) {
	return s, nil
}

// EventLog returns the in-memory EventLog.
func (s *Store) EventLog() (statestore.EventLog, error) {
	return s, nil
}

// Queue returns the in-memory Queue.
// TODO(rfc-0021 task 4): return s once the Queue methods are implemented.
func (s *Store) Queue() (statestore.Queue, error) {
	return nil, statestore.ErrCapabilityUnavailable
}

// Ping always succeeds for the in-memory store (unless closed).
func (s *Store) Ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	return nil
}

// Close marks the store closed; subsequent operations return ErrClosed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
