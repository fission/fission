// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// wake.go implements WakeService, the agent-wake queue that delivers
// yield=continue self-continuation turns: DispatchTurn's continue-chain seam
// (Dispatcher.wake, wired via SetWakeEnqueuer) enqueues a wake message
// instead of re-invoking the function inline, and WakeService.Run leases,
// dispatches, and settles those messages against the same DispatchTurn path
// an inbound HTTP turn uses — so a continuation and a caller-initiated turn
// chain identically (meters, yield handling, and further continue-enqueues
// all happen inside DispatchTurn itself).
//
// Delivery is at-least-once, mirroring the RFC-0024 asyncinvoke dispatcher
// (pkg/router/asyncinvoke/dispatcher.go) this package's Run/process/classify
// shape is deliberately parallel to. Two consequences a wake consumer must
// keep in mind:
//
//   - The envelope's Turns field is a DEDUP basis only, never a precondition.
//     DispatchTurn reads the persisted Stats.Turns inside a CAS
//     mutate closure that can retry after a version conflict; the count it
//     hands EnqueueWake can therefore be stale relative to what actually
//     lands in the store. A wake consumer must not assume Turns matches the
//     session record at delivery time — it exists purely to collapse a
//     double-enqueue of the same logical continuation while it is
//     unsettled, per statestore.EnqueueOptions.DedupKey.
//   - A wake can be (and, under retry/redrive, sometimes will be) delivered
//     more than once for the same continuation. DispatchTurn re-invokes the
//     function each time; functions that yield "continue" own their own
//     idempotency, the same way any at-least-once consumer must.
//   - process's post-Ack step ALSO enqueues a revival wake, independent of
//     DispatchTurn's own shared-path enqueue (see process's doc comment for
//     the self-dedup chain-death scenario this closes) — but ONLY when
//     TurnResult.ShouldReviveChain is true, i.e. the shared path's OWN
//     maxContinuations cap check decided this turn's continuation was
//     allowed. Yield alone is never enough to gate this: a function past the
//     cap keeps yielding "continue" forever (only the shared path's enqueue
//     is suppressed past it), so gating on Yield would have revived a
//     capped-out chain indefinitely, bypassing maxContinuations entirely.
//     The revival keys on the session's PERSISTED Stats.Turns (read via a
//     fresh Get in reviveChain), the SAME value the shared path enqueued
//     under — so the two enqueues always agree on the key and dedup onto the
//     SAME child message (the revival is then a no-op). Keying the revival on
//     the envelope's own carried count instead would fork on any drift
//     between that count and the persisted one; keying both on the persisted
//     count is what makes the revival at most a harmless duplicate of the
//     shared path's child, never a second divergent chain.
//
// A wake arriving after its session has archived or expired finds no live
// record: DispatchTurn's normal ErrNotFound branch creates a fresh one. That
// is accepted behavior, not a bug — keyed state (RFC-0023) has its own TTLs,
// and a wake is not a promise that the session it targets still exists.
package agentruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils/backoff"
)

const (
	// wakeQueue is the single global agent-wake queue. Like asyncinvoke's
	// DefaultQueue, the namespace/agent/session travel inside the envelope,
	// so one queue serves every agent and every replica leases it via the
	// statestore Queue's SKIP LOCKED semantics.
	wakeQueue        = "agent-wake"
	wakeBatchSize    = 10
	wakeLease        = 5 * time.Minute
	wakePollInterval = 1 * time.Second
	wakeBackoffBase  = 1 * time.Second
	wakeBackoffCap   = 5 * time.Minute
	wakeMaxAge       = 6 * time.Hour

	// wakeBodyJSON is every wake turn's request body: continuations carry no
	// payload — durable context lives in the session's keyed state
	// (RFC-0023). DispatchTurn forwards it verbatim to the function, which
	// reads its own state rather than the turn body.
	wakeBodyJSON = "{}"

	// wakeEnvelopeVersion is the wire-format version of wakeEnvelope.
	wakeEnvelopeVersion = 1

	// wakeSettleTimeout bounds one terminal settle (Ack/Nack/Kill), refreshed
	// AFTER DispatchTurn returns (see process's doc comment) so it never has
	// to cover a turn's own runtime — only the settle call itself.
	wakeSettleTimeout = 15 * time.Second

	// wakeDeliveryTimeout bounds one DispatchTurn call, strictly below
	// wakeLease (mirroring asyncinvoke's invariant A7: a delivery's context
	// always expires before its lease). Without this bound a turn slower
	// than wakeLease would still be running when the lease expires and the
	// message becomes re-leasable, so a second replica (or this same loop's
	// next poll) could dispatch it again while the first delivery is still
	// in flight — safe (the eventual stale settle hits the receipt epoch
	// guard, and functions own their own idempotency per this file's
	// at-least-once doc comment) but needlessly wasteful. Bounding delivery
	// below the lease means a hung turn is cancelled and retried on ITS OWN
	// terms (Nack-with-backoff) well before the lease would force the same
	// outcome via a second, concurrent delivery.
	wakeDeliveryTimeout = wakeLease - 30*time.Second
)

