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

// defaultMaxAttempts is the queue attempt budget before a Nack dead-letters a
// message, matching RFC-0024's default retry policy.
const defaultMaxAttempts = 3

// Store is an in-memory Capabilities implementation. One mutex guards all three
// capabilities' state.
type Store struct {
	mu     sync.Mutex
	closed bool

	kv          map[kvKey]kvEntry
	streams     map[string]*streamState
	queues      map[string]*queueState
	maxAttempts int
}

// newStore returns an initialized, empty Store.
func newStore() *Store {
	return &Store{
		kv:          make(map[kvKey]kvEntry),
		streams:     make(map[string]*streamState),
		queues:      make(map[string]*queueState),
		maxAttempts: defaultMaxAttempts,
	}
}

// Option configures an in-memory Store.
type Option func(*Store)

// WithMaxAttempts sets the queue attempt budget (deliveries before a Nack
// dead-letters). n <= 0 is ignored.
func WithMaxAttempts(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.maxAttempts = n
		}
	}
}

// New returns an in-memory Capabilities. The error return keeps the signature
// uniform with drivers that can fail to open.
func New(opts ...Option) (statestore.Capabilities, error) {
	s := newStore()
	for _, o := range opts {
		o(s)
	}
	return s, nil
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
func (s *Store) Queue() (statestore.Queue, error) {
	return s, nil
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
