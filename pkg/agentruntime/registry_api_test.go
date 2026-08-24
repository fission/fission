// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
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
// manual per-entry scope filter, and the two {namespace}-carrying routes
// behind authz.Middleware.
func newTestRegistryMux(t *testing.T, authz *Authorizer) (http.Handler, *AgentView, *SessionStore) {
	t.Helper()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	api := NewRegistryAPI(view, store, authz)

	mux := http.NewServeMux()
	mux.Handle("GET /registry/agents", authz.HTTPMiddleware(http.HandlerFunc(api.ListAgents)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions", authz.Middleware(http.HandlerFunc(api.ListSessions)))
	mux.Handle("GET /registry/agents/{namespace}/{name}/sessions/{id}", authz.Middleware(http.HandlerFunc(api.GetSession)))
	return mux, view, store
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
	mux, view, _ := newTestRegistryMux(t, authz)
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
	// wrong name), so check the raw JSON keys the brief specifies.
	raw := decodeJSON[map[string]any](t, w)
	agents, ok := raw["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 1)
	entry, ok := agents[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"namespace", "name", "sessionSource", "sessionName", "idleAfter", "archiveAfter", "maxSessions"} {
		assert.Contains(t, entry, key)
	}
}

func TestListAgentsWildcardSeesAll(t *testing.T) {
	t.Parallel()
	key := []byte("registry-test-key")
	authz := NewAuthorizer(key)
	mux, view, _ := newTestRegistryMux(t, authz)
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
	mux, view, _ := newTestRegistryMux(t, authz)
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
	mux, view, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns-a", "fn-a", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents", ""))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- GET /registry/agents/{namespace}/{name}/sessions ---

func TestListSessionsPaginationWalksNextToken(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil) // auth disabled: focus this test on pagination.
	mux, view, store := newTestRegistryMux(t, authz)
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
	raw := decodeJSON[map[string]any](t, w)
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
	mux, view, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	t.Run("non-integer limit is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=abc", ""))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("limit above cap is clamped, not rejected", func(t *testing.T) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions?limit=999999", ""))
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestListSessionsNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _ := newTestRegistryMux(t, authz)

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
	mux, view, _ := newTestRegistryMux(t, authz)
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

// --- GET /registry/agents/{namespace}/{name}/sessions/{id} ---

func TestGetSessionFound(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, store := newTestRegistryMux(t, authz)
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
	mux, view, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/no-such-session", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetSessionInvalidIDIs400(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, view, _ := newTestRegistryMux(t, authz)
	view.Upsert(registryTestEntry("ns", "fn", 0))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/fn/sessions/bad%24id%23", ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetSessionNonAgentIs404(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil)
	mux, _, _ := newTestRegistryMux(t, authz)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, bearerRequest(http.MethodGet, "/registry/agents/ns/no-such-fn/sessions/sess-1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}
