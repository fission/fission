// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// newTestWakeService wires a WakeService, its Dispatcher, and the AgentView
// against one shared in-memory statestore instance (KV for sessions, Queue
// for the wake queue), forwarding turns to upstreamURL via http.DefaultClient.
func newTestWakeService(t *testing.T, upstreamURL string, now func() time.Time) (*WakeService, *AgentView, *SessionStore, statestore.Queue) {
	t.Helper()
	return newTestWakeServiceWithClient(t, http.DefaultClient, upstreamURL, now)
}

// newTestWakeServiceWithClient is newTestWakeService with an injectable
// *http.Client, so a test can point the Dispatcher at a fake in-process
// http.RoundTripper instead of a real listener — required inside a
// testing/synctest bubble, where a real network dial's completion is not
// governed by the bubble's fake clock. maxContinuations is 0 (unlimited),
// and the Dispatcher's own enqueuer seam is deliberately left unwired
// (Dispatcher.wake stays nil) — see newTestWakeServiceFull if a test needs
// either dialed in.
func newTestWakeServiceWithClient(t *testing.T, client *http.Client, upstreamURL string, now func() time.Time) (*WakeService, *AgentView, *SessionStore, statestore.Queue) {
	t.Helper()
	return newTestWakeServiceFull(t, client, upstreamURL, now, 0, false)
}

// newTestWakeServiceFull is newTestWakeServiceWithClient with the two knobs
// that vary per test: maxContinuations (Dispatcher's continuation cap, 0 =
// unlimited) and wireEnqueuer (whether Dispatcher.SetWakeEnqueuer(w) is
// called — i.e. whether DispatchTurn's OWN shared-path continue-chain
// enqueue is live, the way production wiring does it, as opposed to relying
// solely on WakeService.process's consumer-side revival).
func newTestWakeServiceFull(t *testing.T, client *http.Client, upstreamURL string, now func() time.Time, maxContinuations int64, wireEnqueuer bool) (*WakeService, *AgentView, *SessionStore, statestore.Queue) {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	kv, err := caps.KV()
	require.NoError(t, err)
	q, err := caps.Queue()
	require.NoError(t, err)

	store := NewSessionStore(kv, now)
	view := NewAgentView()
	d := NewDispatcher(logr.Discard(), view, store, client, upstreamURL, time.Hour, now, maxContinuations, defaultMaxSpawnDepth)
	w := NewWakeService(logr.Discard(), q, d, now)
	if wireEnqueuer {
		d.SetWakeEnqueuer(w)
	}
	return w, view, store, q
}

// roundTripFunc adapts a function to http.RoundTripper, entirely in-process
// (no real dial, no real syscall) — safe to exercise inside a synctest
// bubble, unlike an httptest.Server's real loopback listener.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// statusClient returns an *http.Client whose RoundTripper always answers
// status, counting each call in attempts (nil attempts is fine — the caller
// just doesn't care).
func statusClient(status int, attempts *atomic.Int64) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if attempts != nil {
			attempts.Add(1)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}
}

