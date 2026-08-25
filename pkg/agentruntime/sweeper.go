// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// sweeper.go implements Sweeper, the lifecycle sweeper that ages active
// sessions to idle and idle sessions to archived.
//
// The sweeper runs on every replica with no leader election. Each replica
// pages through every agent's live sessions on its own timer and drives the
// same two transitions the request dispatcher would drive on a turn:
// active -> idle (Update) and idle -> archived (Archive). Both writes are
// CAS at the version the sweeper just read. When two replicas race the same
// record, or a live turn touches it between the sweeper's read and its
// write, the loser's CAS fails with statestore.ErrVersionConflict and the
// sweeper skips that record silently — it never retries within the same
// sweep, and it never deletes anything (archived and orphaned-live records
// expire on their own TTLs). That CAS idempotence is what lets every
// replica sweep independently and correctly: whichever write lands first
// wins, and the loser's no-op is indistinguishable from "nothing to do
// here" from the sweeper's point of view. This is the substitute for leader
// election in v1.
package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/statestore"
)

// sweepPageLimit is the per-agent List page size.
const sweepPageLimit = 500

// Sweeper ages active sessions to idle and idle sessions to archived, per
// the IdleAfter/ArchiveAfter policy resolved on each AgentEntry.
type Sweeper struct {
	logger    logr.Logger
	view      *AgentView
	store     *SessionStore
	hist      *HistoryStore
	interval  time.Duration // AGENT_SWEEP_INTERVAL, default 30s (read in Start, injected here)
	retention time.Duration // AGENT_ARCHIVE_RETENTION, default 168h (7d)
	now       func() time.Time

	// beforeCAS, when non-nil, runs immediately after the sweeper re-reads a
	// record's authoritative version and before it issues the CAS write. It
	// is a test-only seam for landing a race in that window (another writer
	// mutating the record) that would otherwise require real concurrency to
	// exercise deterministically. Production code never sets this.
	beforeCAS func()
}

// NewSweeper returns a Sweeper that ages sessions in store per the policy on
// each entry in view, using now as its clock. hist is the sweeper's only path
// to session history/checkpoint cleanup (archive-time trim + the
// HistoryTrim-knob live-session trim); every call into it is guarded by
// entry.StateKeyspace != "" (history is disabled for an agent with no state
// keyspace), so hist is never dereferenced for such an agent.
func NewSweeper(logger logr.Logger, view *AgentView, store *SessionStore, hist *HistoryStore, interval, retention time.Duration, now func() time.Time) *Sweeper {
	return &Sweeper{
		logger:    logger,
		view:      view,
		store:     store,
		hist:      hist,
		interval:  interval,
		retention: retention,
		now:       now,
	}
}

// Run loops sweepOnce every s.interval until ctx is done.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce sweeps every agent in the view once.
func (s *Sweeper) sweepOnce(ctx context.Context) {
	for _, entry := range s.view.List() {
		s.sweepAgent(ctx, entry)
	}
}

// logSweepErr logs a sweep-path error, unless it is context.Canceled — a
// canceled context is the expected shape of Run's ctx.Done() shutdown
// racing an in-flight store call, not a failure worth an Error-level log.
func (s *Sweeper) logSweepErr(err error, msg string, keysAndValues ...any) {
	if errors.Is(err, context.Canceled) {
		s.logger.V(1).Info("sweeper: context canceled, stopping", append(keysAndValues, "cause", msg)...)
		return
	}
	s.logger.Error(err, msg, keysAndValues...)
}

// sweepAgent pages through one agent's live sessions, applying the idle and
// archive transitions where due. liveTTL (entry.IdleAfter + entry.ArchiveAfter
// + s.retention, per the controller's Update signature) is passed to every
// idle transition so an orphaned record still self-expires with no sweeper
// running.
func (s *Sweeper) sweepAgent(ctx context.Context, entry AgentEntry) {
	liveTTL := entry.IdleAfter + entry.ArchiveAfter + s.retention
	pageToken := ""
	for {
		if ctx.Err() != nil {
			return
		}
		recs, next, err := s.store.List(ctx, entry.Namespace, entry.Name, pageToken, sweepPageLimit)
		if err != nil {
			s.logSweepErr(err, "sweeper: list failed", "namespace", entry.Namespace, "agent", entry.Name)
			return
		}
		for _, rec := range recs {
			if ctx.Err() != nil {
				return
			}
			s.sweepRecord(ctx, entry, rec, liveTTL)
		}
		if next == "" {
			return
		}
		pageToken = next
	}
}

// sweepRecord applies the one lifecycle transition, if any, that rec's
// listed snapshot is due for, and separately (independent of any
// transition) the HistoryTrim-knob live-session trim. The actual CAS
// re-reads the record immediately beforehand (the re-Get discipline: use the
// version from that read, not the List page's snapshot) so the write always
// targets the current data.
func (s *Sweeper) sweepRecord(ctx context.Context, entry AgentEntry, rec SessionRecord, liveTTL time.Duration) {
	if entry.HistoryTrim && entry.StateKeyspace != "" {
		s.trimToCheckpoint(ctx, entry, rec)
	}
	idleFor := s.now().Sub(rec.LastActiveAt)
	switch {
	case rec.Status == StatusActive && idleFor >= entry.IdleAfter:
		s.idle(ctx, entry, rec, liveTTL)
	case rec.Status == StatusIdle && idleFor >= entry.ArchiveAfter:
		s.archive(ctx, entry, rec)
	}
}

