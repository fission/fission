// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// workspace_test.go drives the session workspace routes through the REAL
// production mux wiring (mountWorkspaceRoutes, the exact function main.go
// calls) against a fake storagesvc upstream wrapped in the REAL
// hmacauth.ServiceVerifier — so "the request is signed" means the signature
// actually verifies, not merely that some header is present — and a REAL
// http.ServeMux for path-pattern matching, so the trailing-slash/encoded-
// segment/list-vs-file behavior these tests pin depends on Go's actual
// pattern-matching semantics rather than a hand-set r.SetPathValue shortcut.
package agentruntime

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// fakeStoragesvc is a minimal in-memory stand-in for pkg/storagesvc's
// /v1/workspace surface, wired behind the REAL hmacauth.ServiceVerifier so a
// test asserting "the request is signed" is asserting a verified signature,
// not merely a present header.
type fakeStoragesvc struct {
	mu    sync.Mutex
	objs  map[string][]byte
	calls int

	// failList, when set, makes GET /v1/workspace/list answer 500 — for
	// TestWorkspacePut_ProbeFailureTreatedAsCreate's probe-failure pin.
	failList atomic.Bool
	// listPadBytes, when nonzero, pads the LIST response with an extra JSON
	// field of that many bytes — for TestWorkspaceClientList_BoundedRead,
	// which needs a response that legitimately exceeds
	// workspaceListMaxResponseBytes without needing that many real objects.
	listPadBytes atomic.Int64
}

func newFakeStoragesvc(t *testing.T, master []byte) (*httptest.Server, *fakeStoragesvc) {
	t.Helper()
	f := &fakeStoragesvc{objs: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		id := r.URL.Query().Get("id")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.objs[id] = body
		f.mu.Unlock()
		resp, _ := json.Marshal(map[string]int64{"size": int64(len(body))})
		_, _ = w.Write(resp)
	})
	mux.HandleFunc("GET /v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		body, ok := f.objs[r.URL.Query().Get("id")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("DELETE /v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		delete(f.objs, r.URL.Query().Get("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/workspace/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		if f.failList.Load() {
			f.mu.Unlock()
			http.Error(w, "simulated list failure", http.StatusInternalServerError)
			return
		}
		prefix := r.URL.Query().Get("prefix")
		type item struct {
			ID   string `json:"id"`
			Size int64  `json:"size"`
		}
		type listResp struct {
			Items []item `json:"items"`
			// Pad is TestWorkspaceClientList_BoundedRead's oversized-response
			// simulator (see listPadBytes) — never set outside that test.
			Pad string `json:"_pad,omitempty"`
		}
		var items []item
		for id, body := range f.objs {
			if strings.HasPrefix(id, prefix) {
				items = append(items, item{ID: id, Size: int64(len(body))})
			}
		}
		pad := f.listPadBytes.Load()
		f.mu.Unlock()
		out := listResp{Items: items}
		if pad > 0 {
			out.Pad = strings.Repeat("p", int(pad))
		}
		resp, _ := json.Marshal(out)
		_, _ = w.Write(resp)
	})

	verified := hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceStoragesvc, hmacauth.VerifierOpts{})(mux)
	srv := httptest.NewServer(verified)
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeStoragesvc) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeStoragesvc) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objs[id]
	return ok
}

// newTestWorkspaceHandler returns a WorkspaceHandler wired to a fresh
// in-memory SessionStore/AgentView. storagesvcURL == "" gives a nil client
// (the disabled-feature case); otherwise it signs with master over
// hmacauth.ServiceSigner exactly as main.go's Start does.
func newTestWorkspaceHandler(t *testing.T, master []byte, storagesvcURL string, maxArtifactBytes, maxSessionArtifactBytes int64) (*WorkspaceHandler, *AgentView, *SessionStore) {
	t.Helper()
	return newTestWorkspaceHandlerWithCountBudget(t, master, storagesvcURL, maxArtifactBytes, maxSessionArtifactBytes, 0)
}