// enqueueWakeEnvelope enqueues env directly (bypassing EnqueueWake's dedup
// key and now()-stamped EnqueueTime), for tests that need to control the
// envelope's fields precisely (e.g. an already-expired EnqueueTime).
func enqueueWakeEnvelope(t *testing.T, q statestore.Queue, env wakeEnvelope) string {
	t.Helper()
	body, err := json.Marshal(env)
	require.NoError(t, err)
	id, err := q.Enqueue(t.Context(), wakeQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	return id
}

// drainedWake reports whether wakeQueue has settled everything it was given
// (nothing visible, leased, or dead).
func drainedWake(t *testing.T, q statestore.Queue) bool {
	t.Helper()
	st, err := q.Stats(t.Context(), wakeQueue)
	require.NoError(t, err)
	return st.Visible == 0 && st.Leased == 0 && st.Dead == 0
}

// TestWakeService_EnqueueDedup pins EnqueueWake's DedupKey behavior: an
// unsettled message with the same (namespace, agent, session, turns) key
// collapses the enqueue, but once that message settles (Ack), a later
// EnqueueWake with the identical key enqueues a fresh message — dedup only
// collapses while unsettled, the overall guarantee stays at-least-once.
func TestWakeService_EnqueueDedup(t *testing.T) {
	t.Parallel()
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)
	ctx := t.Context()

	require.NoError(t, w.EnqueueWake(ctx, "ns", "fn", "sess", 3))
	require.NoError(t, w.EnqueueWake(ctx, "ns", "fn", "sess", 3))

	st, err := q.Stats(ctx, wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Visible, "same turns collapses to one unsettled message")

	msgs, err := q.Lease(ctx, wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.NoError(t, q.Ack(ctx, msgs[0].Receipt))

	require.NoError(t, w.EnqueueWake(ctx, "ns", "fn", "sess", 3))
	st, err = q.Stats(ctx, wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Visible, "enqueue after the prior message settled is a fresh message")
}

// TestWakeService_RunHappyPath: a 200 upstream response acks the message and
// stamps the wake replay headers (HeaderWakeID/HeaderWakeAttempt) onto the
// forwarded turn.
func TestWakeService_RunHappyPath(t *testing.T) {
	t.Parallel()
	var gotWakeID, gotAttempt string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWakeID = r.Header.Get(HeaderWakeID)
		gotAttempt = r.Header.Get(HeaderWakeAttempt)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	w, view, _, q := newTestWakeService(t, upstream.URL, time.Now)
	view.Upsert(headerEntry("ns", "fn", 0))

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool { return drainedWake(t, q) }, 2*time.Second, 5*time.Millisecond)
	assert.NotEmpty(t, gotWakeID, "the leased message id must be replayed as HeaderWakeID")
	assert.Equal(t, "1", gotAttempt)
}

// TestWakeService_AckedContinueRevivesChainWithoutSharedPathEnqueuer pins
// the process-level chain-death fix: SetWakeEnqueuer is deliberately never
// called on the Dispatcher here, so Dispatcher.wake stays nil and
// DispatchTurn's OWN shared-path continue-chain enqueue is entirely
// disabled (see DispatchTurn's "if doEnqueue && d.wake != nil" guard,
// dispatcher.go). If reviveChain did not exist, a yield=continue wake
// turn processed under this Dispatcher would Ack with NO successor ever
// enqueued -- the chain would die on the very first delivery. Driving
// process() directly (the same call Run's loop makes per leased message)
// keeps the assertion deterministic: after the Ack, the queue must hold a
// live next message, proving the revival is a consumer-side mechanism
// independent of the shared path.
//
// newTestWakeService leaves maxContinuations at 0 (unlimited), so the
// shared path's OWN cap decision (TurnResult.WakeEnqueueAttempted, which
// process now gates revival on) is trivially true here regardless of
// d.wake being nil -- this test is about the shared path's ENQUEUE being
// disabled, not its cap decision. See
// TestWakeService_RevivalRespectsMaxContinuationsCap for the cap gate
// itself.
func TestWakeService_AckedContinueRevivesChainWithoutSharedPathEnqueuer(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	w, view, _, q := newTestWakeService(t, upstream.URL, time.Now)
	view.Upsert(headerEntry("ns", "fn", 0))
	// Note: d.SetWakeEnqueuer is never called -- the shared path cannot
	// enqueue anything, so any successor observed below must have come from
	// reviveChain.

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	w.process(t.Context(), msgs[0])

	st, err := q.Stats(t.Context(), wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 0, st.Dead, "the parent settled cleanly (Acked), not dead-lettered")
	assert.EqualValues(t, 1, st.Visible, "reviveChain enqueued a live successor even with the shared path disabled")
}