// trimToCheckpoint trims rec's history below its checkpoint's coverage
// boundary, for a live session on an agent that opted into
// HistoryTrimBelowCheckpoint (entry.HistoryTrim). It is called on every
// sweep visit, unconditionally of any idle/archive transition: it carries no
// memo state across passes, and TrimBelow is idempotent, so re-trimming to
// the same (or an unchanged) boundary on a later pass is a no-op cost, never
// a correctness issue. A session with no checkpoint yet
// (statestore.ErrNotFound) is skipped silently — nothing to trim to.
func (s *Sweeper) trimToCheckpoint(ctx context.Context, entry AgentEntry, rec SessionRecord) {
	ckpt, err := s.hist.GetCheckpoint(ctx, entry.Namespace, entry.StateKeyspace, rec.ID)
	if err != nil {
		if !errors.Is(err, statestore.ErrNotFound) {
			s.logSweepErr(err, "sweeper: get checkpoint for history trim failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		}
		return
	}
	if ckpt.CoveredThroughSeq <= 0 {
		return
	}
	if err := s.hist.TrimBelow(ctx, entry.Namespace, entry.StateKeyspace, rec.ID, ckpt.CoveredThroughSeq+1); err != nil {
		s.logSweepErr(err, "sweeper: knob history trim failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
	}
}

// idle CAS-transitions rec to StatusIdle. A version conflict means another
// replica or a live turn already moved it — skip silently, no retry.
func (s *Sweeper) idle(ctx context.Context, entry AgentEntry, rec SessionRecord, liveTTL time.Duration) {
	fresh, version, err := s.store.Get(ctx, entry.Namespace, entry.Name, rec.ID)
	if err != nil {
		if !errors.Is(err, statestore.ErrNotFound) {
			s.logSweepErr(err, "sweeper: get before idle transition failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		}
		return
	}
	// Re-validate against the fresh read: a turn may have touched the
	// record between the List page's snapshot and this Get (the CAS below
	// only guards the Get-to-write window, not this one).
	if fresh.Status != StatusActive || s.now().Sub(fresh.LastActiveAt) < entry.IdleAfter {
		return
	}
	if s.beforeCAS != nil {
		s.beforeCAS()
	}
	fresh.Status = StatusIdle
	if err := s.store.Update(ctx, fresh, version, liveTTL); err != nil {
		if errors.Is(err, statestore.ErrVersionConflict) {
			s.logger.V(1).Info("sweeper: idle transition skipped, CAS conflict", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
			return
		}
		s.logSweepErr(err, "sweeper: idle transition failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
	}
}

// archive CAS-transitions rec to the archived keyspace. A version conflict
// means a concurrent turn revived the session (SessionStore.Archive aborts
// on its CAS-delete conflict, cleaning up its speculative archived write) —
// skip silently, no retry; the live record is left exactly as the reviver
// left it.
func (s *Sweeper) archive(ctx context.Context, entry AgentEntry, rec SessionRecord) {
	fresh, version, err := s.store.Get(ctx, entry.Namespace, entry.Name, rec.ID)
	if err != nil {
		if !errors.Is(err, statestore.ErrNotFound) {
			s.logSweepErr(err, "sweeper: get before archive transition failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		}
		return
	}
	// Re-validate against the fresh read: a turn may have revived the
	// record between the List page's snapshot and this Get (the CAS below
	// only guards the Get-to-write window, not this one) — without this
	// check a just-revived session would be archived out from under it.
	if fresh.Status != StatusIdle || s.now().Sub(fresh.LastActiveAt) < entry.ArchiveAfter {
		return
	}
	if s.beforeCAS != nil {
		s.beforeCAS()
	}

	// Head capture: after the fresh-Get re-validation above (so it never runs
	// for a record the sweeper just skipped) and before store.Archive below.
	// A Head error logs and proceeds with headAtDecision = 0, which disables
	// the post-archive trim below but never blocks the archive itself.
	var headAtDecision int64
	if entry.StateKeyspace != "" {
		var headErr error
		headAtDecision, headErr = s.hist.Head(ctx, entry.Namespace, entry.StateKeyspace, rec.ID)
		if headErr != nil {
			s.logSweepErr(headErr, "sweeper: history head lookup before archive failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
			headAtDecision = 0
		}
	}

	if err := s.store.Archive(ctx, fresh, version, s.retention); err != nil {
		if errors.Is(err, statestore.ErrVersionConflict) {
			s.logger.V(1).Info("sweeper: archive skipped, CAS conflict (revived)", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
			return
		}
		s.logSweepErr(err, "sweeper: archive failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		return
	}

	// History/checkpoint cleanup only after a committed archive (a
	// CAS-conflicted or errored archive above already returned, leaving
	// history and the checkpoint untouched). Bounded by headAtDecision, the
	// PRE-archive head — never a re-read current head, so a same-id session
	// re-created in the archive window keeps its first events. Trim/delete
	// errors here are logged and NOT retried: orphan history/checkpoint
	// residue is accepted, covered by RFC-0027's deferred orphan-stream age
	// sweep.
	if entry.StateKeyspace == "" || headAtDecision <= 0 {
		return
	}
	if err := s.hist.TrimBelow(ctx, entry.Namespace, entry.StateKeyspace, rec.ID, headAtDecision+1); err != nil {
		s.logSweepErr(err, "sweeper: trimming archived session history failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
	}
	if err := s.hist.DeleteCheckpoint(ctx, entry.Namespace, entry.StateKeyspace, rec.ID); err != nil {
		s.logSweepErr(err, "sweeper: deleting archived session checkpoint failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
	}
}
