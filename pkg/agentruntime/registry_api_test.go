// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// registryTestEntry returns an AgentEntry with header-sourced sessions and
// distinct, easy-to-assert-on lifecycle fields.
func registryTestEntry(ns, name string, maxSessions int64) AgentEntry {
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

// newTestRegistryMux wires a mux exactly the way main.go mounts the registry
// read API: GET /registry/agents behind authz.HTTPMiddleware with the
// manual per-entry scope filter, and the {namespace}-carrying routes
// (including history/checkpoint) behind authz.Middleware. The returned
// EventLog is the same underlying handle hist reads through — tests seed
// history events onto it directly (as history_test.go's own tests do), since
// HistoryStore itself holds no append path.
func newTestRegistryMux(t *testing.T, authz *Authorizer) (http.Handler, *AgentView, *SessionStore, *HistoryStore, statestore.EventLog) {
	t.Helper()
	el, kv := newTestStores(t)
	store := NewSessionStore(kv, time.Now)
	hist := NewHistoryStore(el, kv)
	view := NewAgentView()
	api := NewRegistryAPI(logr.Discard(), view, store, hist, authz)

	mux := http.NewServeMux()
	mux.Handle("GET /registry/agents", authz.HTTPMiddleware(http.HandlerFunc(api.ListAgents)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions", authz.Middleware(http.HandlerFunc(api.ListSessions)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}", authz.Middleware(http.HandlerFunc(api.GetSession)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}/history", authz.Middleware(http.HandlerFunc(api.GetHistory)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}/checkpoint", authz.Middleware(http.HandlerFunc(api.GetCheckpoint)))
	return mux, view, store, hist, el
}

// bearerRequest builds a GET request against path, optionally carrying tok as
// a bearer token (tok == "" means no Authorization header).
func bearerRequest(method, path, tok string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v))
	return v
}

// --- GET /registry/agents ---

func TestListAgentsNamespaceScopedFiltersToScope(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 3))
	view.Upsert(registryTestEntry("ns-b", "fn-b", 0))

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents", tok))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[struct {
		Agents []agentSummary `json:"agents"`
	}](t, w)
	require.Len(t, body.Agents, 1)
	got := body.Agents[0]
	assert.Equal(t, "ns-a", got.Namespace)
	assert.Equal(t, "fn-a", got.Name)
	assert.Equal(t, string(fv1.SessionSourceHeader), got.SessionSource)
	assert.Equal(t, fv1.DefaultAgentSessionHeader, got.SessionName)
	assert.Equal(t, "5m0s", got.IdleAfter)
	assert.Equal(t, "1h0m0s", got.ArchiveAfter)
	assert.EqualValues(t, 3, got.MaxSessions)

	// Pin the wire field names literally: decoding through agentSummary's own
	// tags cannot catch a tag typo (it would round-trip through the same
	// wrong name), so check the raw JSON keys the brief specifies —
	// map[string]jsontext.Value the way authz.go's agentClaims.UnmarshalJSON
	// decodes an untyped document, rather than map[string]any.
	raw := decodeJSON[map[string]jsontext.Value](t, w)
	agentsRaw, ok := raw["agents"]
	require.True(t, ok)
	var agentRows []map[string]jsontext.Value
	require.NoError(t, json.Unmarshal(agentsRaw, &agentRows))
	require.Len(t, agentRows, 1)
	for _, key := range []string{"namespace", "name", "sessionSource", "sessionName", "idleAfter", "archiveAfter", "maxSessions"} {
		assert.Contains(t, agentRows[0], key)
	}
}

func TestListAgentsWildcardSeesAll(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))
	view.Upsert(registryTestEntry("ns-b", "fn-b", 0))

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": "*",
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents", tok))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[struct {
		Agents []agentSummary `json:"agents"`
	}](t, w)
	require.Len(t, body.Agents, 2)
	// ListAgents sorts by namespace then name for a deterministic response.
	assert.Equal(t, "ns-a", body.Agents[0].Namespace)
	assert.Equal(t, "ns-b", body.Agents[1].Namespace)
}

func TestListAgentsAuthDisabledWildcard(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	require.False(t, authz.Enabled())
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))
	view.Upsert(registryTestEntry("ns-b", "fn-b", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents", ""))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[struct {
		Agents []agentSummary `json:"agents"`
	}](t, w)
	assert.Len(t, body.Agents, 2)
}

