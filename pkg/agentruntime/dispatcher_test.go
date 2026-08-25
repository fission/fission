// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/statestore"
)

// newTestDispatcher returns a Dispatcher wired to a fresh in-memory
// SessionStore and empty AgentView, forwarding to upstreamURL.
func newTestDispatcher(t *testing.T, upstreamURL string) (*Dispatcher, *AgentView, *SessionStore) {
	t.Helper()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstreamURL, time.Hour, time.Now, 0)
	return d, view, store
}

// newTestDispatcherWithClock is newTestDispatcher with an injected clock
// shared by the store and the dispatcher, for tests that need to order two
// writes unambiguously (real time.Now() calls microseconds apart can tie).
func newTestDispatcherWithClock(t *testing.T, upstreamURL string, now func() time.Time) (*Dispatcher, *AgentView, *SessionStore) {
	t.Helper()
	kv := newTestKV(t)
	store := NewSessionStore(kv, now)
	view := NewAgentView()
	d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstreamURL, time.Hour, now, 0)
	return d, view, store
}

// stepClock returns a clock that advances by 1ms on every call, starting
// after start. Each call is guaranteed strictly greater than the last (safe
// for concurrent callers), which makes a later write's timestamp
// unambiguously After() an earlier one — unlike two real time.Now() calls a
// few instructions apart, which can tie on a coarse system clock.
func stepClock(start time.Time) func() time.Time {
	var n atomic.Int64
	return func() time.Time {
		return start.Add(time.Duration(n.Add(1)) * time.Millisecond)
	}
}

// headerEntry is the default header-sourced AgentEntry used by most cases.
func headerEntry(ns, name string, maxSessions int64) AgentEntry {
	return AgentEntry{
		Namespace:     ns,
		Name:          name,
		SessionSource: fv1.SessionSourceHeader,
		SessionName:   fv1.DefaultAgentSessionHeader,
		IdleAfter:     5 * time.Minute,
		ArchiveAfter:  time.Hour,
		MaxSessions:   maxSessions,
	}
}

func okUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDispatcher_MintsSessionAndCreatesActiveRecord(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	sessionID := rec.Header().Get(HeaderSession)
	require.NotEmpty(t, sessionID, "a session id must be minted and returned")

	got, _, err := store.Get(t.Context(), "ns", "fn", sessionID)
	require.NoError(t, err)
	assert.Equal(t, StatusActive, got.Status)
	assert.EqualValues(t, 1, got.Stats.Turns)
}

func TestDispatcher_ExistingSessionIsReused(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
		req.Header.Set(HeaderSession, "sess-existing")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "sess-existing", rec.Header().Get(HeaderSession))
	}

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-existing")
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Stats.Turns, "the second turn must accumulate onto the same record")
}

// TestDispatcher_YieldWaitingFlipsSessionIdleAndRefreshesLastActive pins R6:
// LastActiveAt must be refreshed by the step-5 write on BOTH branches, not
// just the "stay active" one. It isolates step-3's write (captured at the
// gate, before the upstream handler is released) from step-5's write
// (captured after) using a gate — like the flush test — plus a
// strictly-monotonic fake clock, so the two timestamps can never tie: if
// step 5 skips the LastActiveAt refresh on the waiting branch, "got" equals
// "mid" exactly and the After() assertion fails.
func TestDispatcher_YieldWaitingFlipsSessionIdleAndRefreshesLastActive(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	reachedUpstream := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reachedUpstream)
		<-release
		w.Header().Set(HeaderYield, YieldWaiting)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	t.Cleanup(upstream.Close)

	clock := stepClock(time.Now())
	d, view, store := newTestDispatcherWithClock(t, upstream.URL, clock)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-yield")
	respRec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		d.Handler().ServeHTTP(respRec, req)
		close(done)
	}()

	<-reachedUpstream // step 3's Get/Create/Update has already committed by now
	mid, _, err := store.Get(t.Context(), "ns", "fn", "sess-yield")
	require.NoError(t, err)

	releaseOnce()
	<-done

	require.Equal(t, http.StatusOK, respRec.Code)
	assert.Equal(t, YieldWaiting, respRec.Header().Get(HeaderYield), "the yield outcome must be surfaced on the response")

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-yield")
	require.NoError(t, err)
	assert.Equal(t, StatusIdle, got.Status)
	assert.True(t, got.LastActiveAt.After(mid.LastActiveAt), "LastActiveAt must be refreshed by the post-response write on BOTH branches, including waiting->idle (R6)")
}