// TestWakeService_RevivalRespectsMaxContinuationsCap pins the regression fix
// from the Part C re-review: reviveChain must gate on
// TurnResult.WakeEnqueueAttempted (the shared path's OWN maxContinuations
// cap decision), not on Yield alone. An always-continue upstream keeps
// returning Yield="continue" forever regardless of the cap -- gating on
// Yield alone (the pre-fix code) revives the chain indefinitely, bypassing
// maxContinuations entirely (the reviewer drove 6+ hops with cap=1 before
// this fix). Dispatcher.SetWakeEnqueuer IS wired here (production shape),
// so the shared path's own enqueue is live too: this proves the cap holds
// end-to-end, not just that the consumer-side revival happens to be inert.
//
// With maxContinuations=1: hop 1 is within the cap (Continuations 0->1),
// so the shared path enqueues AND the revival's enqueue -- keyed on the same
// persisted Stats.Turns the shared path used (reviveChain re-Gets it) -- dedups
// onto it, one live child, as normal. Hop 2 crosses the cap (Continuations
// 1->2 == maxContinuations+1): the shared path's own doEnqueue is false, so
// WakeEnqueueAttempted is false, and process must NOT revive -- the chain stops
// with no live successor.
func TestWakeService_RevivalRespectsMaxContinuationsCap(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	const maxContinuations = 1
	w, view, store, q := newTestWakeServiceFull(t, http.DefaultClient, upstream.URL, time.Now, maxContinuations, true)
	view.Upsert(headerEntry("ns", "fn", 0))

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

	// Drive hops through process() (the same per-message unit Run's loop
	// calls) until the queue runs dry. A bound well past what the cap
	// should allow: against the pre-fix code this loop exhausts its budget
	// while messages are STILL arriving, and the assertions below fail.
	const hopBudget = 5
	hops := 0
	for ; hops < hopBudget; hops++ {
		msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
		require.NoError(t, err)
		if len(msgs) == 0 {
			break
		}
		require.Len(t, msgs, 1, "hop %d: exactly one live message at a time -- no duplicate storm", hops)
		w.process(t.Context(), msgs[0])
	}
	require.Less(t, hops, hopBudget, "the chain must stop within the hop budget -- it did not stop at all")

	st, err := q.Stats(t.Context(), wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 0, st.Visible, "chain must stop once the cap is reached -- no live successor left")
	assert.EqualValues(t, 0, st.Leased)
	assert.EqualValues(t, 0, st.Dead, "stopped by the cap, not a failure -- no dead letters")

	rec, _, err := store.Get(t.Context(), "ns", "fn", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, maxContinuations+1, rec.Stats.Continuations,
		"Continuations must stop advancing exactly at the cap crossing -- no further turns were dispatched")

	// One more Lease finds nothing: durably stopped, not just transiently
	// empty mid-hop.
	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

// TestWakeService_ContinueChainDoesNotForkOnDivergence is the regression pin
// for the chain-fork/amplification bug: reviveChain must key its enqueue on
// the session's PERSISTED Stats.Turns (a fresh Get), NOT on the envelope's
// own carried count + 1. When the two diverge — here forced by seeding the
// record at Turns=5 while delivering an envelope carrying Turns=0 — the
// buggy env.Turns+1 keying emits a SECOND live successor under a different
// dedup key (".../1") than the shared path's own enqueue (".../6"), and the
// two never re-converge, so every hop doubles. The fix keys both on the
// persisted count, so the revival dedups onto the shared path's single child.
//
// The shared-path enqueuer IS wired (production shape), so DispatchTurn's own
// continuation enqueue is live; the only successor difference between buggy
// and fixed code is whether reviveChain forks a second one.
func TestWakeService_ContinueChainDoesNotForkOnDivergence(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	// wireEnqueuer=true, maxContinuations=0 (unlimited): the shared path
	// enqueues under the post-increment persisted turns key.
	w, view, store, q := newTestWakeServiceFull(t, http.DefaultClient, upstream.URL, time.Now, 0, true)
	view.Upsert(headerEntry("ns", "fn", 0))

	// Force divergence: the live record is already at Turns=5, but the wake
	// envelope we deliver carries Turns=0. This delivery re-increments the
	// record to 6 (the shared path enqueues ".../6"); the buggy revival would
	// enqueue ".../1" (env.Turns+1), forking the chain.
	require.NoError(t, store.Create(t.Context(), SessionRecord{
		ID: "sess-1", Agent: "fn", Namespace: "ns", Status: StatusActive,
		CreatedAt: time.Now(), LastActiveAt: time.Now(),
		Stats: SessionStats{Turns: 5},
	}, 0, time.Hour))

	enqueueWakeEnvelope(t, q, wakeEnvelope{
		Version: wakeEnvelopeVersion, Namespace: "ns", Agent: "fn", Session: "sess-1",
		Turns: 0, EnqueueTime: time.Now(),
	})

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	w.process(t.Context(), msgs[0])

	rec, _, err := store.Get(t.Context(), "ns", "fn", "sess-1")
	require.NoError(t, err)
	require.EqualValues(t, 6, rec.Stats.Turns, "the delivery must have incremented the record to 6")

	st, err := q.Stats(t.Context(), wakeQueue)
	require.NoError(t, err)
	assert.EqualValues(t, 0, st.Dead, "the parent Acked cleanly")
	assert.EqualValues(t, 1, st.Visible,
		"exactly ONE live successor per hop — the revival must dedup onto the shared path's child, not fork a second chain")

	// The single survivor must be keyed on the persisted count (6), proving
	// the revival re-read the record rather than reusing env.Turns+1 (=1).
	survivors, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, survivors, 1)
	var succ wakeEnvelope
	require.NoError(t, json.Unmarshal(survivors[0].Body, &succ))
	assert.EqualValues(t, 6, succ.Turns, "the successor must carry the persisted turn count, not env.Turns+1")
}

