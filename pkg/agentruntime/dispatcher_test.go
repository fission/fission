// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// newTestDispatcher returns a Dispatcher wired to a fresh in-memory
// SessionStore and empty AgentView, forwarding to upstreamURL.
func newTestDispatcher(t *testing.T, upstreamURL string) (*Dispatcher, *AgentView, *SessionStore) {
	t.Helper()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	d := NewDispatcher(logr.Discard(), view, store, http.DefaultClient, upstreamURL, time.Hour, time.Now)
	return d, view, store
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

func TestDispatcher_YieldWaitingFlipsSessionIdle(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderYield, YieldWaiting)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	t.Cleanup(upstream.Close)
	d, view, store := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", strings.NewReader("hello"))
	req.Header.Set(HeaderSession, "sess-yield")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got, _, err := store.Get(t.Context(), "ns", "fn", "sess-yield")
	require.NoError(t, err)
	assert.Equal(t, StatusIdle, got.Status)
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
	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns", "fn", 0))

	huge := strings.NewReader(strings.Repeat("a", maxTurnBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/agents/ns/fn", huge)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(chunk1)
		require.NoError(t, rc.Flush())
		<-release
		_, _ = w.Write(chunk2)
		require.NoError(t, rc.Flush())
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

	close(release)

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, chunk2, rest)
}