func TestDispatcher_Upstream500IncrementsErrorsAndPassesThrough(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(upstream.Close)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-err")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "boom", rec.Body.String())

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-err")
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Stats.Errors)
	assert.EqualValues(t, 1, got.Stats.Turns)
}

func TestDispatcher_TransportFailureRecordsTurnAndError(t *testing.T) {
	t.Parallel()
	// Bind then immediately close, giving a URL nothing listens on so Do
	// fails with a connection-refused error rather than hanging.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	d, view, store := newTestDispatcher(t, deadURL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-transport")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-transport")
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Stats.Turns, "a transport failure still counts as a turn")
	assert.EqualValues(t, 1, got.Stats.Errors, "a transport failure must be metered as an error")
	assert.Equal(t, StatusActive, got.Status)
}

func TestDispatcher_ConcurrentCreateRaceActivatesInsteadOfDropping(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
			req.Header.Set(HeaderSession, "sess-race")
			rec := httptest.NewRecorder()
			d.Handler().ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for _, code := range codes {
		assert.Equal(t, http.StatusOK, code, "a racing Create must never surface as a client-visible error")
	}
	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-race")
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Stats.Turns, "both racing turns must still be counted, not silently dropped")
}

// errInjectKV wraps a KVStore and forces Get to fail with a chosen error,
// simulating a degraded statestore. It deliberately embeds statestore.KVStore
// rather than satisfying CountedKV, which is fine: the fail-closed paths under
// test are reached on the Get error, before any counted Create.
type errInjectKV struct {
	statestore.KVStore
	getErr error
	setErr error
}

func (k *errInjectKV) Get(ctx context.Context, s statestore.Scope, key string) (statestore.Value, error) {
	if k.getErr != nil {
		return statestore.Value{}, k.getErr
	}
	return k.KVStore.Get(ctx, s, key)
}

func (k *errInjectKV) Set(ctx context.Context, s statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	if k.setErr != nil {
		return k.setErr
	}
	return k.KVStore.Set(ctx, s, key, val, o)
}

// TestDispatcher_PhantomEnqueueSuppressedWhenWriteLost pins the second half of
// fix #1: when the step-5 CAS write never lands (every Update fails), the
// continue-enqueue decision rode on a turn count that was never persisted — a
// phantom — and must be suppressed. The record is pre-created on the inner
// store so step-5's Get succeeds and the mutate closure runs (setting
// doEnqueue=true); the wrapper then fails the Update, so persisted=false must
// reset doEnqueue and the wired enqueuer must never be called. Pre-fix, the
// enqueuer fired with the phantom count.
func TestDispatcher_PhantomEnqueueSuppressedWhenWriteLost(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	inner := newTestKV(t)
	// Pre-create the record so step-5's Get finds it before the wrapper starts
	// failing writes.
	seed := NewSessionStore(inner, time.Now)
	require.NoError(t, seed.Create(t.Context(), SessionRecord{
		ID: "sess-p", Agent: "fn", Namespace: "ns", Status: StatusActive,
		CreatedAt: time.Now(), LastActiveAt: time.Now(),
	}, 0, time.Hour))

	kv := &errInjectKV{KVStore: inner, setErr: errors.New("writes are down")}
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	view.Upsert(headerEntry("ns", "fn", 0))
	d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 0)
	fe := &fakeEnqueuer{}
	d.SetWakeEnqueuer(fe)

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hi"))
	req.Header.Set(HeaderSession, "sess-p")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "a lost bookkeeping write must not fail the turn")
	assert.Equal(t, 0, fe.callCount(), "no enqueue may fire on a turn count that was never persisted")
}