// newTestWorkspaceHandlerWithCountBudget is newTestWorkspaceHandler with an
// explicit maxSessionArtifacts (the count budget), for tests exercising it
// directly.
func newTestWorkspaceHandlerWithCountBudget(t *testing.T, master []byte, storagesvcURL string, maxArtifactBytes, maxSessionArtifactBytes, maxSessionArtifacts int64) (*WorkspaceHandler, *AgentView, *SessionStore) {
	t.Helper()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	view := NewAgentView()
	var client *workspaceClient
	if storagesvcURL != "" {
		rt := hmacauth.ServiceSigner(master, hmacauth.ServiceStoragesvc, http.DefaultTransport, time.Now)
		client = newWorkspaceClient(storagesvcURL, rt)
	}
	wh := NewWorkspaceHandler(logr.Discard(), view, store, client, time.Hour, maxArtifactBytes, maxSessionArtifactBytes, maxSessionArtifacts)
	return wh, view, store
}

// passthroughAuth is a no-op middleware used for the routing/validation
// tests below that are not exercising the auth layer at all (identity_test.go
// and the composition tests in this file already cover auth).
func passthroughAuth(h http.Handler) http.Handler { return h }

// identityWorkspaceRequest builds a request against the workspace mux
// carrying identity claim headers, mirroring newIdentityRequest
// (identity_test.go) for the workspace path shape.
func identityWorkspaceRequest(method, path, claimedNS, claimedAgent, bearer string, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if claimedNS != "" {
		r.Header.Set(HeaderIdentityNamespace, claimedNS)
	}
	if claimedAgent != "" {
		r.Header.Set(HeaderIdentityName, claimedAgent)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func newActiveSessionRecord(ns, agent, id string, now time.Time) SessionRecord {
	return SessionRecord{ID: id, Agent: agent, Namespace: ns, Status: StatusActive, CreatedAt: now, LastActiveAt: now}
}

// TestWorkspaceRoutes_IdentityHappyPath drives PUT -> GET -> LIST -> DELETE
// through the real mux with a valid G16 identity bearer, asserting the fake
// storagesvc actually received the signed, translated ?id= at each step.
func TestWorkspaceRoutes_IdentityHappyPath(t *testing.T) {
	t.Parallel()
	master := []byte("ws-happy-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))

	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")
	const artifact = "hello artifact bytes"

	putReq := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/notes/todo.txt", "ns-a", "agent-x", tok, artifact)
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, putReq)
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())
	var putResp workspacePutResp
	require.NoError(t, json.Unmarshal(putRec.Body.Bytes(), &putResp))
	assert.Equal(t, "notes/todo.txt", putResp.Path)
	assert.EqualValues(t, len(artifact), putResp.Size)
	assert.True(t, fake.has("_workspace_/ns-a/agent-x/sess-1/notes/todo.txt"), "the fake must have received the translated ?id=")

	getReq := identityWorkspaceRequest(http.MethodGet, "/workspace/ns-a/agent-x/sess-1/notes/todo.txt", "ns-a", "agent-x", tok, "")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Equal(t, artifact, getRec.Body.String())
	assert.Equal(t, strconv.Itoa(len(artifact)), getRec.Header().Get("Content-Length"))

	listReq := identityWorkspaceRequest(http.MethodGet, "/workspace/ns-a/agent-x/sess-1", "ns-a", "agent-x", tok, "")
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	var listResp workspaceListResp
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listResp))
	require.Len(t, listResp.Items, 1)
	assert.Equal(t, "notes/todo.txt", listResp.Items[0].Path)
	assert.EqualValues(t, len(artifact), listResp.Items[0].Size)

	delReq := identityWorkspaceRequest(http.MethodDelete, "/workspace/ns-a/agent-x/sess-1/notes/todo.txt", "ns-a", "agent-x", tok, "")
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code)
	assert.False(t, fake.has("_workspace_/ns-a/agent-x/sess-1/notes/todo.txt"))
}