// TestDecodeWakeEnvelope_UnknownVersion pins the version guard (#26b): an
// envelope carrying a version this consumer does not recognize is undecodable
// (dead-lettered), never silently processed as v1.
func TestDecodeWakeEnvelope_UnknownVersion(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(wakeEnvelope{Version: wakeEnvelopeVersion + 1, Namespace: "ns", Agent: "fn", Session: "s"})
	require.NoError(t, err)
	_, derr := decodeWakeEnvelope(body)
	assert.Error(t, derr, "an unrecognized envelope version must not decode")

	// A well-formed current-version envelope still decodes.
	good, err := json.Marshal(wakeEnvelope{Version: wakeEnvelopeVersion, Namespace: "ns", Agent: "fn", Session: "s"})
	require.NoError(t, err)
	_, derr = decodeWakeEnvelope(good)
	assert.NoError(t, derr)
}

// TestWakeService_RetryBackoffGrows runs the loop under virtual time (via
// testing/synctest) against an always-500 upstream: it proves a nacked wake
// is not redelivered before its backoff elapses, AND that the backoff grows
// between the first and second retry (ExpFullJitter(wakeBackoffBase,
// wakeBackoffCap, attempt, nil) with attempt = msg.Attempts).
func TestWakeService_RetryBackoffGrows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int64
		client := statusClient(http.StatusInternalServerError, &attempts)
		w, view, _, _ := newTestWakeServiceWithClient(t, client, "http://router.internal", time.Now)
		view.Upsert(headerEntry("ns", "fn", 0))

		require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

		ctx, cancel := context.WithCancel(t.Context())
		go w.Run(ctx)

		// First delivery happens, then the message is nacked with a ~1s
		// (wakeBackoffBase, attempt 1) backoff.
		synctest.Wait()
		require.EqualValues(t, 1, attempts.Load())

		// Still within the first backoff window.
		synctest.Sleep(500 * time.Millisecond)
		require.EqualValues(t, 1, attempts.Load(), "no re-delivery before the first backoff elapses")

		// Past the first (~1s) backoff -> redelivered (attempt 2), nacked
		// again with a LONGER ~2s backoff -- proving the delay grows.
		synctest.Sleep(600 * time.Millisecond)
		synctest.Wait()
		require.EqualValues(t, 2, attempts.Load())

		synctest.Sleep(1500 * time.Millisecond)
		require.EqualValues(t, 2, attempts.Load(), "no third delivery before the grown (~2s) backoff elapses")

		synctest.Sleep(700 * time.Millisecond)
		synctest.Wait()
		require.EqualValues(t, 3, attempts.Load(), "redelivered once the grown backoff elapses")

		cancel()
		synctest.Wait()
	})
}