// TestDispatcher_StoreDownFailsClosedWhenQuotaBearing pins fix #2: when the
// agent enforces a MaxSessions budget and the session store is unhealthy (a
// Get that fails for a reason other than ErrNotFound), the turn is REJECTED
// with 503 and never forwarded — a store outage must not evaporate the quota
// gate and let unmetered sessions dispatch. The converse (a non-quota agent
// keeps the "bookkeeping never gates" rule) is pinned by the sub-test below.
func TestDispatcher_StoreDownFailsClosedWhenQuotaBearing(t *testing.T) {
	t.Parallel()

	t.Run("quota-bearing agent fails closed (503)", func(t *testing.T) {
		t.Parallel()
		var upstreamHits atomic.Int64
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstream.Close)

		kv := &errInjectKV{KVStore: newTestKV(t), getErr: errors.New("statestore down")}
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		view.Upsert(headerEntry("ns", "fn", 5)) // MaxSessions=5: quota-bearing
		d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 0)

		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hi"))
		req.Header.Set(HeaderSession, "sess-x")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "session store unavailable")
		assert.EqualValues(t, 0, upstreamHits.Load(), "a fail-closed turn must never reach the upstream function")
	})

	t.Run("non-quota agent keeps forwarding (bookkeeping never gates)", func(t *testing.T) {
		t.Parallel()
		var upstreamHits atomic.Int64
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstream.Close)

		kv := &errInjectKV{KVStore: newTestKV(t), getErr: errors.New("statestore down")}
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		view.Upsert(headerEntry("ns", "fn", 0)) // MaxSessions=0: no quota to protect
		d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 0)

		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hi"))
		req.Header.Set(HeaderSession, "sess-y")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, "with no quota, a store read blip does not gate the turn")
		assert.EqualValues(t, 1, upstreamHits.Load(), "the turn is still forwarded")
	})
}

// TestDispatcher_CreateWriteFailureFailsClosedWhenQuotaBearing extends fix #2
// (TestDispatcher_StoreDownFailsClosedWhenQuotaBearing above) to the SIBLING
// code path that can leave the store unable to bookkeep a brand-new session:
// that test pins the Get-failure branch (dispatcher.go:412-420, a Get that
// fails for a reason other than ErrNotFound). This one pins the
// Create-failure branch (dispatcher.go:402-411): the Get correctly reports
// ErrNotFound (this session id has never been seen), but the subsequent
// Create call's underlying Set itself fails for a reason that is neither
// ErrSessionQuota nor a lost create-race — a write-path outage, not a
// read-path one. errInjectKV never satisfies statestore.CountedKV (it only
// embeds the narrower KVStore interface), so Create always takes the plain
// Set path regardless of MaxSessions, and setErr fires there.
func TestDispatcher_CreateWriteFailureFailsClosedWhenQuotaBearing(t *testing.T) {
	t.Parallel()

	t.Run("quota-bearing agent fails closed (503)", func(t *testing.T) {
		t.Parallel()
		var upstreamHits atomic.Int64
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstream.Close)

		kv := &errInjectKV{KVStore: newTestKV(t), setErr: errors.New("writes are down")}
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		view.Upsert(headerEntry("ns", "fn", 5)) // MaxSessions=5: quota-bearing
		d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 0)

		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hi"))
		req.Header.Set(HeaderSession, "sess-create-fail")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "session store unavailable")
		assert.EqualValues(t, 0, upstreamHits.Load(), "a fail-closed turn must never reach the upstream function")
	})

	t.Run("non-quota agent keeps forwarding (bookkeeping never gates)", func(t *testing.T) {
		t.Parallel()
		var upstreamHits atomic.Int64
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			upstreamHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstream.Close)

		kv := &errInjectKV{KVStore: newTestKV(t), setErr: errors.New("writes are down")}
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		view.Upsert(headerEntry("ns", "fn", 0)) // MaxSessions=0: no quota to protect
		d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 0)

		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hi"))
		req.Header.Set(HeaderSession, "sess-create-fail-ok")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, "with no quota, a Create write failure does not gate the turn")
		assert.EqualValues(t, 1, upstreamHits.Load(), "the turn is still forwarded")
	})
}

func TestDispatcher_SessionQuotaExceeded(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 1))

	req1 := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req1.Header.Set(HeaderSession, "sess-a")
	rec1 := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req2.Header.Set(HeaderSession, "sess-b")
	rec2 := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusTooManyRequests, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "session quota exceeded")
}