// TestWorkspaceRoutes_CrossAgentForbidden is the G16 surface amendment's own
// test: a valid identity bearer for (ns-a, agent-x) must not reach agent-y's
// workspace in the SAME namespace — stricter than dispatch's cross-agent
// spawn allowance (identity_test.go's
// TestIdentityOrJWT_SameNamespaceCrossAgentAllowed).
func TestWorkspaceRoutes_CrossAgentForbidden(t *testing.T) {
	t.Parallel()
	master := []byte("ws-crossagent-master")
	wh, view, _ := newTestWorkspaceHandler(t, master, "", 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	view.Upsert(headerEntry("ns-a", "agent-y", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))

	tok := identityToken(master, "ns-a", "agent-x")
	req := identityWorkspaceRequest(http.MethodGet, "/workspace/ns-a/agent-y/sess-1", "ns-a", "agent-x", tok, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestWorkspaceRoutes_CrossNamespaceForbidden mirrors dispatch's own
// cross-namespace pin (identity_test.go's
// TestIdentityOrJWT_CrossNamespaceForbidden) on the workspace surface.
func TestWorkspaceRoutes_CrossNamespaceForbidden(t *testing.T) {
	t.Parallel()
	master := []byte("ws-crossns-master")
	wh, view, _ := newTestWorkspaceHandler(t, master, "", 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))

	tok := identityToken(master, "ns-a", "agent-x")
	req := identityWorkspaceRequest(http.MethodGet, "/workspace/ns-b/agent-x/sess-1", "ns-a", "agent-x", tok, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestWorkspaceRoutes_JWTScopeDebuggingParity: a namespace-scoped JWT (no
// identity headers) may reach ANY agent's workspace within its scoped
// namespace — deliberately LESS strict than the identity path's
// own-workspace-only rule (debugging parity, per the plan).
func TestWorkspaceRoutes_JWTScopeDebuggingParity(t *testing.T) {
	t.Parallel()
	master := []byte("ws-jwt-master")
	key := []byte("ws-jwt-signing-key")
	upstream, _ := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)
	authz := NewAuthorizer(key)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, authz.Middleware))

	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))

	scopedTok := mintToken(t, key, jwt.MapClaims{
		"sub": "debug-operator", "allowed_namespaces": []any{"ns-a"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	t.Run("scoped JWT reaches an agent's workspace with no identity claim at all", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/workspace/ns-a/agent-x/sess-1", nil)
		req.Header.Set("Authorization", "Bearer "+scopedTok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("out-of-scope namespace is still forbidden via JWT", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/workspace/ns-b/agent-x/sess-1", nil)
		req.Header.Set("Authorization", "Bearer "+scopedTok)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

// TestWorkspaceRoutes_NoSessionRecord404_NoUpstreamCalls is the GC-anchor
// pin: a session id with no live record 404s BEFORE any storagesvc call —
// the counting fake proves zero upstream hits.
func TestWorkspaceRoutes_NoSessionRecord404_NoUpstreamCalls(t *testing.T) {
	t.Parallel()
	master := []byte("ws-404-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, view, _ := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))

	tok := identityToken(master, "ns-a", "agent-x")
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"GET file", http.MethodGet, "/workspace/ns-a/agent-x/no-such-session/foo.txt"},
		{"PUT file", http.MethodPut, "/workspace/ns-a/agent-x/no-such-session/foo.txt"},
		{"DELETE file", http.MethodDelete, "/workspace/ns-a/agent-x/no-such-session/foo.txt"},
		{"LIST", http.MethodGet, "/workspace/ns-a/agent-x/no-such-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := identityWorkspaceRequest(tc.method, tc.path, "ns-a", "agent-x", tok, "")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
	assert.Zero(t, fake.callCount(), "a 404 on the existence anchor must never reach storagesvc")
}

// TestWorkspacePut_OverCap413_NothingStored is the over-read-detection pin:
// LimitReader(cap+1) rejects a body over AGENT_MAX_ARTIFACT_BYTES with 413
// and ZERO upstream calls — never silently stores a truncated artifact.
func TestWorkspacePut_OverCap413_NothingStored(t *testing.T) {
	t.Parallel()
	master := []byte("ws-413-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 10, 0) // 10-byte cap
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))

	tok := identityToken(master, "ns-a", "agent-x")
	req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/big.txt", "ns-a", "agent-x", tok, strings.Repeat("x", 25))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Zero(t, fake.callCount(), "an over-cap PUT must never reach storagesvc")

	rec2, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.Zero(t, rec2.Stats.ArtifactCount, "a rejected PUT must not bump the meter")
}

// TestWorkspacePut_OverBudget429 is the quota pre-check pin: a session
// already near AGENT_MAX_SESSION_ARTIFACT_BYTES rejects a PUT that would
// push it over, BEFORE the storagesvc WRITE. It costs exactly ONE upstream
// call — the probeExisting LIST that delta-accounting needs to tell an
// overwrite from a create (M1's fix) — never the PUT itself.
func TestWorkspacePut_OverBudget429(t *testing.T) {
	t.Parallel()
	master := []byte("ws-429-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 100) // 100-byte session budget
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))

	rec := newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now())
	rec.Stats.ArtifactBytes = 90
	require.NoError(t, store.Create(t.Context(), rec, 0, time.Hour))

	tok := identityToken(master, "ns-a", "agent-x")
	req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/more.txt", "ns-a", "agent-x", tok, strings.Repeat("y", 20))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.EqualValues(t, 1, fake.callCount(), "an over-budget PUT must reach storagesvc only for the delta-accounting probe (LIST), never the PUT itself")
	assert.False(t, fake.has("_workspace_/ns-a/agent-x/sess-1/more.txt"), "nothing must be stored on a 429")
}

// TestWorkspacePut_OverwriteDeltaAccounting is finding M1's core pin:
// rewriting the SAME path multiple times (with different sizes) must settle
// ArtifactBytes to the CURRENT stored size, not the cumulative sum of every
// write, and ArtifactCount must stay at 1 — never re-incrementing for a path
// that already existed. A budget sized to fit only ONE of the larger bodies
// (not N of them) proves the settle is delta-based, not additive: a naive
// additive implementation would 429 on the second or third rewrite.
func TestWorkspacePut_OverwriteDeltaAccounting(t *testing.T) {
	t.Parallel()
	master := []byte("ws-delta-master")
	upstream, fake := newFakeStoragesvc(t, master)
	// Budget fits exactly one 40-byte body plus slack, never two.
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 50)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	sizes := []int{10, 40, 20, 40} // grows, shrinks, grows again — all under budget only if delta-settled
	for i, sz := range sizes {
		body := strings.Repeat("z", sz)
		req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/same.txt", "ns-a", "agent-x", tok, body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equalf(t, http.StatusOK, w.Code, "rewrite #%d (size %d) must succeed under a budget sized for ONE body, not the cumulative sum: %s", i, sz, w.Body.String())

		rec, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
		require.NoError(t, err)
		assert.EqualValuesf(t, sz, rec.Stats.ArtifactBytes, "after rewrite #%d, ArtifactBytes must equal the CURRENT stored size, not a cumulative sum", i)
		assert.EqualValuesf(t, 1, rec.Stats.ArtifactCount, "rewrite #%d must not re-increment ArtifactCount for a path that already existed", i)
	}
	assert.True(t, fake.has("_workspace_/ns-a/agent-x/sess-1/same.txt"))

	// DELETE must restore the budget to exactly 0, not leave phantom residue
	// from the (now-irrelevant) earlier write sizes.
	delReq := identityWorkspaceRequest(http.MethodDelete, "/workspace/ns-a/agent-x/sess-1/same.txt", "ns-a", "agent-x", tok, "")
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code)

	rec, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.Zero(t, rec.Stats.ArtifactBytes, "delete must restore the byte budget fully")
	assert.Zero(t, rec.Stats.ArtifactCount)
}

// TestWorkspacePut_ProbeFailureTreatedAsCreate is M1's accepted-drift pin:
// when probeExisting's LIST call fails, handlePut must treat the path as
// not-yet-existing (oldSize=0, count bumped) rather than blocking the write
// — a probe outage degrades bookkeeping accuracy, never availability.
func TestWorkspacePut_ProbeFailureTreatedAsCreate(t *testing.T) {
	t.Parallel()
	master := []byte("ws-probefail-master")
	upstream, fake := newFakeStoragesvc(t, master)
	fake.failList.Store(true)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	const artifact = "twelve bytes"
	req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/a.txt", "ns-a", "agent-x", tok, artifact)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "a probe failure must not block the PUT")

	rec, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, rec.Stats.ArtifactCount, "probe failure must fall back to treating the path as new")
	assert.EqualValues(t, len(artifact), rec.Stats.ArtifactBytes)
}

// TestWorkspacePut_CountBudget429 is finding M2(a)/(c)'s pin: a session at
// its AGENT_MAX_SESSION_ARTIFACTS ceiling rejects a NEW-path PUT even when
// the body is 0 bytes (closing the bytes-budget bypass), while a rewrite of
// an ALREADY-EXISTING path (which does not consume a new count slot) still
// succeeds.
func TestWorkspacePut_CountBudget429(t *testing.T) {
	t.Parallel()
	master := []byte("ws-countbudget-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandlerWithCountBudget(t, master, upstream.URL, 1<<20, 0, 1) // count budget of 1
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	firstReq := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/first.txt", "ns-a", "agent-x", tok, "")
	firstRec := httptest.NewRecorder()
	mux.ServeHTTP(firstRec, firstReq)
	require.Equal(t, http.StatusOK, firstRec.Code, "the first (zero-byte) artifact must consume the one available count slot")

	t.Run("a second distinct zero-byte path is rejected — the count budget, not the byte budget, catches it", func(t *testing.T) {
		secondReq := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/second.txt", "ns-a", "agent-x", tok, "")
		secondRec := httptest.NewRecorder()
		mux.ServeHTTP(secondRec, secondReq)
		assert.Equal(t, http.StatusTooManyRequests, secondRec.Code)
		assert.False(t, fake.has("_workspace_/ns-a/agent-x/sess-1/second.txt"))
	})

	t.Run("rewriting the SAME existing path is still allowed — it consumes no new count slot", func(t *testing.T) {
		rewriteReq := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/first.txt", "ns-a", "agent-x", tok, "updated")
		rewriteRec := httptest.NewRecorder()
		mux.ServeHTTP(rewriteRec, rewriteReq)
		assert.Equal(t, http.StatusOK, rewriteRec.Code)
	})

	rec, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, rec.Stats.ArtifactCount)
}

// TestWorkspacePathValidation_400 exercises the four path-traversal/encoding
// pins through the REAL ServeMux (auth deliberately bypassed via
// passthroughAuth — these are pure routing/validation assertions).
func TestWorkspacePathValidation_400(t *testing.T) {
	t.Parallel()
	master := []byte("ws-400-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, _, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, passthroughAuth)

	tests := []struct {
		name string
		path string
	}{
		{"encoded traversal segment (%2E%2E decoded by ServeMux before PathValue)", "/workspace/ns-a/agent-x/sess-1/%2E%2E%2Fetc%2Fpasswd"},
		{"trailing slash yields an empty {path...} on the file route", "/workspace/ns-a/agent-x/sess-1/"},
		{"leading underscore path segment collides with the reserved marker", "/workspace/ns-a/agent-x/sess-1/_secret"},
		{"empty inner segment via doubled encoded slash", "/workspace/ns-a/agent-x/sess-1/foo%2F%2Fbar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader("x"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		})
	}
	assert.Zero(t, fake.callCount(), "every case here must be rejected before any storagesvc call")
}

// TestWorkspaceSessionValidation_400 is finding 4's pin: sessionIDPattern
// alone admits ".", "..", and a leading "_" as a session id (its charset is
// permissive), and only storagesvc's own validateWorkspaceID rejects those
// as a path segment — surfacing as an opaque 502 rather than this layer's
// own 400, contradicting the "mirrors storagesvc's rules exactly" defense-
// in-depth claim. validateWorkspaceRouteIdentity now additionally runs
// validateWorkspaceSegment(session), closing the gap: these ids must 400
// here, before any storagesvc call.
func TestWorkspaceSessionValidation_400(t *testing.T) {
	t.Parallel()
	master := []byte("ws-session400-master")
	upstream, fake := newFakeStoragesvc(t, master)
	wh, _, _ := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, passthroughAuth)

	tests := []struct {
		name    string
		session string
	}{
		// "." and ".." are given ENCODED (%2E): a literal "/workspace/.../."
		// or "/workspace/.../.." is redirected (307) by net/http's own path
		// cleaning before ever reaching the mux pattern match — the encoded
		// form is what actually exercises this layer's validation, exactly
		// like the {path...} traversal case in TestWorkspacePathValidation_400.
		{"single dot (encoded)", "%2E"},
		{"double dot (encoded)", "%2E%2E"},
		{"leading underscore", "_hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/workspace/ns-a/agent-x/"+tt.session, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		})
	}
	assert.Zero(t, fake.callCount(), "a malformed session id must never reach storagesvc")
}

// TestWorkspaceClientList_BoundedRead is finding M2(b)'s pin: a storagesvc
// LIST response larger than workspaceListMaxResponseBytes must error out
// (never be fully buffered) — the fake pads its response with an oversized
// filler field to simulate a hostile/corrupted upstream without needing
// thousands of real objects.
func TestWorkspaceClientList_BoundedRead(t *testing.T) {
	t.Parallel()
	master := []byte("ws-listbound-master")
	upstream, fake := newFakeStoragesvc(t, master)
	fake.listPadBytes.Store(workspaceListMaxResponseBytes + 1024)

	rt := hmacauth.ServiceSigner(master, hmacauth.ServiceStoragesvc, http.DefaultTransport, time.Now)
	client := newWorkspaceClient(upstream.URL, rt)

	_, err := client.list(t.Context(), "_workspace_/ns-a/agent-x/sess-1/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

// TestWorkspaceMeters_BumpAndDecrement is the settle-ordering pin: a
// successful PUT bumps Stats.ArtifactCount/ArtifactBytes, a successful
// DELETE decrements them, and a second DELETE of the same (now-gone) path
// leaves the counters clamped at 0 rather than going negative.
func TestWorkspaceMeters_BumpAndDecrement(t *testing.T) {
	t.Parallel()
	master := []byte("ws-meters-master")
	upstream, _ := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	const artifact = "twelve bytes"
	putReq := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/a.txt", "ns-a", "agent-x", tok, artifact)
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, putReq)
	require.Equal(t, http.StatusOK, putRec.Code)

	rec, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, rec.Stats.ArtifactCount)
	assert.EqualValues(t, len(artifact), rec.Stats.ArtifactBytes)

	delReq := identityWorkspaceRequest(http.MethodDelete, "/workspace/ns-a/agent-x/sess-1/a.txt", "ns-a", "agent-x", tok, "")
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code)

	rec, _, err = store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.Zero(t, rec.Stats.ArtifactCount)
	assert.Zero(t, rec.Stats.ArtifactBytes)

	// Second delete of the already-gone path: idempotent 200, and the
	// counters must stay clamped at 0, never go negative.
	delRec2 := httptest.NewRecorder()
	mux.ServeHTTP(delRec2, identityWorkspaceRequest(http.MethodDelete, "/workspace/ns-a/agent-x/sess-1/a.txt", "ns-a", "agent-x", tok, ""))
	assert.Equal(t, http.StatusOK, delRec2.Code)

	rec, _, err = store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	assert.Zero(t, rec.Stats.ArtifactCount)
	assert.Zero(t, rec.Stats.ArtifactBytes)
}

// TestWorkspacePut_SSEFeedUntouched is the sessionUnchanged pin: an artifact
// write settles Stats.ArtifactCount/ArtifactBytes but must not be
// SSE-worthy — sessionUnchanged (events.go) must still report the before/
// after records as unchanged even though those two fields differ.
func TestWorkspacePut_SSEFeedUntouched(t *testing.T) {
	t.Parallel()
	master := []byte("ws-sse-master")
	upstream, _ := newFakeStoragesvc(t, master)
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, 1<<20, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	before, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)

	req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/a.txt", "ns-a", "agent-x", tok, "some bytes")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	after, _, err := store.Get(t.Context(), "ns-a", "agent-x", "sess-1")
	require.NoError(t, err)
	require.NotEqualValues(t, before.Stats.ArtifactBytes, after.Stats.ArtifactBytes, "the PUT must have actually settled the counter")
	assert.True(t, sessionUnchanged(before, after), "an artifact write must not be SSE-diff-worthy")
}