func TestListAgentsNoTokenUnauthorized(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer([]byte("registry-test-key"))
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents", ""))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- GET /registry/agents/{namespace}/{name}/sessions ---

func TestListSessionsPaginationWalksNextToken(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil) // auth disabled: focus this test on pagination.
	mux, view, store, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	ctx := t.Context()
	for _, id := range []string{"s1", "s2", "s3"} {
		rec := SessionRecord{ID: id, Agent: "fn", Namespace: "ns", Status: StatusActive}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=2", ""))
	require.Equal(t, http.StatusOK, w.Code)
	page1 := decodeJSON[sessionsResponse](t, w)
	require.Len(t, page1.Sessions, 2)
	assert.Equal(t, "s1", page1.Sessions[0].ID)
	assert.Equal(t, "s2", page1.Sessions[1].ID)
	require.NotEmpty(t, page1.Next)

	// Pin the raw wire keys the brief specifies (see the analogous check in
	// TestListAgentsNamespaceScopedFiltersToScope for why this can't be
	// caught by decoding through sessionsResponse's own tags).
	raw := decodeJSON[map[string]jsontext.Value](t, w)
	assert.Contains(t, raw, "sessions")
	assert.Contains(t, raw, "next")

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=2&page="+page1.Next, ""))
	require.Equal(t, http.StatusOK, w2.Code)
	page2 := decodeJSON[sessionsResponse](t, w2)
	require.Len(t, page2.Sessions, 1)
	assert.Equal(t, "s3", page2.Sessions[0].ID)
	assert.Empty(t, page2.Next, "last page must not carry a next token")
}

func TestListSessionsDefaultAndCapLimit(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, store, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	ctx := t.Context()
	for _, id := range []string{"s1", "s2", "s3"} {
		rec := SessionRecord{ID: id, Agent: "fn", Namespace: "ns", Status: StatusActive}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))
	}

	t.Run("non-integer limit is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=abc", ""))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("limit above cap is clamped and still returns everything within it", func(t *testing.T) {
		// 3 seeded sessions is well within the 500 cap, so a clamp bug that
		// silently drops the request (rather than serving it at limit=500)
		// would show up here as a wrong count or an error, not just a 200.
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=9999", ""))
		require.Equal(t, http.StatusOK, w.Code)
		body := decodeJSON[sessionsResponse](t, w)
		assert.Len(t, body.Sessions, 3)
		assert.Empty(t, body.Next)
	})
}

// TestResolveSessionListLimit unit-tests the clamp/default/parse policy
// ListSessions delegates to, independent of the HTTP plumbing above.
func TestResolveSessionListLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		raw       string
		wantLimit int
		wantOK    bool
	}{
		{"empty uses default", "", defaultSessionListLimit, true},
		{"zero uses default", "0", defaultSessionListLimit, true},
		{"negative uses default", "-5", defaultSessionListLimit, true},
		{"within range passes through", "2", 2, true},
		{"exactly at cap passes through", "500", maxSessionListLimit, true},
		{"above cap is clamped down", "9999", maxSessionListLimit, true},
		{"non-integer is rejected", "abc", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			limit, ok := resolveSessionListLimit(tc.raw)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantLimit, limit)
			}
		})
	}
}

func TestListSessionsNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _, _, _ := newTestRegistryMux(t, authz)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/no-such-fn/sessions", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)

	body := decodeJSON[struct {
		Error string `json:"error"`
	}](t, w)
	assert.Equal(t, "not an agent function", body.Error)
}