func TestDispatcher_RequestBodyTooLarge(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	huge := strings.NewReader(strings.Repeat("a", maxTurnBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", huge)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	sessionID := rec.Header().Get(HeaderSession)
	require.NotEmpty(t, sessionID, "the session id still resolves (and is reported) even though the turn is rejected")
	_, _, err := store.Get(t.Context(), "ns", "fn", sessionID)
	assert.ErrorIs(t, err, statestore.ErrNotFound, "an oversized request must never create a session record (R5)")
}

func TestDispatcher_NonAgentFunctionNotFound(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, _, _ := newTestDispatcher(t, upstream.URL)
	// No entry Upserted for ns/fn — it is not an agent function.

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not an agent function")
	assert.Empty(t, rec.Header().Get(HeaderSession), "no session header on a 404")
}

func TestDispatcher_GetMethodNotAllowed(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodGet, "/agents/ns/fn", nil)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDispatcher_InvalidSessionIDRejected(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "bad session id!")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDispatcher_QueryParamSessionSourceForwardsAndResolves(t *testing.T) {
	t.Parallel()
	var gotQueryParam string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueryParam = r.URL.Query().Get("session")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(AgentEntry{
		Namespace: "ns", Name: "fn",
		SessionSource: fv1.SessionSourceQueryParam, SessionName: "session",
		IdleAfter: time.Minute, ArchiveAfter: time.Hour,
	})

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn?session=abc-123", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "abc-123", rec.Header().Get(HeaderSession))
	assert.Equal(t, "abc-123", gotQueryParam, "the session id must be forwarded upstream under the entry's query param name")
}

func TestDispatcher_ResponseBodyPassesThroughByteIdentically(t *testing.T) {
	t.Parallel()
	payload := make([]byte, 1<<20) // 1 MiB
	_, err := rand.Read(payload)
	require.NoError(t, err)
	wantHash := sha256.Sum256(payload)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(upstream.Close)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	gotHash := sha256.Sum256(rec.Body.Bytes())
	assert.Equal(t, wantHash, gotHash)
}

// TestDispatcher_StreamsIncrementallyWithPerWriteFlush is the test that
// catches a missing per-write flush: it holds the upstream's second chunk
// behind a gate and asserts the client can read the first chunk before the
// gate is released. A plain io.Copy (which buffers) would make the first
// read block until the whole response — including the still-gated second
// chunk — is available, hanging this test.
func TestDispatcher_StreamsIncrementallyWithPerWriteFlush(t *testing.T) {
	t.Parallel()
	chunk1 := []byte("first-chunk-payload")
	chunk2 := []byte("second-chunk-payload")
	release := make(chan struct{})
	// releaseOnce is shared between the test body's deliberate release and a
	// t.Cleanup safety net for a failed/timed-out run — sync.OnceFunc makes
	// closing twice safe instead of panicking.
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler runs on the httptest server's own goroutine, not the
		// test goroutine: require's t.FailNow is documented as unsafe there,
		// so use assert (t.Errorf), which is goroutine-safe.
		rc := http.NewResponseController(w)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chunk1)
		assert.NoError(t, rc.Flush())
		<-release
		_, _ = w.Write(chunk2)
		assert.NoError(t, rc.Flush())
	}))
	t.Cleanup(upstream.Close)

	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	front := httptest.NewServer(d.Handler())
	t.Cleanup(front.Close)

	req, err := http.NewRequest(http.MethodPost, front.URL+"/agents/ns/fn", strings.NewReader("hello"))
	require.NoError(t, err)
	resp, err := front.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	got := make([]byte, len(chunk1))
	readDone := make(chan error, 1)
	go func() {
		_, rerr := io.ReadFull(resp.Body, got)
		readDone <- rerr
	}()

	select {
	case rerr := <-readDone:
		require.NoError(t, rerr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out reading the first chunk before the gate opened: the dispatcher is not flushing incrementally")
	}
	assert.Equal(t, chunk1, got)

	releaseOnce()

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, chunk2, rest)
}

// wakeCall is one recorded fakeEnqueuer.EnqueueWake invocation.
type wakeCall struct {
	ns, agent, session string
	turns              int64
}