// Dead-letter reasons WakeService assigns. statestore.ReasonRetriesExhausted
// (space-separated) is the store's OWN reason for a Nack that silently spends
// the attempt budget internally; wakeReasonRetriesExhausted is the distinct
// reason this package stamps when IT decides the budget is spent and Kills
// explicitly instead (see retry's doc comment) — kept as its own string so a
// dashboard can tell "the wake dispatcher gave up" from "some other Queue
// caller's Nack exhausted a message".
const (
	wakeReasonUndecodable      = "undecodable_envelope"
	wakeReasonExpired          = "expired"
	wakeReasonNotAgent         = "not_agent"
	wakeReasonSessionQuota     = "session_quota"
	wakeReasonHTTP4xx          = "http_4xx"
	wakeReasonRetriesExhausted = "retries_exhausted"
)

// wakeEnvelope is the durable record of one pending continuation, JSON-encoded
// into a statestore Queue message body.
type wakeEnvelope struct {
	Version     int       `json:"v"`
	Namespace   string    `json:"namespace"`
	Agent       string    `json:"agent"`
	Session     string    `json:"session"`
	Turns       int64     `json:"turns"` // dedup basis: turn count at enqueue
	EnqueueTime time.Time `json:"enqueueTime"`
}

// decodeWakeEnvelope parses a leased message body. Deliberately lenient about
// the message's age or origin — a leased message can be minutes to hours old
// — but NOT about shape: a wire-incompatible or corrupt body is
// wakeReasonUndecodable, not a retry (no future delivery attempt can fix it).
// An unrecognized envelope Version is treated as undecodable too: a version
// this consumer does not understand cannot be safely processed as v1, and no
// future delivery attempt will change the stored bytes — so it dead-letters
// rather than being silently mis-processed.
func decodeWakeEnvelope(data []byte) (wakeEnvelope, error) {
	var env wakeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return env, err
	}
	if env.Version != wakeEnvelopeVersion {
		return env, fmt.Errorf("agentruntime: unrecognized wake envelope version %d (want %d)", env.Version, wakeEnvelopeVersion)
	}
	return env, nil
}

// wakeSink is the TurnSink DispatchTurn writes a wake-delivered turn's
// upstream response into. DispatchTurn's shared path requires a sink for
// every turn kind, but a wake turn's response has no destination — the
// caller that would read it is the continuation chain itself, and its actual
// output (if any) belongs in keyed state, not an HTTP response nobody is
// waiting on. Begin is a no-op; Write counts up to wakeDiscardCap bytes (a
// cheap diagnostic bound, not a buffer — nothing is retained) and drains
// everything beyond it uncounted, always reporting success so
// streamToSink's read loop runs to completion (a Write error would instead
// stop the copy early and leave the upstream body undrained).
type wakeSink struct {
	counted int64
}

// wakeDiscardCap bounds wakeSink's byte counter.
const wakeDiscardCap = 1 << 20

func (s *wakeSink) Begin(int, string, string) {}

func (s *wakeSink) Write(p []byte) (int, error) {
	if remaining := wakeDiscardCap - s.counted; remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		s.counted += n
	}
	return len(p), nil
}

// WakeService is the agent-wake queue's enqueuer and consumer. It implements
// the Dispatcher's enqueuer seam (EnqueueWake) and runs the lease/dispatch/
// settle loop (Run) that delivers what it enqueues.
type WakeService struct {
	logger logr.Logger
	q      statestore.Queue
	d      *Dispatcher
	now    func() time.Time
	// rand is backoff.ExpFullJitter's jitter source: nil disables jitter
	// (deterministic tests); production wiring sets a real source.
	rand func() float64
}

