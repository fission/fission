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

// workspacePurger is the sweeper's dependency for purging a session's
// workspace at archive time (deletePrefix on pkg/agentruntime's own
// workspaceClient in production; a counting fake in tests). A nil
// workspacePurger means the workspace feature is disabled (STORAGESVC_URL
// unset — main.go passes a nil interface, never a typed-nil *workspaceClient,
// so this comparison is safe) and the sweeper skips the purge silently,
// exactly like every other workspace route's 503-vs-disabled stance.
type workspacePurger interface {
	deletePrefix(ctx context.Context, prefix string) (int, error)
}

// Sweeper ages active sessions to idle and idle sessions to archived, per
// the IdleAfter/ArchiveAfter policy resolved on each AgentEntry.
type Sweeper struct {
	logger    logr.Logger
	view      *AgentView
	store     *SessionStore
	hist      *HistoryStore
	wsPurger  workspacePurger // nil when the workspace feature is disabled; see workspacePurger's doc.
	interval  time.Duration   // AGENT_SWEEP_INTERVAL, default 30s (read in Start, injected here)
	retention time.Duration   // AGENT_ARCHIVE_RETENTION, default 168h (7d)
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
// keyspace), so hist is never dereferenced for such an agent. wsPurger is the
// sweeper's path to the archive-time workspace purge (see workspacePurger's
// doc for the nil-disables contract); unlike hist it is called unconditionally
// of StateKeyspace — a workspace artifact has nothing to do with whether the
// agent opted into history.
func NewSweeper(logger logr.Logger, view *AgentView, store *SessionStore, hist *HistoryStore, wsPurger workspacePurger, interval, retention time.Duration, now func() time.Time) *Sweeper {
	return &Sweeper{
		logger:    logger,
		view:      view,
		store:     store,
		hist:      hist,
		wsPurger:  wsPurger,
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
	// Clamp to the stream's own head: CoveredThroughSeq lives in the
	// checkpoint record, which is writable by anything holding the
	// keyspace's bearer token (the same credential the SDK's history writer
	// holds), so a forged value must not trim past what actually exists.
	head, err := s.hist.Head(ctx, entry.Namespace, entry.StateKeyspace, rec.ID)
	if err != nil {
		s.logSweepErr(err, "sweeper: history head lookup for knob trim failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		return
	}
	if head == 0 {
		return
	}
	if err := s.hist.TrimBelow(ctx, entry.Namespace, entry.StateKeyspace, rec.ID, min(ckpt.CoveredThroughSeq, head)+1); err != nil {
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

	// Head + checkpoint-version capture: after the fresh-Get re-validation
	// above (so it never runs for a record the sweeper just skipped) and
	// before store.Archive below. A Head error logs and proceeds with
	// headAtDecision = 0, which disables the post-archive TRIM below but
	// never blocks the archive itself. The checkpoint version is captured
	// here too — not re-read after the archive commits — so the post-archive
	// delete can be CAS-bound at the version that existed at decision time:
	// a same-id session re-created in the archive window writes a checkpoint
	// at a NEW version, and the CAS-delete below then fails instead of
	// erasing it.
	var headAtDecision int64
	var ckptVersion int64
	var ckptExisted bool
	if entry.StateKeyspace != "" {
		var headErr error
		headAtDecision, headErr = s.hist.Head(ctx, entry.Namespace, entry.StateKeyspace, rec.ID)
		if headErr != nil {
			s.logSweepErr(headErr, "sweeper: history head lookup before archive failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
			headAtDecision = 0
		}
		v, ckptErr := s.hist.GetCheckpointVersion(ctx, entry.Namespace, entry.StateKeyspace, rec.ID)
		switch {
		case ckptErr == nil:
			ckptVersion = v
			ckptExisted = true
		case errors.Is(ckptErr, statestore.ErrNotFound):
			// No checkpoint at decision time: nothing to delete below.
		default:
			s.logSweepErr(ckptErr, "sweeper: get checkpoint version before archive failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
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

	// Workspace purge: fires on every successful archive CAS, BEFORE the
	// entry.StateKeyspace=="" early return below — deliberately, not merely
	// placed early. A workspace artifact is independent of whether the agent
	// opted into history/state (StateKeyspace); putting this purge beside the
	// history/checkpoint cleanup further down would nest it inside that early
	// return and it would never run for a state-less agent (plan-review
	// finding on this slice).
	s.purgeWorkspace(ctx, entry, rec)

	if entry.StateKeyspace == "" {
		return
	}

	// History trim, bounded by headAtDecision (the PRE-archive head — never a
	// re-read current head, so a same-id session re-created in the archive
	// window keeps its first events). headAtDecision <= 0 (no history, or a
	// Head error above) means nothing to trim. Trim/delete errors here are
	// logged and NOT retried: orphan history/checkpoint residue is accepted,
	// covered by RFC-0027's deferred orphan-stream age sweep.
	if headAtDecision > 0 {
		if err := s.hist.TrimBelow(ctx, entry.Namespace, entry.StateKeyspace, rec.ID, headAtDecision+1); err != nil {
			s.logSweepErr(err, "sweeper: trimming archived session history failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		}
	}

	// Checkpoint delete: gated only on whether a checkpoint existed AT THE
	// PRE-ARCHIVE CAPTURE (not on headAtDecision — a session with a
	// checkpoint but zero history events, or a Head error captured above,
	// must not leak its checkpoint forever). CAS-bound at the captured
	// version: a fresh checkpoint written in the archive window fails the
	// CAS and is left in place rather than erased.
	if ckptExisted {
		if err := s.hist.DeleteCheckpointIfVersion(ctx, entry.Namespace, entry.StateKeyspace, rec.ID, ckptVersion); err != nil {
			if errors.Is(err, statestore.ErrVersionConflict) {
				s.logger.V(1).Info("sweeper: checkpoint delete skipped, fresh checkpoint appeared", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
				return
			}
			s.logSweepErr(err, "sweeper: deleting archived session checkpoint failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		}
	}
}

// purgeWorkspace deletes every workspace artifact under rec's session prefix,
// called once per successful archive CAS (see the call site in archive above
// for why this must run BEFORE the StateKeyspace=="" early return). A nil
// wsPurger means the workspace feature is disabled (STORAGESVC_URL unset) —
// skipped silently, not an error: an install that never turned workspace on
// has nothing to purge.
//
// Discipline mirrors this sweeper's own stated stance exactly (package doc):
// idempotent (storagesvc's prefix-delete is itself an idempotent bulk delete,
// so two replicas racing the same purge, or a retry on a later sweep pass
// that never happens because this is fire-once, both land safely), log-don't-
// retry on error, and orphan residue accepted. Two residue classes are
// therefore accepted here, not just one:
//   - a per-item delete failure inside storagesvc's own prefix-delete (its
//     own log-don't-retry stance, see pkg/storagesvc/workspace.go); and
//   - the GC-anchor residual class named in this feature's Global
//     Constraints: a session record that TTLs out unswept (its agent
//     Function deleted before the sweeper ever archived it) never reaches
//     this call at all, and the archive pruner excludes the workspace root
//     entirely, so that record's workspace objects are orphaned permanently.
//     That class joins the already-named orphan-sweep follow-up (RFC-0027's
//     deferred orphan-stream age sweep, referenced above for history/
//     checkpoint residue) — not solved here.
//
// The purge-vs-recreated-session race (a new session minted under the same
// id in the window between this archive and the purge landing) is also
// accepted: workspace is scratch, not truth, and unlike a checkpoint/history
// delete there is no version to CAS a prefix-delete against, so no head-
// capture analog is possible here.
func (s *Sweeper) purgeWorkspace(ctx context.Context, entry AgentEntry, rec SessionRecord) {
	if s.wsPurger == nil {
		return
	}
	prefix := workspaceSessionPrefix(entry.Namespace, entry.Name, rec.ID)
	deleted, err := s.wsPurger.deletePrefix(ctx, prefix)
	if err != nil {
		s.logSweepErr(err, "sweeper: workspace purge failed", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID)
		return
	}
	s.logger.V(1).Info("sweeper: workspace purged", "namespace", entry.Namespace, "agent", entry.Name, "session", rec.ID, "deleted", deleted)
}