// fakeEnqueuer is a test double for the enqueuer seam: it records every call
// and can be made to fail, for the "bookkeeping never gates a turn" test.
type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []wakeCall
	err   error
}

func (f *fakeEnqueuer) EnqueueWake(_ context.Context, ns, agent, session string, turns int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wakeCall{ns, agent, session, turns})
	return f.err
}

func (f *fakeEnqueuer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// discardSink is a minimal TurnSink for tests that call DispatchTurn
// directly (the wake path has no http.ResponseWriter to adapt).
type discardSink struct {
	statusCode  int
	contentType string
	yield       string
}

func (s *discardSink) Begin(statusCode int, contentType, yield string) {
	s.statusCode = statusCode
	s.contentType = contentType
	s.yield = yield
}

func (s *discardSink) Write(p []byte) (int, error) { return len(p), nil }

// TestDispatcher_YieldContinueEnqueuesWake pins the new continue-chain seam:
// an upstream response yielding "continue" must (a) count as a continuation
// on the record, (b) leave the session active (not idle), and (c) call the
// configured enqueuer with the post-increment turn count as the dedup basis.
func TestDispatcher_YieldContinueEnqueuesWake(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))
	fe := &fakeEnqueuer{}
	d.SetWakeEnqueuer(fe)

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-continue")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, YieldContinue, rec.Header().Get(HeaderYield))

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-continue")
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Stats.Continuations)
	assert.Equal(t, StatusActive, got.Status)

	fe.mu.Lock()
	defer fe.mu.Unlock()
	require.Len(t, fe.calls, 1)
	assert.Equal(t, wakeCall{"ns", "fn", "sess-continue", 1}, fe.calls[0], "the enqueuer must see (ns, agent, session, turns==1)")
}

// TestDispatcher_ContinuationCapStopsEnqueueing pins the maxContinuations cap:
// once a session's Continuations count exceeds the cap, further continue
// turns must still be metered but must NOT enqueue another wake.
func TestDispatcher_ContinuationCapStopsEnqueueing(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	view.Upsert(headerEntry("ns", "fn", 0))
	d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstream.URL, time.Hour, time.Now, 1)
	fe := &fakeEnqueuer{}
	d.SetWakeEnqueuer(fe)

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
		req.Header.Set(HeaderSession, "sess-cap")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-cap")
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Stats.Continuations, "both continue turns must still be metered past the cap")

	assert.Equal(t, 1, fe.callCount(), "the enqueuer must be called exactly once: the second continuation is past the cap of 1")
}

// TestDispatcher_HeaderTurnsReflectsPreTurnCount pins HeaderTurns: it must
// carry the record's Stats.Turns as read BEFORE the current turn, so a
// stateless upstream fixture can bound its own loop. It also pins the
// converse for the wake replay headers: a plain HTTP turn carries
// no WakeID, so HeaderWakeID/HeaderWakeAttempt must be absent, never set to
// an empty string a function could mistake for a wake delivery.
func TestDispatcher_HeaderTurnsReflectsPreTurnCount(t *testing.T) {
	t.Parallel()
	var gotHeaders []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Get(HeaderTurns))
		assert.Empty(t, r.Header.Get(HeaderWakeID), "wake headers must be absent on a plain HTTP turn")
		assert.Empty(t, r.Header.Get(HeaderWakeAttempt), "wake headers must be absent on a plain HTTP turn")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
		req.Header.Set(HeaderSession, "sess-turns")
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	require.Equal(t, []string{"0", "1"}, gotHeaders, "HeaderTurns must carry Stats.Turns as read BEFORE this turn")
}

// TestDispatcher_WakeTurnSetsWakeHeaders pins the wake-delivered replay
// headers, exercised directly via DispatchTurn since WakeID has no HTTP
// surface (it is set by the wake consumer, not the HTTP handler).
func TestDispatcher_WakeTurnSetsWakeHeaders(t *testing.T) {
	t.Parallel()
	var gotID, gotAttempt string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get(HeaderWakeID)
		gotAttempt = r.Header.Get(HeaderWakeAttempt)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	tr := TurnRequest{
		Namespace: "ns", Agent: "fn", SessionID: "sess-wake",
		Body: []byte("{}"), WakeID: "wake-1", WakeAttempt: 3,
	}
	sink := &discardSink{}
	_, err := d.DispatchTurn(t.Context(), tr, sink)
	require.NoError(t, err)
	assert.Equal(t, "wake-1", gotID)
	assert.Equal(t, "3", gotAttempt)
}