// TestWakeService_DeliveryTimeoutBoundedBelowLease proves DispatchTurn is
// bounded by wakeDeliveryTimeout, strictly below wakeLease: a RoundTripper
// that never returns on its own is cancelled well before the lease would
// expire, settled with a single Nack, and only redelivered once ITS OWN
// backoff elapses -- never as a second, CONCURRENT delivery racing the
// first because the lease expired out from under a still-hanging call.
// Without wakeDeliveryTimeout, this dispatch would still be in flight when
// wakeLease (5m) elapsed, the message would become re-leasable, and a
// second delivery would start while the first was still running.
func TestWakeService_DeliveryTimeoutBoundedBelowLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int64
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts.Add(1)
			<-r.Context().Done() // hangs until DispatchTurn's per-delivery timeout fires
			return nil, r.Context().Err()
		})}
		w, view, _, _ := newTestWakeServiceWithClient(t, client, "http://router.internal", time.Now)
		view.Upsert(headerEntry("ns", "fn", 0))

		require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

		ctx, cancel := context.WithCancel(t.Context())
		go w.Run(ctx)

		// Still mid-delivery, well short of wakeDeliveryTimeout.
		synctest.Sleep(wakeDeliveryTimeout - 100*time.Millisecond)
		synctest.Wait()
		require.EqualValues(t, 1, attempts.Load(), "still one in-flight delivery, timeout not yet reached")

		// Cross wakeDeliveryTimeout (at the 100ms mark from here): the hung
		// call is cancelled and the message Nacked with a deterministic ~1s
		// (wakeBackoffBase, attempt 1) backoff, landing visibleAt ~1s after
		// the cancellation. This checkpoint (300ms past the crossing) is
		// comfortably inside that ~1s window, so the dispatch count must
		// still read 1 -- no concurrent second delivery.
		synctest.Sleep(400 * time.Millisecond)
		synctest.Wait()
		require.EqualValues(t, 1, attempts.Load(), "cancelled and Nacked by the delivery timeout; backoff not yet elapsed")

		// Past the ~1s backoff (this checkpoint lands ~1.2s after the
		// crossing): a second, SEQUENTIAL delivery begins -- the ordinary
		// retry, not a lease-expiry race (the original lease wouldn't
		// expire for minutes yet).
		synctest.Sleep(900 * time.Millisecond)
		synctest.Wait()
		require.EqualValues(t, 2, attempts.Load(), "exactly one new, sequential delivery once the backoff elapses")

		cancel()
		synctest.Wait()
	})
}

// TestWakeService_RunKillsHTTP4xx: a 404 upstream response through the full
// Run loop dead-letters with reason "http_4xx", never retried.
func TestWakeService_RunKillsHTTP4xx(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	w, view, _, q := newTestWakeService(t, upstream.URL, time.Now)
	view.Upsert(headerEntry("ns", "fn", 0))

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 2*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, wakeReasonHTTP4xx, dead[0].Reason)
	assert.Equal(t, 1, dead[0].Attempts, "killed on the first attempt, never retried")
}