// TestSpoolWorkspaceBody_FileBranchIndependentReaders is the load-bearing
// spool test: a body larger than threshold spills to a temp file, and
// reader() called TWICE must each return the FULL body independently — the
// invariant workspaceClient.put relies on (one reader for the outgoing
// req.Body, a second FRESH one from req.GetBody for hmacauth.Signer's
// streaming-hash pass). A shared *os.File handle rewound with Seek would
// pass a single-reader test but fail this one: the second reader would see
// whatever position the first read left behind.
func TestSpoolWorkspaceBody_FileBranchIndependentReaders(t *testing.T) {
	t.Parallel()
	const body = "0123456789" // 10 bytes
	sp, err := spoolWorkspaceBody(strings.NewReader(body), 4)
	require.NoError(t, err)
	t.Cleanup(sp.cleanup)

	require.NotEmpty(t, sp.fileName, "a body larger than threshold must spill to a temp file")
	assert.EqualValues(t, len(body), sp.size)

	r1, err := sp.reader()
	require.NoError(t, err)
	got1, err := io.ReadAll(r1)
	require.NoError(t, err)
	require.NoError(t, r1.Close())
	assert.Equal(t, body, string(got1), "first reader must see the full spooled body")

	r2, err := sp.reader()
	require.NoError(t, err)
	got2, err := io.ReadAll(r2)
	require.NoError(t, err)
	require.NoError(t, r2.Close())
	assert.Equal(t, body, string(got2), "a SECOND, independent reader must ALSO see the full body — a shared file handle would see EOF here instead")

	fileName := sp.fileName
	sp.cleanup()
	_, statErr := os.Stat(fileName)
	assert.True(t, os.IsNotExist(statErr), "cleanup must remove the spool temp file")
}