// TestDispatcher_EnqueuerErrorDoesNotFailTurn pins the bookkeeping-never-
// gates-a-turn rule for the continue-chain seam specifically: a failing
// enqueuer must still let the turn succeed from the caller's point of view.
func TestDispatcher_EnqueuerErrorDoesNotFailTurn(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldContinue)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))
	fe := &fakeEnqueuer{err: errors.New("boom")}
	d.SetWakeEnqueuer(fe)

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-enqueue-err")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "an enqueuer failure must not surface as a turn failure")
	assert.Equal(t, 1, fe.callCount(), "the enqueuer must still have been attempted")
}

// testEndpointIndex builds an endpointcache.Index seeded with one function's
// ready endpoints via ApplySlice — the same exported entry point the router's
// own informer feeds in production (endpointSliceHandlers), so this exercises
// the real slice-to-Endpoint projection rather than a hand-built fixture.
// ips are bare pod IPs (no port): ApplySlice composes the "ip:port" Address
// itself from the slice's port, exactly as endpointsFromSlice does — passing
// an already-"ip:port" string here would make endpointsFromSlice's
// url.Parse("http://" + ip + ":" + port) fail and silently drop the
// endpoint.
func testEndpointIndex(t *testing.T, ns, name string, ips ...string) *endpointcache.Index {
	t.Helper()
	ix := endpointcache.NewIndex()
	port := int32(8888)
	ready := true
	eps := make([]discoveryv1.Endpoint, 0, len(ips))
	for _, ip := range ips {
		eps = append(eps, discoveryv1.Endpoint{
			Addresses:  []string{ip},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	ix.ApplySlice(&discoveryv1.EndpointSlice{
		Name:      name + "-slice",
		Namespace: ns,
		Labels: map[string]string{
			fv1.FUNCTION_NAME:      name,
			fv1.FUNCTION_NAMESPACE: ns,
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   eps,
		Ports:       []discoveryv1.EndpointPort{{Port: &port}},
	})
	return ix
}

// TestDispatchTurn_CurrentPodPrediction pins the CurrentPod
// bookkeeping: with a non-nil index carrying ready endpoints, DispatchTurn's
// step-5 write must set SessionRecord.CurrentPod to exactly what
// endpointcache.PredictSticky would independently compute for the same
// (lookup, sessionID) — proving the wiring calls the real function rather
// than some ad hoc pick.
func TestDispatchTurn_CurrentPodPrediction(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	ix := testEndpointIndex(t, "ns", "fn", "10.0.0.1", "10.0.0.2")
	d.SetEndpointIndex(ix)

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-predict")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-predict")
	require.NoError(t, err)

	wantEp, ok := endpointcache.PredictSticky(ix.Lookup("ns", "fn", ""), "sess-predict")
	require.True(t, ok, "the test fixture must have a predictable ready endpoint")
	assert.Equal(t, wantEp.Address, got.CurrentPod)
	assert.NotEmpty(t, got.CurrentPod)
}

// TestDispatchTurn_CurrentPodPrediction_NilIndex pins the disabled-by-default
// contract: a Dispatcher that never had SetEndpointIndex called must leave
// CurrentPod empty rather than erroring or panicking.
func TestDispatchTurn_CurrentPodPrediction_NilIndex(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))
	// No SetEndpointIndex call: d.index stays nil (the zero value).

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-no-index")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-no-index")
	require.NoError(t, err)
	assert.Empty(t, got.CurrentPod, "a nil index must leave CurrentPod untouched")
}

