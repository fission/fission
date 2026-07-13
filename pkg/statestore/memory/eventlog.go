// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// streamState is one EventLog stream. head is the sequence of the last appended
// event; it is tracked separately from len(events) so Trim can drop old events
// without moving the append point.
type streamState struct {
	events []statestore.Event
	head   int64
}

// Append implements statestore.EventLog with optimistic concurrency on the head
// sequence: it succeeds only when expectedSeq equals the current head, so
// concurrent appenders get ErrVersionConflict instead of interleaving
// (invariant E1). It assigns each event a sequential Seq and the current time as
// At, and returns the new head.
func (s *Store) Append(_ context.Context, stream string, expectedSeq int64, events []statestore.Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, statestore.ErrClosed
	}
	st := s.streams[stream]
	if st == nil {
		st = &streamState{}
		s.streams[stream] = st
	}
	if st.head != expectedSeq {
		return st.head, statestore.ErrVersionConflict
	}
	now := time.Now()
	for _, e := range events {
		st.head++
		e.Seq = st.head
		e.At = now
		// Copy the payload so callers cannot mutate stored state.
		if e.Payload != nil {
			p := make([]byte, len(e.Payload))
			copy(p, e.Payload)
			e.Payload = p
		}
		st.events = append(st.events, e)
	}
	return st.head, nil
}

// Read implements statestore.EventLog: up to limit events with Seq > fromSeq, in
// order. limit <= 0 returns all matching events.
func (s *Store) Read(_ context.Context, stream string, fromSeq int64, limit int) ([]statestore.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, statestore.ErrClosed
	}
	st := s.streams[stream]
	if st == nil {
		return nil, nil
	}
	var out []statestore.Event
	for _, e := range st.events {
		if e.Seq <= fromSeq {
			continue
		}
		out = append(out, cloneEvent(e))
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Trim implements statestore.EventLog: drop events with Seq < belowSeq. The head
// is unchanged, so the append point is preserved.
func (s *Store) Trim(_ context.Context, stream string, belowSeq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	st := s.streams[stream]
	if st == nil {
		return nil
	}
	kept := st.events[:0:0]
	for _, e := range st.events {
		if e.Seq >= belowSeq {
			kept = append(kept, e)
		}
	}
	st.events = kept
	return nil
}

func cloneEvent(e statestore.Event) statestore.Event {
	if e.Payload != nil {
		p := make([]byte, len(e.Payload))
		copy(p, e.Payload)
		e.Payload = p
	}
	return e
}