// NewWakeService returns a WakeService that enqueues into and consumes from
// q, dispatching turns via d. now is the service's injected clock (envelope
// timestamps and MaxAge comparisons). rand starts nil (no jitter — see the
// rand field's doc comment); production callers must wire a real source via
// SetRand before Run starts.
func NewWakeService(logger logr.Logger, q statestore.Queue, d *Dispatcher, now func() time.Time) *WakeService {
	return &WakeService{logger: logger, q: q, d: d, now: now}
}

// SetRand wires retry's jitter source post-construction, mirroring
// Dispatcher.SetWakeEnqueuer's post-construction wiring shape: production
// callers pass a real source (e.g. math/rand/v2's Float64) before Run
// starts; tests leave it nil for deterministic (unjittered) backoff. It is a
// plain field, not synchronized — call it before Run, never concurrently
// with it.
func (w *WakeService) SetRand(rand func() float64) {
	w.rand = rand
}

// wakeDedupKey is the DedupKey EnqueueWake enqueues under: "<ns>/<agent>/
// <session>/<turns>" collapses a double-enqueue of the same logical
// continuation while it is unsettled (statestore.EnqueueOptions.DedupKey —
// an existing not-yet-settled message with the same key short-circuits the
// enqueue). It does NOT dedup across settled deliveries: once a message
// Acks, Kills, or dead-letters, a later EnqueueWake with the identical key
// enqueues a fresh message — the overall guarantee is at-least-once, not
// exactly-once.
func wakeDedupKey(ns, agent, session string, turns int64) string {
	return fmt.Sprintf("%s/%s/%s/%d", ns, agent, session, turns)
}

// EnqueueWake implements the dispatcher's enqueuer seam (Dispatcher.wake, set
// via SetWakeEnqueuer). turns is stamped into the envelope as the dedup
// basis ONLY — see this file's package doc comment for why it must never be
// treated as a precondition on delivery.
func (w *WakeService) EnqueueWake(ctx context.Context, ns, agent, session string, turns int64) error {
	env := wakeEnvelope{
		Version:     wakeEnvelopeVersion,
		Namespace:   ns,
		Agent:       agent,
		Session:     session,
		Turns:       turns,
		EnqueueTime: w.now(),
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("agentruntime: encoding wake envelope: %w", err)
	}
	_, err = w.q.Enqueue(ctx, wakeQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{
		DedupKey: wakeDedupKey(ns, agent, session, turns),
	})
	if err != nil {
		return fmt.Errorf("agentruntime: enqueueing wake: %w", err)
	}
	return nil
}

// Run leases and settles until ctx is cancelled: it leases a batch,
// dispatches the batch concurrently, waits, and leases again; an empty
// lease sleeps wakePollInterval (interruptibly). Multiple replicas call Run
// against the same queue safely — statestore leases are SKIP LOCKED.
// Mirrors asyncinvoke's Dispatcher.Run/pollOnce.
func (w *WakeService) Run(ctx context.Context) {
	w.logger.Info("wake service started", "queue", wakeQueue)
	for {
		if ctx.Err() != nil {
			return
		}
		if n := w.pollOnce(ctx); n == 0 {
			if !wakeSleepCtx(ctx, wakePollInterval) {
				return
			}
		}
	}
}

// pollOnce leases one batch and processes it concurrently, returning the
// count leased.
func (w *WakeService) pollOnce(ctx context.Context) int {
	msgs, err := w.q.Lease(ctx, wakeQueue, wakeBatchSize, wakeLease)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error(err, "wake lease failed", "queue", wakeQueue)
		}
		return 0
	}
	var wg sync.WaitGroup
	for _, msg := range msgs {
		wg.Go(func() { w.process(ctx, msg) })
	}
	wg.Wait()
	return len(msgs)
}

