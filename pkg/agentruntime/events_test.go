// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// pipeResponseWriter is an in-memory http.ResponseWriter backed by an
// io.PipeWriter: Write blocks — a channel operation internally, per
// io.Pipe's implementation — until a reader consumes it, which is exactly
// the "durably blocked" condition testing/synctest recognizes. A real
// socket write is NOT recognized that way (goroutines httptest.NewServer
// spawns from inside a synctest bubble would join it, then block on real
// syscalls synctest cannot reason about), so this fake is what lets the
// streaming EventsHandler tests below run inside a synctest.Test bubble at
// all, exercising real timer-driven behavior (keepalive, the poll ticker)
// with no wall-clock sleeps.
type pipeResponseWriter struct {
	header http.Header
	w      *io.PipeWriter
}

func newPipeResponseWriter(w *io.PipeWriter) *pipeResponseWriter {
	return &pipeResponseWriter{header: http.Header{}, w: w}
}

func (p *pipeResponseWriter) Header() http.Header { return p.header }

// WriteHeader discards the status code entirely — this fake exists only for
// the streaming tests below, none of which need to observe it (they never
// reach an error status once past auth). A test that DOES need a status
// assertion — see TestEventsHandlerUnauthorizedWithoutToken — uses
// httptest.NewRecorder instead, since that 401 path returns immediately
// with no blocking Write for this fake's synctest-safety to matter.
func (p *pipeResponseWriter) WriteHeader(int) {}

func (p *pipeResponseWriter) Write(b []byte) (int, error) {
	return p.w.Write(b)
}

// Flush satisfies http.Flusher so http.NewResponseController(p).Flush()
// succeeds instead of returning http.ErrNotSupported: io.Pipe has no
// buffering of its own (Write already blocks until fully consumed), so
// there is nothing to flush.
func (p *pipeResponseWriter) Flush() {}

// runEventsHandler starts h.ServeHTTP against a fresh io.Pipe-backed
// response in its own goroutine, which MUST be spawned from inside a
// synctest.Test closure (so it joins that bubble). It returns a reader for
// the response body and a stop function that cancels the request context
// and closes the pipe (unblocking a Write the handler may be mid-flight
// on), then waits for the handler to return. Every call site MUST call
// stop before its enclosing synctest.Test closure returns — synctest fails
// a test that leaves goroutines running past the end of the bubble.
func runEventsHandler(t *testing.T, h *EventsHandler, req *http.Request) (r *bufio.Reader, stop func()) {
	t.Helper()
	pr, pw := io.Pipe()
	w := newPipeResponseWriter(pw)

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, req)
	}()

	return bufio.NewReader(pr), func() {
		cancel()
		_ = pr.Close()
		<-done
	}
}

// readSSELine reads one line, minus its trailing "\n", from r. Used only
// inside a synctest bubble: a read that never completes durably blocks, and
// synctest reports the whole bubble as deadlocked — a clear test failure
// rather than a hang — so no wall-clock read deadline is needed here
// (contrast with dispatcher_test.go's non-bubbled streaming test, which
// does use one).
func readSSELine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSuffix(line, "\n")
}

// readSSEEvent reads one `event:`/`data:` frame plus its trailing blank
// line, and returns the two payloads with their field prefixes stripped.
func readSSEEvent(t *testing.T, r *bufio.Reader) (eventType, data string) {
	t.Helper()
	eventLine := readSSELine(t, r)
	require.True(t, strings.HasPrefix(eventLine, "event: "), "eventLine=%q", eventLine)
	dataLine := readSSELine(t, r)
	require.True(t, strings.HasPrefix(dataLine, "data: "), "dataLine=%q", dataLine)
	blank := readSSELine(t, r)
	require.Equal(t, "", blank)
	return strings.TrimPrefix(eventLine, "event: "), strings.TrimPrefix(dataLine, "data: ")
}

// assertNoPendingMessage fails the test if c has a message already queued.
func assertNoPendingMessage(t *testing.T, c *sseClient) {
	t.Helper()
	select {
	case msg, ok := <-c.ch:
		t.Fatalf("unexpected pending message: ok=%v msg=%+v", ok, msg)
	default:
	}
}