// TestSpoolWorkspaceBody_MemBranch covers the in-memory path: a body at or
// under threshold never touches disk.
func TestSpoolWorkspaceBody_MemBranch(t *testing.T) {
	t.Parallel()
	const body = "small"
	sp, err := spoolWorkspaceBody(strings.NewReader(body), int64(len(body)+10))
	require.NoError(t, err)
	t.Cleanup(sp.cleanup)

	assert.Empty(t, sp.fileName, "a body under threshold must stay in memory")
	assert.EqualValues(t, len(body), sp.size)

	r, err := sp.reader()
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

// TestWorkspacePut_OverThresholdViaRealSignerVerifier is the end-to-end
// proof that the spilled-to-disk branch round-trips through the REAL
// hmacauth.Signer/Verifier pair, not just spoolWorkspaceBody in isolation: a
// body larger than workspaceSpoolThresholdBytes must still produce a
// signature that verifies, and the bytes that arrive at storagesvc must
// match exactly.
func TestWorkspacePut_OverThresholdViaRealSignerVerifier(t *testing.T) {
	t.Parallel()
	master := []byte("ws-spill-master")
	upstream, fake := newFakeStoragesvc(t, master)
	artifactCap := workspaceSpoolThresholdBytes + 1024 // forces the file-spill branch
	wh, view, store := newTestWorkspaceHandler(t, master, upstream.URL, artifactCap, 0)
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, IdentityOrJWTOwnWorkspace(identity, NewAuthorizer(nil).Middleware))
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))
	tok := identityToken(master, "ns-a", "agent-x")

	artifact := strings.Repeat("A", int(workspaceSpoolThresholdBytes)+512)
	req := identityWorkspaceRequest(http.MethodPut, "/workspace/ns-a/agent-x/sess-1/big.bin", "ns-a", "agent-x", tok, artifact)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	getReq := identityWorkspaceRequest(http.MethodGet, "/workspace/ns-a/agent-x/sess-1/big.bin", "ns-a", "agent-x", tok, "")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	assert.Equal(t, len(artifact), getRec.Body.Len())
	assert.Equal(t, artifact, getRec.Body.String())
	assert.GreaterOrEqual(t, fake.callCount(), 2)
}

// TestWorkspaceRoutes_Disabled503 covers every route answering 503 when
// STORAGESVC_URL was unset at startup (client == nil) — agentruntime must
// never guess a storagesvc URL.
func TestWorkspaceRoutes_Disabled503(t *testing.T) {
	t.Parallel()
	wh, _, store := newTestWorkspaceHandler(t, nil, "", 1<<20, 0)
	require.NoError(t, store.Create(t.Context(), newActiveSessionRecord("ns-a", "agent-x", "sess-1", time.Now()), 0, time.Hour))

	mux := http.NewServeMux()
	mountWorkspaceRoutes(mux, wh, passthroughAuth)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPut, "/workspace/ns-a/agent-x/sess-1/a.txt"},
		{http.MethodGet, "/workspace/ns-a/agent-x/sess-1/a.txt"},
		{http.MethodDelete, "/workspace/ns-a/agent-x/sess-1/a.txt"},
		{http.MethodGet, "/workspace/ns-a/agent-x/sess-1"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("x"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Contains(t, rec.Body.String(), "STORAGESVC_URL")
		})
	}
}