// TestWakeService_RetryDeadLettersWhenNextAttemptWouldExpire pins retry's
// would-expire pre-check (mirroring asyncinvoke's retry() order): when the
// NEXT attempt's backoff would land past wakeMaxAge, retry Kills with
// wakeReasonExpired immediately instead of Nacking work that can only
// expire once the backoff elapses.
func TestWakeService_RetryDeadLettersWhenNextAttemptWouldExpire(t *testing.T) {
	t.Parallel()
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)

	// EnqueueTime is old enough that only 500ms of the wakeMaxAge budget
	// remains -- less than the ~1s (attempt 1) backoff retry is about to
	// compute.
	enqueueTime := time.Now().Add(-wakeMaxAge + 500*time.Millisecond)
	enqueueWakeEnvelope(t, q, wakeEnvelope{
		Version: wakeEnvelopeVersion, Namespace: "ns", Agent: "fn", Session: "sess-1",
		EnqueueTime: enqueueTime,
	})

	msgs, err := q.Lease(t.Context(), wakeQueue, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, 1, msgs[0].Attempts, "first lease, well under the retry-exhaustion budget")

	w.retry(t.Context(), msgs[0], wakeEnvelope{EnqueueTime: enqueueTime})

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, wakeReasonExpired, dead[0].Reason, "the next backoff would land past wakeMaxAge")
}

// TestWakeService_RetriesExhaustedDeadLetters: once msg.Attempts reaches
// statestore.DefaultMaxAttempts, the NEXT 5xx is Killed with
// wakeReasonRetriesExhausted instead of Nacked again (the exhaustion check
// runs BEFORE the Nack call, mirroring asyncinvoke's retry()).
func TestWakeService_RetriesExhaustedDeadLetters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := statusClient(http.StatusInternalServerError, nil)
		w, view, _, q := newTestWakeServiceWithClient(t, client, "http://router.internal", time.Now)
		view.Upsert(headerEntry("ns", "fn", 0))

		require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-1", 0))

		ctx, cancel := context.WithCancel(t.Context())
		go w.Run(ctx)

		// Growing backoff across DefaultMaxAttempts (3) deliveries comfortably
		// finishes within 10 virtual seconds (~1s + ~2s + kill, no further wait).
		synctest.Sleep(10 * time.Second)
		synctest.Wait()

		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		require.Len(t, dead, 1)
		assert.Equal(t, wakeReasonRetriesExhausted, dead[0].Reason)
		assert.GreaterOrEqual(t, dead[0].Attempts, statestore.DefaultMaxAttempts)

		cancel()
		synctest.Wait()
	})
}

// TestWakeService_RunKillsNotAgent: DispatchTurn's ErrNotAgent (the agent
// entry is absent from the view — e.g. the Function was deleted between
// enqueue and delivery) dead-letters immediately with reason "not_agent",
// never retried.
func TestWakeService_RunKillsNotAgent(t *testing.T) {
	t.Parallel()
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)
	// No view.Upsert: (ns, missing-fn) is not a registered agent.

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "missing-fn", "sess-1", 0))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 2*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, wakeReasonNotAgent, dead[0].Reason)
	assert.Equal(t, 1, dead[0].Attempts, "killed on the first attempt, never retried")
}

// TestWakeService_RunKillsSessionQuota: DispatchTurn's ErrSessionQuota (the
// wake's session record is gone — expired or archived — and the fresh
// Create hit the agent's MaxSessions budget) dead-letters with reason
// "session_quota", never retried (the ledgered rationale: seconds-later
// retries rarely help a full budget, and the driver's own Nack-exhaustion
// would DLQ in DefaultMaxAttempts anyway under a generic reason).
func TestWakeService_RunKillsSessionQuota(t *testing.T) {
	t.Parallel()
	w, view, store, q := newTestWakeService(t, "http://unused.invalid", time.Now)
	entry := headerEntry("ns", "fn", 1) // MaxSessions=1
	view.Upsert(entry)

	// Spend the one-session budget with an unrelated live session, so the
	// wake's target session (which does not exist -- simulating an
	// expired/archived record) hits ErrSessionQuota on DispatchTurn's Create.
	require.NoError(t, store.Create(t.Context(), SessionRecord{
		ID: "other-session", Agent: "fn", Namespace: "ns", Status: StatusActive,
	}, entry.MaxSessions, time.Hour))

	require.NoError(t, w.EnqueueWake(t.Context(), "ns", "fn", "sess-expired", 0))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 2*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	assert.Equal(t, wakeReasonSessionQuota, dead[0].Reason)
}