// TestPollerEmitsOneEventPerChangedRecord proves the core poll-diff
// contract: a record mutated between two ticks produces exactly one
// "session" event on the tick after the mutation, carrying the new state —
// no event on a tick where nothing changed, and no duplicate for the same
// change on a later tick.
func TestPollerEmitsOneEventPerChangedRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		view := NewAgentView()
		view.Upsert(registryTestEntry("ns", "agent1", 0))

		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), view, store, bus, time.Second)
		client := bus.subscribe(AuthScope{Wildcard: true})

		ctx, cancel := context.WithCancel(t.Context())
		go poller.Run(ctx)

		// First tick: the view goes from empty to one agent (an "agents"
		// event), and the pre-existing session is new to the poller's
		// snapshot (a "session" event for it).
		synctest.Sleep(time.Second)
		synctest.Wait()
		first := <-client.ch
		assert.Equal(t, "agents", first.eventType)
		second := <-client.ch
		assert.Equal(t, "session", second.eventType)
		assertNoPendingMessage(t, client)

		// A tick with no change at all: nothing published.
		synctest.Sleep(time.Second)
		synctest.Wait()
		assertNoPendingMessage(t, client)

		// Mutate the record directly between ticks, exactly as a live turn
		// or the sweeper would (a CAS write against the store), bypassing
		// the poller entirely.
		_, version, err := store.Get(t.Context(), "ns", "agent1", "s1")
		require.NoError(t, err)
		rec.Status = StatusIdle
		require.NoError(t, store.Update(t.Context(), rec, version, time.Hour))

		synctest.Sleep(time.Second)
		synctest.Wait()

		msg := <-client.ch
		assert.Equal(t, "session", msg.eventType)
		var evt sessionEvent
		require.NoError(t, json.Unmarshal(msg.data, &evt))
		assert.Equal(t, StatusIdle, evt.Record.Status)
		assertNoPendingMessage(t, client) // exactly one event for the one changed record

		cancel()
		synctest.Wait()
	})
}

// TestPollerPublishesAgentsEventOnViewChange proves the second half of the
// poll-diff contract: the bare {"type":"agents"} event fires exactly once
// when the agent-enabled Function set itself changes between ticks, and
// never on a tick where it didn't.
func TestPollerPublishesAgentsEventOnViewChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		view := NewAgentView()

		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), view, store, bus, time.Second)
		client := bus.subscribe(AuthScope{Wildcard: true})

		ctx, cancel := context.WithCancel(t.Context())
		go poller.Run(ctx)

		// Empty view -> empty view: no change, nothing published.
		synctest.Sleep(time.Second)
		synctest.Wait()
		assertNoPendingMessage(t, client)

		view.Upsert(registryTestEntry("ns", "agent1", 0))

		synctest.Sleep(time.Second)
		synctest.Wait()
		msg := <-client.ch
		assert.Equal(t, "agents", msg.eventType)
		assert.JSONEq(t, string(agentsEventJSON), string(msg.data))
		assertNoPendingMessage(t, client)

		cancel()
		synctest.Wait()
	})
}

// pagingFailureKV wraps a real statestore.KVStore and forces a List failure
// on a chosen call number, regardless of how many real records exist: it
// silently shrinks whatever Limit the caller asked for down to 1 on every
// successful call, so a scope holding >= 2 keys always yields a non-empty
// Next token and a second List call is always attempted. This is what lets
// TestPollerSurvivesMidPaginationListError exercise a page-2 failure with
// only two real session records, instead of needing enough records to
// force genuine pagination at the real pollPageLimit (500).
type pagingFailureKV struct {
	statestore.KVStore
	calls      int
	failOnCall int
}

var errInjectedListFailure = errors.New("injected list failure")

func (k *pagingFailureKV) List(ctx context.Context, s statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	k.calls++
	if k.calls == k.failOnCall {
		return statestore.KeyPage{}, errInjectedListFailure
	}
	return k.KVStore.List(ctx, s, prefix, statestore.Page{Token: page.Token, Limit: 1})
}

