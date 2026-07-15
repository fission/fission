// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"sync"
	"time"
)

// Retention tuning (RFC-0027). The reaper trims each subscribed stream to the
// minimum committed cursor (E3: no live subscriber loses an unconsumed event),
// with two backstops that deliberately override a stalled subscriber's floor —
// documented, bounded loss, identical in kind to a broker's retention evicting
// a lagging consumer group:
const (
	// reaperInterval paces retention ticks.
	reaperInterval = time.Minute
	// maxStreamAge is the age backstop: events older than this are trimmed even
	// if a stalled subscriber has not consumed them.
	maxStreamAge = 7 * 24 * time.Hour
	// maxStreamEvents is the size backstop: a stream's retained backlog never
	// exceeds this many events.
	maxStreamEvents = 100_000
	// ageScanPages bounds the age-backstop scan per stream per tick, so one huge
	// backlog cannot monopolize a tick (the scan resumes next tick).
	ageScanPages = 10
)

// subscriptionSet tracks the provider's live subscriptions for the reaper.
type subscriptionSet struct {
	mu   sync.Mutex
	subs map[*subscription]struct{}
}

func newSubscriptionSet() *subscriptionSet {
	return &subscriptionSet{subs: map[*subscription]struct{}{}}
}

func (ss *subscriptionSet) add(s *subscription) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.subs[s] = struct{}{}
}

func (ss *subscriptionSet) remove(s *subscription) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.subs, s)
}

// byStream snapshots the minimum committed cursor per stream over STARTED
// subscriptions (one stream can have several triggers).
func (ss *subscriptionSet) byStream() map[string]int64 {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	min := map[string]int64{}
	for s := range ss.subs {
		if !s.started.Load() {
			continue
		}
		c := s.committed.Load()
		if cur, ok := min[s.stream]; !ok || c < cur {
			min[s.stream] = c
		}
	}
	return min
}

// reaperOnce starts the retention loop the first time a subscription appears.
// The loop is bound to the provider's lifetime via the given context's PARENT
// semantics deliberately not being used: it runs on its own stop channel so a
// leadership transition (which cancels subscription contexts) does not kill
// retention while the process lives; with no live subscriptions a tick is a
// no-op.
func (s *Statestore) reaperOnce(_ context.Context) {
	s.reaperStart.Do(func() {
		go s.runReaper()
	})
}

func (s *Statestore) runReaper() {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.reaperStop:
			return
		case <-ticker.C:
			s.reapTick(context.Background())
		}
	}
}

// reapTick trims every subscribed stream. Per stream, the trim point is the max
// of three candidates: the min committed cursor (exact, loss-free), the age
// backstop, and the size backstop (both documented loss for stalled consumers).
func (s *Statestore) reapTick(ctx context.Context) {
	for stream, minCursor := range s.subs.byStream() {
		s.reapStream(ctx, stream, minCursor)
	}
}

func (s *Statestore) reapStream(ctx context.Context, stream string, minCursor int64) {
	// Current floor: the seq just below the first retained event (== head when
	// the stream is fully trimmed or empty).
	head, err := s.el.Head(ctx, stream)
	if err != nil {
		s.logger.Error(err, "reaper: reading stream head", "stream", stream)
		return
	}
	first, err := s.el.Read(ctx, stream, 0, 1)
	if err != nil {
		s.logger.Error(err, "reaper: reading stream floor", "stream", stream)
		return
	}
	floor := head
	if len(first) == 1 {
		floor = first[0].Seq - 1
	}

	// Candidate 1 — min-cursor (loss-free): everything at or below the slowest
	// subscriber's committed cursor is consumed.
	trimTo := minCursor // trim events with Seq <= trimTo
	reason := "mincursor"

	// Candidate 2 — size backstop.
	if sizeTo := head - s.reaperMaxEvents; sizeTo > trimTo {
		trimTo, reason = sizeTo, "size"
	}

	// Candidate 3 — age backstop: bounded forward scan for events older than the
	// cutoff (resumes next tick if the backlog is huge).
	cutoff := time.Now().Add(-s.reaperMaxAge)
	ageTo := floor
	from := floor
scan:
	for range ageScanPages {
		evs, rerr := s.el.Read(ctx, stream, from, readBatch)
		if rerr != nil {
			s.logger.Error(rerr, "reaper: age scan", "stream", stream)
			break
		}
		if len(evs) == 0 {
			break
		}
		for _, ev := range evs {
			if !ev.At.Before(cutoff) {
				break scan
			}
			ageTo = ev.Seq
		}
		from = evs[len(evs)-1].Seq
	}
	if ageTo > trimTo {
		trimTo, reason = ageTo, "age"
	}

	if trimTo <= floor {
		return // nothing new to trim
	}
	if err := s.el.Trim(ctx, stream, trimTo+1); err != nil {
		s.logger.Error(err, "reaper: trimming stream", "stream", stream, "below", trimTo+1)
		return
	}
	recordTrimmed(ctx, reason, trimTo-floor)
	s.logger.V(1).Info("reaper: trimmed topic stream",
		"stream", stream, "events", trimTo-floor, "reason", reason)
}