// TestWakeService_MaxAgeExpiryDeadLetters: an envelope enqueued more than
// wakeMaxAge ago is dead-lettered with reason "expired" without ever calling
// DispatchTurn (checked before dispatch, mirroring asyncinvoke's MaxAge
// check).
func TestWakeService_MaxAgeExpiryDeadLetters(t *testing.T) {
	t.Parallel()
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)

	enqueueWakeEnvelope(t, q, wakeEnvelope{
		Version:     wakeEnvelopeVersion,
		Namespace:   "ns",
		Agent:       "fn",
		Session:     "sess-1",
		Turns:       0,
		EnqueueTime: time.Now().Add(-(wakeMaxAge + time.Minute)),
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 2*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	assert.Equal(t, wakeReasonExpired, dead[0].Reason)
}

// TestWakeService_RunUndecodableEnvelopeDeadLetters: a message body that
// will not decode as a wakeEnvelope is killed with "undecodable_envelope"
// rather than retried — no future delivery attempt can fix a corrupt body.
func TestWakeService_RunUndecodableEnvelopeDeadLetters(t *testing.T) {
	t.Parallel()
	w, _, _, q := newTestWakeService(t, "http://unused.invalid", time.Now)

	_, err := q.Enqueue(t.Context(), wakeQueue, statestore.Message{Body: []byte("not json")}, statestore.EnqueueOptions{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go w.Run(ctx)

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 2*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), wakeQueue, statestore.Page{})
	require.NoError(t, err)
	assert.Equal(t, wakeReasonUndecodable, dead[0].Reason)
}

// TestWakeService_RunExitsOnContextCancel: Run returns once ctx is
// cancelled, rather than looping forever.
func TestWakeService_RunExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	w, _, _, _ := newTestWakeService(t, "http://unused.invalid", time.Now)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Let Run reach its poll-interval wait at least once before cancelling.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

// TestClassifyWakeResult pins the classify matrix from the task brief as a
// pure function, independent of any queue or HTTP plumbing.
func TestClassifyWakeResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  TurnResult
		err  error
		want wakeAction
	}{
		{"not agent", TurnResult{}, ErrNotAgent, wakeActionKillNotAgent},
		{"session quota", TurnResult{}, ErrSessionQuota, wakeActionKillSessionQuota},
		{"transport error", TurnResult{}, errors.New("dial: connection refused"), wakeActionRetry},
		{"200 ack", TurnResult{StatusCode: 200}, nil, wakeActionAck},
		{"204 ack", TurnResult{StatusCode: 204}, nil, wakeActionAck},
		{"299 ack", TurnResult{StatusCode: 299}, nil, wakeActionAck},
		{"400 kill http4xx", TurnResult{StatusCode: 400}, nil, wakeActionKillHTTP4xx},
		{"404 kill http4xx", TurnResult{StatusCode: 404}, nil, wakeActionKillHTTP4xx},
		{"408 retry", TurnResult{StatusCode: 408}, nil, wakeActionRetry},
		{"429 retry", TurnResult{StatusCode: 429}, nil, wakeActionRetry},
		{"499 kill http4xx", TurnResult{StatusCode: 499}, nil, wakeActionKillHTTP4xx},
		{"500 retry", TurnResult{StatusCode: 500}, nil, wakeActionRetry},
		{"502 retry", TurnResult{StatusCode: 502}, nil, wakeActionRetry},
		{"error wins over 2xx", TurnResult{StatusCode: 200}, errors.New("x"), wakeActionRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, classifyWakeResult(tc.res, tc.err))
		})
	}
}