// TestPollerSurvivesMidPaginationListError is the mitigation for the
// partial-commit bug seedFromPreviousSnapshot exists to prevent: a List
// error partway through paging one agent's sessions must not make its
// unread sessions vanish from that tick's snapshot, nor make them look
// spuriously "new" once the failure clears. The scenario: tick 1 succeeds
// fully (both sessions land in the snapshot via two size-1 pages); tick 2's
// second page (of the same two sessions, now re-paginated from scratch)
// fails. The record collectAgentSessions never reached on tick 2 must be
// backfilled from tick 1's snapshot — proven by (a) no event for it on the
// next successful tick 3 (it was never "new" to begin with) and (b) its
// value in a snapshot replay after tick 2 still matches what tick 1 saw.
func TestPollerSurvivesMidPaginationListError(t *testing.T) {
	t.Parallel()
	kv := &pagingFailureKV{KVStore: newTestKV(t), failOnCall: 4}
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	view.Upsert(registryTestEntry("ns", "agent1", 0))

	ts := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	rec1 := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: ts}
	rec2 := SessionRecord{ID: "s2", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: ts}
	require.NoError(t, store.Create(t.Context(), rec1, 0, time.Hour))
	require.NoError(t, store.Create(t.Context(), rec2, 0, time.Hour))

	bus := NewBroadcaster()
	poller := NewPoller(logr.Discard(), view, store, bus, time.Second)
	client := bus.subscribe(AuthScope{Wildcard: true})

	// Tick 1: both size-1 pages succeed (calls 1 and 2). Full baseline
	// snapshot, both records reported as new (plus the empty->1-agent
	// "agents" event, published before the session diffs).
	poller.pollOnce(t.Context())
	drainAgentsEvent(t, client)
	drainSessionEvents(t, client, 2)

	// Tick 2: page 1 succeeds (call 3, returns one record + a Next token),
	// page 2 fails (call 4 == failOnCall). collectAgentSessions must
	// backfill the unread record from tick 1's snapshot rather than drop
	// it.
	poller.pollOnce(t.Context())
	assertNoPendingMessage(t, client) // neither record actually changed, so nothing new to report even for the one re-fetched on page 1

	snap := poller.Snapshot(AuthScope{Wildcard: true})
	require.Len(t, snap, 2, "both records must still be present after the mid-pagination error")
	byID := map[string]SessionRecord{}
	for _, r := range snap {
		byID[r.ID] = r
	}
	assert.Equal(t, StatusActive, byID["s1"].Status)
	assert.Equal(t, StatusActive, byID["s2"].Status)

	// Tick 3: a normal, fully successful poll. If tick 2's backfilled
	// record had been silently dropped instead of carried over, it would
	// look "new" here and a phantom session event would fire.
	poller.pollOnce(t.Context())
	assertNoPendingMessage(t, client)

	bus.unsubscribe(client)
}

// drainSessionEvents reads and discards exactly n "session" events from c.
func drainSessionEvents(t *testing.T, c *sseClient, n int) {
	t.Helper()
	for range n {
		msg := <-c.ch
		require.Equal(t, "session", msg.eventType)
	}
}

// drainAgentsEvent reads and discards exactly one "agents" event from c.
func drainAgentsEvent(t *testing.T, c *sseClient) {
	t.Helper()
	msg := <-c.ch
	require.Equal(t, "agents", msg.eventType)
}

// TestEventsHandlerSnapshotOnConnect proves a newly connected client
// replays the poller's current snapshot before anything else. The poller's
// cache is seeded with a single deterministic pollOnce call rather than a
// running ticker, since this test cares about connect-time replay content,
// not poll cadence (TestPollerEmitsOneEventPerChangedRecord above covers
// that).
func TestEventsHandlerSnapshotOnConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		view := NewAgentView()
		view.Upsert(registryTestEntry("ns", "agent1", 0))

		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), view, store, bus, time.Second)
		poller.pollOnce(t.Context())

		h := NewEventsHandler(t.Context(), poller, bus, NewAuthorizer(nil)) // auth disabled: wildcard scope

		req := httptest.NewRequest(http.MethodGet, "/registry/events", nil).WithContext(t.Context())
		r, stop := runEventsHandler(t, h, req)

		eventType, data := readSSEEvent(t, r)
		assert.Equal(t, "session", eventType)
		var evt sessionEvent
		require.NoError(t, json.Unmarshal([]byte(data), &evt))
		assert.Equal(t, "s1", evt.Record.ID)
		assert.Equal(t, StatusActive, evt.Record.Status)

		stop()
	})
}

// listCountingKV wraps a KVStore and counts List calls, to prove the poller
// does or does not touch the store on a given tick.
type listCountingKV struct {
	statestore.KVStore
	lists atomic.Int64
}

func (k *listCountingKV) List(ctx context.Context, s statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	k.lists.Add(1)
	return k.KVStore.List(ctx, s, prefix, page)
}