// process dispatches one leased wake message and settles it per
// classifyWakeResult. DispatchTurn runs under wakeDeliveryTimeout (bounded
// below wakeLease — see that constant's doc comment). The terminal settle
// runs on a FRESH context — built AFTER DispatchTurn returns, never the
// (possibly now-expired) delivery context (mirrors asyncinvoke's process,
// dispatcher.go :246-305, :281-283): a turn can run for as long as
// wakeDeliveryTimeout allows, which can exceed wakeSettleTimeout many times
// over, so settling against a pre-dispatch timeout context would find it
// already expired and every settle call would silently fail, stranding the
// message leased until wakeLease expires.
//
// Self-dedup chain-death window: DispatchTurn's shared post-response path
// (dispatcher.go) enqueues its OWN continuation wake using the CAS-persisted
// Stats.Turns count — but that count can be stale. If an earlier HTTP turn's
// bookkeeping CAS lost a version-conflict race (the record stayed at
// Turns=N-1) while its wake enqueue under dedup key ".../N" still landed,
// then THIS delivery (which re-runs DispatchTurn and re-increments the
// now-N-1 record to N) has the shared path enqueue under that SAME key
// ".../N" again — which collapses against the message THIS call is
// currently processing (still leased, hence unsettled per
// statestore/types.go:115) instead of creating a fresh child. ack() below
// then settles this message with no live successor ever created: the chain
// dies silently even though the function said continue. reviveChain is the
// fix — a second, consumer-side enqueue keyed on the session's PERSISTED
// Stats.Turns (re-Get inside reviveChain, the SAME key the shared path used),
// issued only once this message has actually Acked. In the ordinary
// (non-racing) case it dedups onto whatever the shared path already
// (correctly) enqueued, a no-op; in the chain-death case above the shared
// path's enqueue collapsed onto this now-Acked parent, so the revival's
// post-settle enqueue is the only live one and the chain survives. Keying on
// the persisted count (rather than the message's own carried count) is what
// keeps revive and shared path in lockstep — the two never emit two
// divergent successors, so the cost is at most one bounded duplicate per hop,
// never a fork.
//
// This must fire ONLY when the shared path's OWN maxContinuations cap check
// decided the continuation was allowed (TurnResult.ShouldReviveChain),
// never merely because Yield=="continue": past the cap, DispatchTurn keeps
// returning Yield=="continue" forever (it is the function's own response,
// unaffected by the cap — only the shared path's enqueue is suppressed), so
// gating on Yield alone would revive an already-capped chain indefinitely,
// silently bypassing maxContinuations. WakeEnqueueAttempted (which
// ShouldReviveChain checks) is exactly the shared path's cap decision,
// independent of whether an enqueuer was wired or its own enqueue call
// errored or deduped — which is what lets this gate still revive the
// chain-death race above (the cap DID allow it there; the shared path's
// enqueue just collided with its own parent) while correctly refusing to
// revive a chain that is past its cap.
func (w *WakeService) process(ctx context.Context, msg statestore.LeasedMessage) {
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), wakeSettleTimeout)
	defer scancel()

	env, err := decodeWakeEnvelope(msg.Body)
	if err != nil {
		w.logger.Error(err, "wake envelope will not decode; dead-lettering", "id", msg.ID)
		w.kill(sctx, msg, wakeReasonUndecodable)
		return
	}

	if w.now().Sub(env.EnqueueTime) > wakeMaxAge {
		w.logger.V(1).Info("wake dead-lettered: past max age", "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts, "age", w.now().Sub(env.EnqueueTime).String())
		w.kill(sctx, msg, wakeReasonExpired)
		return
	}

	tr := TurnRequest{
		Namespace:   env.Namespace,
		Agent:       env.Agent,
		SessionID:   env.Session,
		Body:        []byte(wakeBodyJSON),
		ContentType: "application/json",
		WakeID:      msg.ID,
		WakeAttempt: msg.Attempts,
	}
	sink := &wakeSink{}
	dctx, dcancel := context.WithTimeout(ctx, wakeDeliveryTimeout)
	res, derr := w.d.DispatchTurn(dctx, tr, sink)
	dcancel()
	w.logger.V(1).Info("wake turn dispatched", "id", msg.ID, "attempt", msg.Attempts, "bytesDrained", sink.counted)

	// Refresh the settle budget now that DispatchTurn has returned — see this
	// method's doc comment.
	scancel()
	sctx, scancel = context.WithTimeout(context.WithoutCancel(ctx), wakeSettleTimeout)
	defer scancel()

	switch classifyWakeResult(res, derr) {
	case wakeActionAck:
		if w.ack(sctx, msg) && res.ShouldReviveChain() {
			w.reviveChain(sctx, env)
		}
	case wakeActionKillNotAgent:
		w.logger.V(1).Info("wake dead-lettered: not an agent function", "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		w.kill(sctx, msg, wakeReasonNotAgent)
	case wakeActionKillSessionQuota:
		w.logger.V(1).Info("wake dead-lettered: session quota exceeded on re-create", "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		w.kill(sctx, msg, wakeReasonSessionQuota)
	case wakeActionKillHTTP4xx:
		// A 401/403 from the router internal listener is not a function-level
		// client error — it signals a runtime-side auth misconfiguration (a
		// missing/incorrect internal HMAC secret, or a rejected JWT). Surface
		// it loudly (an otherwise-silent HMAC misconfig would DLQ every chain),
		// but still dead-letter: retrying an auth failure will not fix it.
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			w.logger.Error(nil, "wake turn rejected by router auth; check the internal HMAC secret / JWT signing key — dead-lettering", "status", res.StatusCode, "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		} else {
			w.logger.V(1).Info("wake dead-lettered: http 4xx", "status", res.StatusCode, "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		}
		w.kill(sctx, msg, wakeReasonHTTP4xx)
	case wakeActionRetry:
		w.retry(sctx, msg, env)
	}
}

// wakeAction is the settle decision for one wake delivery attempt.
type wakeAction int

const (
	wakeActionAck wakeAction = iota
	wakeActionKillNotAgent
	wakeActionKillSessionQuota
	wakeActionKillHTTP4xx
	wakeActionRetry
)

// classifyWakeResult maps a DispatchTurn outcome to a settle action — the
// classify matrix from the task brief, kept pure (like asyncinvoke's
// classify) so it is exhaustively unit-tested independent of any queue or
// HTTP plumbing.
//
//   - ErrNotAgent (function deleted or Agent block removed): kill, not_agent.
//   - ErrSessionQuota: kill, session_quota. Rationale: quota on a WAKE can
//     only mean the session record expired mid-chain and the fresh Create
//     hit a full budget — a rare state seconds-later retries rarely help,
//     and the driver's Nack would DLQ in 3 attempts under a generic reason
//     anyway (statestore.DefaultMaxAttempts). A Kill with a precise reason
//     is redrivable via Queue.Redrive once the underlying quota clears.
//   - any other error (a transport failure, or DispatchTurn failing to even
//     build the upstream request): retry, same bucket as a 5xx.
//   - 2xx: ack (continue-chaining, if any, already happened inside
//     DispatchTurn's shared post-response bookkeeping).
//   - 408/429: retry (transient).
//   - other 4xx: kill, http_4xx (permanent client error).
//   - 5xx (or any other unexpected status): retry.
func classifyWakeResult(res TurnResult, err error) wakeAction {
	switch {
	case errors.Is(err, ErrNotAgent):
		return wakeActionKillNotAgent
	case errors.Is(err, ErrSessionQuota):
		return wakeActionKillSessionQuota
	case err != nil:
		return wakeActionRetry
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return wakeActionAck
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusTooManyRequests:
		return wakeActionRetry
	case res.StatusCode >= 400 && res.StatusCode < 500:
		return wakeActionKillHTTP4xx
	default:
		return wakeActionRetry
	}
}

// ack settles a delivery as succeeded, reporting whether the settle actually
// landed — a stale receipt (a newer lease already superseded this one)
// returns false, which process uses to skip reviveChain: whichever delivery
// actually won the settle is the one responsible for reviving the chain.
func (w *WakeService) ack(ctx context.Context, msg statestore.LeasedMessage) bool {
	if err := w.q.Ack(ctx, msg.Receipt); err != nil {
		w.logSettle("ack", msg.ID, err)
		return false
	}
	return true
}

// reviveChain closes the self-dedup chain-death window documented on
// process: a best-effort enqueue issued only after the Ack has actually
// landed AND only when the caller (process) has confirmed
// TurnResult.ShouldReviveChain — i.e. this session's maxContinuations cap
// allowed the continuation.
//
// It keys the revival on the session's PERSISTED Stats.Turns, read via a
// fresh Get here (NOT env.Turns+1). That is the same value DispatchTurn's
// shared post-response path enqueued under, so revive and shared path always
// agree on the key: in the ordinary case the revival dedups onto the exact
// child the shared path already created (a harmless no-op), and it never
// forks. Keying on env.Turns+1 (the message's own carried count + 1) instead
// would DIVERGE from the persisted count whenever the two drift — a
// concurrent HTTP turn, an archived-and-recreated session, or a lost CAS —
// and each hop would then emit TWO live successors under two different keys
// that never re-converge, compounding without bound when maxContinuations=0.
//
// Post-Ack this message is settled, so a fresh enqueue under the persisted
// key is guaranteed to create a live successor rather than collapse onto this
// parent — which is exactly what carries the chain forward in the chain-death
// case (where the shared path's own enqueue collided with, and collapsed
// onto, this still-leased parent before the Ack). reviveChain does not
// re-check the cap — it trusts the caller's gate — so it must never be called
// except behind that check.
//
// If the fresh Get fails, the revival is skipped (logged at Info): the chain
// is carried from the persisted state, so without a readable record there is
// nothing to safely key on — failing closed on the fork is consistent with
// the "bookkeeping never gates a turn" rule (the turn itself already
// succeeded and Acked). A failure here never propagates: this runs after the
// message has already settled, so there is no settle left to fail or retry.
func (w *WakeService) reviveChain(ctx context.Context, env wakeEnvelope) {
	rec, _, err := w.d.store.Get(ctx, env.Namespace, env.Agent, env.Session)
	if err != nil {
		w.logger.Info("wake chain revival skipped: session read failed", "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "err", err.Error())
		return
	}
	if err := w.EnqueueWake(ctx, env.Namespace, env.Agent, env.Session, rec.Stats.Turns); err != nil {
		w.logger.Info("wake chain revival enqueue failed", "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "err", err.Error())
	}
}

// kill dead-letters a delivery immediately with reason.
func (w *WakeService) kill(ctx context.Context, msg statestore.LeasedMessage, reason string) {
	if err := w.q.Kill(ctx, msg.Receipt, reason); err != nil {
		w.logSettle("kill", msg.ID, err)
	}
}

// retry either requeues with backoff or dead-letters, checked in the same
// order asyncinvoke's retry() uses (dispatcher.go :321-338):
//
//  1. attempt budget spent (msg.Attempts >= statestore.DefaultMaxAttempts):
//     Kill with the precise wakeReasonRetriesExhausted reason BEFORE calling
//     Nack, rather than letting the store's own internal Nack-exhaustion
//     path dead-letter it under the store's generic ReasonRetriesExhausted.
//  2. the NEXT retry would land past wakeMaxAge (now+backoff exceeds the
//     envelope's age budget): Kill with wakeReasonExpired now rather than
//     requeue work that can only expire once the backoff elapses — the true
//     reason, not a manufactured extra retry cycle.
//  3. otherwise, Nack with the computed backoff.
func (w *WakeService) retry(ctx context.Context, msg statestore.LeasedMessage, env wakeEnvelope) {
	if msg.Attempts >= statestore.DefaultMaxAttempts {
		w.logger.V(1).Info("wake dead-lettered: retries exhausted", "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		w.kill(ctx, msg, wakeReasonRetriesExhausted)
		return
	}
	delay := backoff.ExpFullJitter(wakeBackoffBase, wakeBackoffCap, msg.Attempts, w.rand)
	if w.now().Add(delay).Sub(env.EnqueueTime) > wakeMaxAge {
		w.logger.V(1).Info("wake dead-lettered: next retry would exceed max age", "id", msg.ID, "namespace", env.Namespace, "agent", env.Agent, "session", env.Session, "attempts", msg.Attempts)
		w.kill(ctx, msg, wakeReasonExpired)
		return
	}
	if err := w.q.Nack(ctx, msg.Receipt, delay); err != nil {
		w.logSettle("nack", msg.ID, err)
	}
}

// logSettle logs a settle error, quieting the expected stale-receipt case
// (a delivery whose lease a newer lease already superseded) to V(1) so it
// does not drown a genuine store failure logged at Error — mirrors
// asyncinvoke's logSettle.
func (w *WakeService) logSettle(op, id string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, statestore.ErrInvalidReceipt) {
		w.logger.V(1).Info("wake settle raced a newer lease (expected)", "op", op, "id", id)
		return
	}
	w.logger.Error(err, "wake "+op+" failed", "id", id)
}

// wakeSleepCtx sleeps for d or until ctx is cancelled; it returns false if
// cancelled. Mirrors asyncinvoke's sleepCtx (package-private there too, so
// duplicated rather than exported cross-package for a five-line helper).
func wakeSleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