// TestDispatchTurn_CurrentPodPrediction_NoReadyEndpoints pins the
// prediction-miss half of the "leave it untouched" contract: an index with no
// ready endpoint for the function (e.g. the pool hasn't specialized yet)
// must not clear a previously-set CurrentPod.
func TestDispatchTurn_CurrentPodPrediction_NoReadyEndpoints(t *testing.T) {
	t.Parallel()
	upstream := okUpstream(t)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))
	d.SetEndpointIndex(endpointcache.NewIndex()) // non-nil, but empty: no endpoints for ns/fn

	require.NoError(t, store.Create(t.Context(), SessionRecord{
		ID: "sess-stale", Agent: "fn", Namespace: "ns", Status: StatusActive,
		CreatedAt: time.Now(), LastActiveAt: time.Now(), CurrentPod: "10.0.0.9:8888",
	}, 0, time.Hour))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-stale")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-stale")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9:8888", got.CurrentPod, "a prediction miss must not clear a previously-known CurrentPod")
}

// isSessionIDCharset is an independent (non-regexp) predicate for
// sessionIDPattern's character class, so TestSessionIDPatternRapid's
// property is not just checking the regexp against a re-derivation of
// itself.
func isSessionIDCharset(s string) bool {
	for i := range len(s) {
		b := s[i]
		switch {
		case b >= 'A' && b <= 'Z':
		case b >= 'a' && b <= 'z':
		case b >= '0' && b <= '9':
		case b == '.' || b == '_' || b == '-':
		default:
			return false
		}
	}
	return true
}

// TestSessionIDPatternRapid pins sessionIDPattern as a property: a candidate
// is accepted iff it is 1-128 BYTES long and every byte is in the
// [A-Za-z0-9._-] class. The class is entirely single-byte ASCII, so a byte-
// wise oracle is exact even against multi-byte-rune input: any string
// containing a non-ASCII rune fails both the regexp (the rune itself is
// outside the character class) and isSessionIDCharset (its UTF-8 encoding's
// individual bytes fall outside every accepted byte range), so byte-length
// and rune-length never need to be reconciled.
//
// rapid.String() alone would almost never land an all-in-class string, so it
// would essentially only exercise rejection; mixing in
// rapid.StringMatching for the in-class alphabet gives the accept side real
// coverage too.
func TestSessionIDPatternRapid(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		s := rapid.OneOf(
			rapid.StringMatching(`[A-Za-z0-9._-]{0,200}`),
			rapid.String(),
		).Draw(rt, "candidate")

		got := sessionIDPattern.MatchString(s)
		want := len(s) >= 1 && len(s) <= 128 && isSessionIDCharset(s)
		if got != want {
			rt.Fatalf("sessionIDPattern.MatchString(%q) = %v, want %v (len=%d)", s, got, want, len(s))
		}
	})
}

// TestSessionIDPatternBoundary pins the exact 128/129-byte accept/reject
// boundary deterministically: rapid's random search is not guaranteed to
// land exactly on a boundary value, so this is not implied by
// TestSessionIDPatternRapid above.
func TestSessionIDPatternBoundary(t *testing.T) {
	t.Parallel()
	assert.True(t, sessionIDPattern.MatchString(strings.Repeat("a", 128)), "128 bytes must be accepted")
	assert.False(t, sessionIDPattern.MatchString(strings.Repeat("a", 129)), "129 bytes must be rejected")
}

// TestResolveSessionIDRejectsCheckpointPrefix pins M4: CheckpointKeyPrefix
// ("agentckpt.") is a reserved keyspace within function-state (history.go),
// and sessionIDPattern's charset otherwise permits a dot — so without this
// check a caller-supplied session id like "agentckpt.s2" would be accepted
// and could collide with session "s2"'s checkpoint KV key.
func TestResolveSessionIDRejectsCheckpointPrefix(t *testing.T) {
	t.Parallel()
	entry := AgentEntry{SessionSource: fv1.SessionSourceHeader, SessionName: HeaderSession}

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", nil)
	req.Header.Set(HeaderSession, CheckpointKeyPrefix+"s2")
	_, ok := resolveSessionID(req, entry)
	assert.False(t, ok, "a session id starting with the reserved checkpoint prefix must be rejected")

	// Sanity: an otherwise-identical id without the prefix is accepted, so
	// the rejection above is specifically about the prefix, not the charset.
	req2 := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", nil)
	req2.Header.Set(HeaderSession, "s2")
	got, ok2 := resolveSessionID(req2, entry)
	assert.True(t, ok2)
	assert.Equal(t, "s2", got)
}