// TestPollerSkipsPollWhenNoSubscribers pins the CPU half of fix #6: with zero
// subscribers the Run loop must NOT run its O(sessions) list-diff cycle, and
// it must resume the moment a client subscribes.
func TestPollerSkipsPollWhenNoSubscribers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := &listCountingKV{KVStore: newTestKV(t)}
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		view.Upsert(registryTestEntry("ns", "agent1", 0))
		require.NoError(t, store.Create(t.Context(), SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}, 0, time.Hour))

		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), view, store, bus, time.Second)

		ctx, cancel := context.WithCancel(t.Context())
		go poller.Run(ctx)

		// Several ticks with no subscribers: the store is never listed.
		synctest.Sleep(3 * time.Second)
		synctest.Wait()
		assert.EqualValues(t, 0, kv.lists.Load(), "the poller must not touch the store while nobody is subscribed")

		// A subscriber arrives: polling resumes on the next tick.
		bus.subscribe(AuthScope{Wildcard: true})
		synctest.Sleep(time.Second)
		synctest.Wait()
		assert.Positive(t, kv.lists.Load(), "the poller must resume listing once a client subscribes")

		cancel()
		synctest.Wait()
	})
}

// TestEventsHandlerConnectPrimesSnapshot pins the correctness half of fix #6:
// because Run skips ticks with no subscribers, a client connecting to an
// idle/fresh process would replay an empty snapshot — so ServeHTTP must prime
// synchronously on connect. The poller is deliberately NEVER Run and NEVER
// pre-polled here; the session below can only appear in the connect-time
// replay if the handler primed the snapshot itself.
func TestEventsHandlerConnectPrimesSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		view := NewAgentView()
		view.Upsert(registryTestEntry("ns", "agent1", 0))
		require.NoError(t, store.Create(t.Context(), SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}, 0, time.Hour))

		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), view, store, bus, time.Second) // never Run, never pre-polled
		h := NewEventsHandler(t.Context(), poller, bus, NewAuthorizer(nil))

		req := httptest.NewRequest(http.MethodGet, "/registry/events", nil).WithContext(t.Context())
		r, stop := runEventsHandler(t, h, req)

		eventType, data := readSSEEvent(t, r)
		assert.Equal(t, "session", eventType)
		var evt sessionEvent
		require.NoError(t, json.Unmarshal([]byte(data), &evt))
		assert.Equal(t, "s1", evt.Record.ID, "connect-time replay must reflect live state primed on connect")

		stop()
	})
}

// TestEventsHandlerSlowClientDroppedWithoutStallingFastClient is the
// slow-consumer test: a client that never reads its response body must be
// disconnected once its buffered channel overflows, and must never stall
// delivery to a second, well-behaved client subscribed at the same time.
func TestEventsHandlerSlowClientDroppedWithoutStallingFastClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), NewAgentView(), store, bus, time.Second) // never Run: publish-driven only
		h := NewEventsHandler(t.Context(), poller, bus, NewAuthorizer(nil))

		slowReq := httptest.NewRequest(http.MethodGet, "/registry/events", nil).WithContext(t.Context())
		_, stopSlow := runEventsHandler(t, h, slowReq) // deliberately never read from

		fastReq := httptest.NewRequest(http.MethodGet, "/registry/events", nil).WithContext(t.Context())
		rFast, stopFast := runEventsHandler(t, h, fastReq)

		// Let both handlers subscribe and settle into their serve loop
		// (empty snapshot for both — nothing was ever polled) before
		// publishing anything.
		synctest.Wait()
		require.Equal(t, 2, bus.count())

		const n = sseClientBufferSize + 5
		for i := range n {
			payload := []byte(fmt.Sprintf(`{"n":%d}`, i))
			bus.publish("", feedMessage{eventType: "session", data: payload})

			// Drain the fast client every iteration — this is what keeps it
			// under its buffer cap while the slow client (never read)
			// overflows and gets dropped partway through the loop.
			eventType, data := readSSEEvent(t, rFast)
			assert.Equal(t, "session", eventType)
			assert.Equal(t, string(payload), data)
		}
		synctest.Wait()

		// The slow client overflowed its cap-64 buffer and was dropped; the
		// fast client is still subscribed, having received all n messages
		// above without ever being stalled by the slow one.
		assert.Equal(t, 1, bus.count())

		stopFast()
		stopSlow() // unblocks the slow handler's stuck first Write and waits for it to return
	})
}