func TestListSessionsMiddlewareWrongNamespaceForbidden(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))
	view.Upsert(registryTestEntry("ns-b", "fn-b", 0))

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-b/fn-b/sessions", tok))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestNamespaceScopedRoutesAuthEnabledCorrectNamespace is the positive
// counterpart to the wrong-namespace 403 tests above: with auth ENABLED, a
// token correctly scoped to the request's namespace must be admitted through
// BOTH {namespace}-carrying routes and see real data, not just a passing
// status code. Without this, a Middleware regression that 403'd every
// request regardless of scope would slip through the wrong-namespace tests
// undetected (they only ever exercise the mismatched-namespace path).
func TestNamespaceScopedRoutesAuthEnabledCorrectNamespace(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, store, _, el := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns-a", "fn-a", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	rec := SessionRecord{ID: "sess-1", Agent: "fn-a", Namespace: "ns-a", Status: StatusActive}
	require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

	stream := stateapi.StreamName("ns-a", "myks", HistoryClientStream("sess-1"))
	_, err := el.Append(t.Context(), stream, statestore.AppendAny, []statestore.Event{{Type: "turn.start"}})
	require.NoError(t, err)

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	t.Run("ListSessions", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-a/fn-a/sessions", tok))
		require.Equal(t, http.StatusOK, w.Code)
		body := decodeJSON[sessionsResponse](t, w)
		require.Len(t, body.Sessions, 1)
		assert.Equal(t, "sess-1", body.Sessions[0].ID)
	})

	t.Run("GetSession", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-a/fn-a/sessions/sess-1", tok))
		require.Equal(t, http.StatusOK, w.Code)
		got := decodeJSON[SessionRecord](t, w)
		assert.Equal(t, "sess-1", got.ID)
		assert.Equal(t, StatusActive, got.Status)
	})

	t.Run("GetHistory", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-a/fn-a/sessions/sess-1/history", tok))
		require.Equal(t, http.StatusOK, w.Code)
		body := decodeJSON[historyResponse](t, w)
		assert.EqualValues(t, 1, body.Head)
		require.Len(t, body.Events, 1)
		assert.Equal(t, "turn.start", body.Events[0].Type)
	})

	t.Run("GetCheckpoint", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-a/fn-a/sessions/sess-1/checkpoint", tok))
		// No checkpoint was written for this session: 404, not 403 — the
		// token was admitted through authz.Middleware and reached the
		// handler, it just found nothing.
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// --- GET /registry/agents/{namespace}/{name}/sessions/{id} ---

func TestGetSessionFound(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, store, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	rec := SessionRecord{ID: "sess-1", Agent: "fn", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeJSON[SessionRecord](t, w)
	assert.Equal(t, "sess-1", got.ID)
	assert.Equal(t, StatusActive, got.Status)
}

func TestGetSessionNotFound(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/no-such-session", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetSessionInvalidIDIs400(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/bad%24id%23", ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetSessionNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _, _, _ := newTestRegistryMux(t, authz)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/no-such-fn/sessions/sess-1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestGetSessionMiddlewareWrongNamespaceForbidden mirrors
// TestListSessionsMiddlewareWrongNamespaceForbidden for the single-session
// route: authz.Middleware must reject the request before GetSession's own
// view/store lookups ever run.
func TestGetSessionMiddlewareWrongNamespaceForbidden(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, store, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))
	view.Upsert(registryTestEntry("ns-b", "fn-b", 0))
	rec := SessionRecord{ID: "sess-1", Agent: "fn-b", Namespace: "ns-b", Status: StatusActive}
	require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-b/fn-b/sessions/sess-1", tok))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// --- GET /registry/agents/{namespace}/{name}/sessions/{id}/history ---

func TestGetHistoryRoundtrip(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, el := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	stream := stateapi.StreamName("ns", "myks", HistoryClientStream("sess-1"))
	_, err := el.Append(t.Context(), stream, statestore.AppendAny, []statestore.Event{
		{Type: "turn.start", Payload: []byte(`{"n":1}`)},
		{Type: "turn.end", Payload: []byte(`{"n":2}`)},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history", ""))
	require.Equal(t, http.StatusOK, w.Code)

	body := decodeJSON[historyResponse](t, w)
	assert.EqualValues(t, 2, body.Head)
	require.Len(t, body.Events, 2)
	assert.EqualValues(t, 1, body.Events[0].Seq)
	assert.Equal(t, "turn.start", body.Events[0].Type)
	assert.Equal(t, []byte(`{"n":1}`), body.Events[0].Payload)
	assert.False(t, body.Events[0].At.IsZero())
	assert.EqualValues(t, 2, body.Events[1].Seq)
	assert.Equal(t, "turn.end", body.Events[1].Type)

	// Pin the raw wire keys literally: statestore.Event carries no JSON tags
	// of its own (see historyEvent's doc comment), so decoding through
	// historyResponse's own tags cannot catch a tag typo — it would
	// round-trip through the same wrong name.
	raw := decodeJSON[map[string]jsontext.Value](t, w)
	require.Contains(t, raw, "head")
	require.Contains(t, raw, "events")
	var eventRows []map[string]jsontext.Value
	require.NoError(t, json.Unmarshal(raw["events"], &eventRows))
	require.Len(t, eventRows, 2)
	for _, key := range []string{"seq", "type", "payload", "at"} {
		assert.Contains(t, eventRows[0], key)
	}
}

func TestGetHistoryFromSeqExcludesSeen(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, el := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	stream := stateapi.StreamName("ns", "myks", HistoryClientStream("sess-1"))
	_, err := el.Append(t.Context(), stream, statestore.AppendAny, []statestore.Event{
		{Type: "turn.start"}, {Type: "turn.end"},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history?fromSeq=1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeJSON[historyResponse](t, w)
	assert.EqualValues(t, 2, body.Head, "head reports the stream's true tail regardless of fromSeq")
	require.Len(t, body.Events, 1)
	assert.Equal(t, "turn.end", body.Events[0].Type)
}

// TestGetHistoryEmptyKeyspaceIsGracefulEmpty pins the contract's hard
// requirement: an agent with no state keyspace answers 200 with an empty
// history, never an error, and never touches the store (Head/Read are not
// called at all — there is nothing seeded here for them to find, so a bug
// that skipped the early-return and called through to the store with an
// empty keyspace would show up as a 500 or a store-level panic, not a wrong
// count).
func TestGetHistoryEmptyKeyspaceIsGracefulEmpty(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0)) // StateKeyspace left empty.

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history", ""))
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeJSON[historyResponse](t, w)
	assert.EqualValues(t, 0, body.Head)
	assert.Empty(t, body.Events)

	raw := decodeJSON[map[string]jsontext.Value](t, w)
	var events []jsontext.Value
	require.NoError(t, json.Unmarshal(raw["events"], &events))
	assert.Empty(t, events, "events must be [], not null")
}

func TestGetHistoryLimitClamped(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, el := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	stream := stateapi.StreamName("ns", "myks", HistoryClientStream("sess-1"))
	_, err := el.Append(t.Context(), stream, statestore.AppendAny, []statestore.Event{
		{Type: "e1"}, {Type: "e2"}, {Type: "e3"},
	})
	require.NoError(t, err)

	t.Run("non-integer limit is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history?limit=abc", ""))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("non-integer fromSeq is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history?fromSeq=abc", ""))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("limit above cap is clamped and still returns everything within it", func(t *testing.T) {
		// 3 seeded events is well within the 500 cap, so a clamp bug that
		// silently drops the request would show up here as a wrong count or
		// an error, not just a 200 — mirrors
		// TestListSessionsDefaultAndCapLimit's rationale.
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/history?limit=9999", ""))
		require.Equal(t, http.StatusOK, w.Code)
		body := decodeJSON[historyResponse](t, w)
		assert.Len(t, body.Events, 3)
	})
}

func TestGetHistoryInvalidIDIs400(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/bad%24id%23/history", ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetHistoryNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _, _, _ := newTestRegistryMux(t, authz)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/no-such-fn/sessions/sess-1/history", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetHistoryMiddlewareWrongNamespaceForbidden(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns-b", "fn-b", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-b/fn-b/sessions/sess-1/history", tok))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// --- GET /registry/agents/{namespace}/{name}/sessions/{id}/checkpoint ---

func TestGetCheckpointFound(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, hist, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	ts := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	rec := CheckpointRecord{
		V:                 1,
		CoveredThroughSeq: 42,
		Kind:              "summary",
		Payload:           jsontext.Value(`{"summary":"so far"}`),
		CreatedAt:         ts,
		Turn:              7,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, hist.kv.Set(t.Context(), stateScope("ns", "myks"), CheckpointKeyPrefix+"sess-1", data, statestore.SetOptions{}))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/checkpoint", ""))
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeJSON[CheckpointRecord](t, w)
	assert.Equal(t, rec.V, got.V)
	assert.Equal(t, rec.CoveredThroughSeq, got.CoveredThroughSeq)
	assert.Equal(t, rec.Kind, got.Kind)
	assert.Equal(t, string(rec.Payload), string(got.Payload))
	assert.True(t, ts.Equal(got.CreatedAt))
	assert.Equal(t, rec.Turn, got.Turn)
}

func TestGetCheckpointNotFound(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/checkpoint", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestGetCheckpointEmptyKeyspaceIs404 pins that an agent with no state
// keyspace answers 404 (same as "no checkpoint written yet") rather than
// erroring — GetCheckpoint checks StateKeyspace before ever calling into
// HistoryStore, exactly like GetHistory's graceful-empty branch.
func TestGetCheckpointEmptyKeyspaceIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0)) // StateKeyspace left empty.

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/sess-1/checkpoint", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetCheckpointInvalidIDIs400(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns", "fn", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/bad%24id%23/checkpoint", ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetCheckpointNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _, _, _ := newTestRegistryMux(t, authz)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/no-such-fn/sessions/sess-1/checkpoint", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetCheckpointMiddlewareWrongNamespaceForbidden(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _, _, _ := newTestRegistryMux(t, authz)
	entry := registryTestEntry("ns-b", "fn-b", 0)
	entry.StateKeyspace = "myks"
	view.Upsert(entry)

	tok := mintToken(t, key, jwt.MapClaims{
		"sub":                "caller-1",
		"allowed_namespaces": []any{"ns-a"},
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns-b/fn-b/sessions/sess-1/checkpoint", tok))
	assert.Equal(t, http.StatusForbidden, w.Code)
}