// TestEventsHandlerNamespaceFiltering proves session events are filtered to
// the connected client's token scope at send time: a client scoped to only
// ns-a never observes a message published for ns-b, even interleaved
// between two ns-a messages it does receive. The token rides the ?token=
// query parameter — the EventSource transport this route exists for. It
// also proves the deliberate exception to that filter: an "agents" event
// (published with ns="") reaches the client regardless of scope, since it
// carries no per-record data for a scope check to apply to.
func TestEventsHandlerNamespaceFiltering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		key := []byte("test-signing-key")
		authz := NewAuthorizer(key)
		tokA := mintToken(t, key, jwt.MapClaims{
			"sub":                "caller-a",
			"allowed_namespaces": []any{"ns-a"},
			"exp":                time.Now().Add(time.Hour).Unix(),
		})

		store := NewSessionStore(newTestKV(t), time.Now)
		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), NewAgentView(), store, bus, time.Second)
		h := NewEventsHandler(t.Context(), poller, bus, authz)

		req := httptest.NewRequest(http.MethodGet, "/registry/events?token="+tokA, nil).WithContext(t.Context())
		r, stop := runEventsHandler(t, h, req)
		synctest.Wait() // subscribed and blocked in its serve loop; empty snapshot

		bus.publish("ns-a", feedMessage{eventType: "session", data: []byte(`{"n":"a1"}`)})
		bus.publish("ns-b", feedMessage{eventType: "session", data: []byte(`{"n":"b"}`)}) // must never arrive
		bus.publish("", feedMessage{eventType: "agents", data: agentsEventJSON})          // bypasses the scope filter
		bus.publish("ns-a", feedMessage{eventType: "session", data: []byte(`{"n":"a2"}`)})

		_, data1 := readSSEEvent(t, r)
		assert.Equal(t, `{"n":"a1"}`, data1)
		eventType3, data3 := readSSEEvent(t, r)
		assert.Equal(t, "agents", eventType3)
		assert.JSONEq(t, string(agentsEventJSON), data3)
		_, data2 := readSSEEvent(t, r)
		assert.Equal(t, `{"n":"a2"}`, data2) // proves the ns-b message never landed in between

		stop()
	})
}

// TestEventsHandlerKeepaliveAppears proves the ": keepalive" comment line
// is written once sseKeepaliveInterval elapses with nothing else to send —
// advanced with synctest's fake clock, no wall-clock sleep.
func TestEventsHandlerKeepaliveAppears(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := NewSessionStore(newTestKV(t), time.Now)
		bus := NewBroadcaster()
		poller := NewPoller(logr.Discard(), NewAgentView(), store, bus, time.Second)
		h := NewEventsHandler(t.Context(), poller, bus, NewAuthorizer(nil))

		req := httptest.NewRequest(http.MethodGet, "/registry/events", nil).WithContext(t.Context())
		r, stop := runEventsHandler(t, h, req)

		synctest.Sleep(sseKeepaliveInterval)
		synctest.Wait()

		assert.Equal(t, ": keepalive", readSSELine(t, r))

		stop()
	})
}

// TestEventsHandlerUnauthorizedWithoutToken proves the bare-mount contract:
// with auth enabled and no token on either transport, the handler answers
// 401 without ever subscribing to the broadcaster. httptest.NewRecorder is
// safe here (no synctest/io.Pipe machinery needed) because this path
// returns immediately — it never blocks on a streaming Write.
func TestEventsHandlerUnauthorizedWithoutToken(t *testing.T) {
	t.Parallel()
	store := NewSessionStore(newTestKV(t), time.Now)
	bus := NewBroadcaster()
	poller := NewPoller(logr.Discard(), NewAgentView(), store, bus, time.Second)
	h := NewEventsHandler(t.Context(), poller, bus, NewAuthorizer([]byte("test-signing-key")))

	req := httptest.NewRequest(http.MethodGet, "/registry/events", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, 0, bus.count())
}

// TestBearerFromRequest covers both token transports GET /registry/events
// accepts — ?token= (what a browser EventSource, unable to set request
// headers, must use) and a standard Authorization: Bearer header (for any
// other client) — plus the precedence between them and the absent case.
func TestBearerFromRequest(t *testing.T) {
	t.Parallel()

	t.Run("query token", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodGet, "/registry/events?token=tok-query", nil)
		assert.Equal(t, "tok-query", bearerFromRequest(r))
	})

	t.Run("authorization header", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodGet, "/registry/events", nil)
		r.Header.Set("Authorization", "Bearer tok-header")
		assert.Equal(t, "tok-header", bearerFromRequest(r))
	})

	t.Run("query token takes precedence over header", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodGet, "/registry/events?token=tok-query", nil)
		r.Header.Set("Authorization", "Bearer tok-header")
		assert.Equal(t, "tok-query", bearerFromRequest(r))
	})

	t.Run("neither present", func(t *testing.T) {
		t.Parallel()
		r := httptest.NewRequest(http.MethodGet, "/registry/events", nil)
		assert.Equal(t, "", bearerFromRequest(r))
	})
}
