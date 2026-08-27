// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// newWorkspaceTestServer builds the workspace + archive routes on a mux
// wrapped with the real namespace-scoped HMAC verifier (mirroring
// TestArchiveAuthzHandlers), with a small, test-controlled spoolThreshold so
// callers can exercise both the in-memory and spooled verifier body paths
// without writing multi-megabyte payloads.
func newWorkspaceTestServer(t *testing.T, spoolThreshold int64) (r http.Handler, sc *StorageClient, storage Storage, master []byte, now time.Time) {
	t.Helper()
	master = []byte("storagesvc-test-master-key-long-enough")
	now = time.Unix(1715000123, 0)

	storage = NewLocalStorage(t.TempDir())
	sc, err := MakeStorageClient(logr.Discard(), storage)
	require.NoError(t, err)
	ss := MakeStorageService(logr.Discard(), sc, master, nil, 0)

	m := httpmux.New(httpmux.WithMiddleware(hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc,
		hmacauth.VerifierOpts{SkewSec: 60, Now: func() time.Time { return now }, SpoolThresholdBytes: spoolThreshold})))
	m.HandleFunc("/v1/archive", ss.getOrListHandler).Methods(http.MethodGet)
	m.HandleFunc("/v1/archive", ss.deleteHandler).Methods(http.MethodDelete)
	m.HandleFunc("/v1/archive", ss.infoHandler).Methods(http.MethodHead)
	m.HandleFunc("/v1/workspace", ss.workspacePutHandler).Methods(http.MethodPut)
	m.HandleFunc("/v1/workspace", ss.workspaceGetHandler).Methods(http.MethodGet)
	m.HandleFunc("/v1/workspace", ss.workspaceDeleteHandler).Methods(http.MethodDelete)
	m.HandleFunc("/v1/workspace/list", ss.workspaceListHandler).Methods(http.MethodGet)
	m.HandleFunc("/v1/workspace/prefix", ss.workspaceDeletePrefixHandler).Methods(http.MethodDelete)
	return m.Handler(), sc, storage, master, now
}

// signedWorkspaceRequest builds a raw-body request signed with key, optionally
// carrying the X-Fission-Auth-Namespace header (nsHeader == "" omits it).
func signedWorkspaceRequest(t *testing.T, method, target string, body []byte, key []byte, ts int64, nsHeader string) *http.Request {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reqBody)
	sig := hmacauth.Sign(key, method, req.URL.RequestURI(), body, ts)
	req.Header.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(hmacauth.HeaderSignature, sig)
	if nsHeader != "" {
		req.Header.Set(hmacauth.HeaderNamespace, nsHeader)
	}
	return req
}

// TestValidateWorkspaceID covers the per-segment validation table: dot/dotdot,
// empty, leading underscore, overlong segment/total, bad charset, missing
// marker, too few segments, and the crafted traversal id from the plan
// ("_workspace_/ns/agent/sess/../../../_tenant_/x") that must be rejected
// outright (a literal ".." segment) rather than resolved via path.Clean.
func TestValidateWorkspaceID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid minimal (no path)", "_workspace_/team-a/agentX/sess1", false},
		{"valid with nested path", "_workspace_/team-a/agentX/sess1/dir/artifact.txt", false},
		{"missing marker", "team-a/agentX/sess1/artifact.txt", true},
		{"too few segments", "_workspace_/team-a/agentX", true},
		{"dot segment", "_workspace_/team-a/./sess1/x", true},
		{"dotdot segment", "_workspace_/team-a/agentX/../sess1/x", true},
		{"empty segment", "_workspace_/team-a//sess1/x", true},
		{"leading underscore segment", "_workspace_/team-a/agentX/_sess1/x", true},
		{"bad charset", "_workspace_/team-a/agentX/sess1/art@fact.txt", true},
		{"overlong segment", "_workspace_/" + strings.Repeat("a", 129) + "/agentX/sess1/x", true},
		{"overlong total", "_workspace_/team-a/agentX/sess1/" + strings.Repeat("a", 1100), true},
		{"crafted traversal escaping the marker", "_workspace_/ns/agent/sess/../../../_tenant_/x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkspaceID(tc.id)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateWorkspacePrefix(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"valid with trailing slash", "_workspace_/team-a/agentX/sess1/", false},
		{"valid without trailing slash", "_workspace_/team-a/agentX/sess1", false},
		{"valid deeper than session", "_workspace_/team-a/agentX/sess1/nested/", false},
		{"missing marker", "team-a/agentX/sess1/", true},
		{"too few segments (agent-wide)", "_workspace_/team-a/agentX/", true},
		{"too few segments (namespace-wide)", "_workspace_/team-a/", true},
		{"dotdot segment", "_workspace_/team-a/agentX/../sess1/", true},
		{"overlong", "_workspace_/team-a/agentX/" + strings.Repeat("a", 1100), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkspacePrefix(tc.prefix)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCanonicalWorkspacePrefix(t *testing.T) {
	assert.Equal(t, "_workspace_/ns/agent/sess1/", canonicalWorkspacePrefix("_workspace_/ns/agent/sess1"))
	assert.Equal(t, "_workspace_/ns/agent/sess1/", canonicalWorkspacePrefix("_workspace_/ns/agent/sess1/"))
}

// TestIsWorkspaceID locks the segment-based detection used to seal the
// archive routes and the pruner candidate list: it must find the marker
// regardless of what backend-specific prefix precedes it (a local absolute
// container path, an S3 STORAGE_S3_SUB_DIR prefix) and regardless of a
// traversal that resolves back onto the marker after path.Clean.
func TestIsWorkspaceID(t *testing.T) {
	assert.True(t, isWorkspaceID("_workspace_/ns/agent/sess/x"))
	assert.True(t, isWorkspaceID("/data/fission-functions/_workspace_/ns/agent/sess/x"), "local absolute container path")
	assert.True(t, isWorkspaceID("sub/dir/_workspace_/ns/agent/sess/x"), "s3 subDir-prefixed key")
	assert.True(t, isWorkspaceID("_workspace_/ns/agent/sess/../../../_tenant_/x"), "resolves onto the marker after cleaning")
	assert.False(t, isWorkspaceID("550e8400-e29b-41d4-a716-446655440000"))
	assert.False(t, isWorkspaceID(archiveTenantMarker+"/team-a/550e8400-e29b-41d4-a716-446655440000"))
}

func TestNormalizeWorkspaceID(t *testing.T) {
	id, ok := normalizeWorkspaceID("/data/fission-functions/_workspace_/ns/agent/sess/x")
	require.True(t, ok)
	assert.Equal(t, "_workspace_/ns/agent/sess/x", id)

	id, ok = normalizeWorkspaceID("_workspace_/ns/agent/sess/x")
	require.True(t, ok)
	assert.Equal(t, "_workspace_/ns/agent/sess/x", id)

	_, ok = normalizeWorkspaceID("550e8400-e29b-41d4-a716-446655440000")
	assert.False(t, ok)
}

// TestWorkspacePutGetRoundTrip exercises a byte round-trip for a small
// (in-memory verifier path) and a >spool-threshold (spooled verifier path)
// body, checking both the PUT response's reported size and the GET's
// Content-Length + body bytes.
func TestWorkspacePutGetRoundTrip(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 16) // small threshold to force spooling for the large case
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	id := "_workspace_/team-a/agentX/sess1/artifact.txt"
	target := "/v1/workspace?id=" + url.QueryEscape(id)

	cases := []struct {
		name    string
		payload []byte
	}{
		{"small in-memory body", []byte("hello")},
		{"large spooled body", bytes.Repeat([]byte("x"), 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putReq := signedWorkspaceRequest(t, http.MethodPut, target, tc.payload, keyMaster, now.Unix(), "")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, putReq)
			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			var putResp workspacePutResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &putResp))
			assert.Equal(t, int64(len(tc.payload)), putResp.Size)

			getReq := signedWorkspaceRequest(t, http.MethodGet, target, nil, keyMaster, now.Unix(), "")
			rr2 := httptest.NewRecorder()
			r.ServeHTTP(rr2, getReq)
			require.Equal(t, http.StatusOK, rr2.Code)
			assert.Equal(t, tc.payload, rr2.Body.Bytes())
			// GET streams chunked (no pre-stat, no Content-Length) so a
			// concurrent overwrite can never make a declared length disagree
			// with the bytes actually streamed — see workspaceGetHandler.
			assert.Empty(t, rr2.Header().Get("Content-Length"))
		})
	}
}

func TestWorkspaceGetMissingIsNotFound(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	target := "/v1/workspace?id=" + url.QueryEscape("_workspace_/team-a/agentX/sess1/never-put.txt")
	req := signedWorkspaceRequest(t, http.MethodGet, target, nil, keyMaster, now.Unix(), "")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestWorkspaceDeleteIdempotent verifies a second DELETE of an already-deleted
// (or never-existing) object still returns 200 — the purge callers (a later
// slice of this feature) may race or retry.
func TestWorkspaceDeleteIdempotent(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	target := "/v1/workspace?id=" + url.QueryEscape("_workspace_/team-a/agentX/sess1/gone.txt")

	put := signedWorkspaceRequest(t, http.MethodPut, target, []byte("bye"), keyMaster, now.Unix(), "")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, put)
	require.Equal(t, http.StatusOK, rr.Code)

	for i := range 2 {
		del := signedWorkspaceRequest(t, http.MethodDelete, target, nil, keyMaster, now.Unix(), "")
		rrDel := httptest.NewRecorder()
		r.ServeHTTP(rrDel, del)
		assert.Equal(t, http.StatusOK, rrDel.Code, "delete #%d must be idempotent", i+1)
	}

	get := signedWorkspaceRequest(t, http.MethodGet, target, nil, keyMaster, now.Unix(), "")
	rrGet := httptest.NewRecorder()
	r.ServeHTTP(rrGet, get)
	assert.Equal(t, http.StatusNotFound, rrGet.Code)
}

// TestWorkspaceHTTPPathTraversalRejected exercises validateWorkspaceID's
// rejection through the actual PUT handler, including the crafted
// `?id=_workspace_/ns/agent/sess/../../../_tenant_/x` case from the plan.
func TestWorkspaceHTTPPathTraversalRejected(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)

	ids := []string{
		"_workspace_/ns/agent/sess/../../../_tenant_/x",
		"_workspace_/ns/agent/sess/..",
		"_workspace_/ns/agent/sess/.",
		"_workspace_/ns/agent/sess/_hidden",
		"_workspace_/ns/agent",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			target := "/v1/workspace?id=" + url.QueryEscape(id)
			put := signedWorkspaceRequest(t, http.MethodPut, target, []byte("x"), keyMaster, now.Unix(), "")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, put)
			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	}
}

// TestWorkspacePrefixListAndDelete seeds two objects under one session and one
// under a sibling session, verifies list returns exactly the session's items
// WITH sizes, then verifies prefix-delete removes exactly those two,
// idempotently (a second delete reports 0), leaving the sibling session
// untouched.
func TestWorkspacePrefixListAndDelete(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)

	put := func(id string, payload []byte) {
		t.Helper()
		target := "/v1/workspace?id=" + url.QueryEscape(id)
		req := signedWorkspaceRequest(t, http.MethodPut, target, payload, keyMaster, now.Unix(), "")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}

	sessPrefix := "_workspace_/team-a/agentX/sess1/"
	put(sessPrefix+"a.txt", []byte("aaa"))
	put(sessPrefix+"nested/b.txt", []byte("bb"))
	otherSession := "_workspace_/team-a/agentX/sess2/c.txt"
	put(otherSession, []byte("cccc"))

	listTarget := "/v1/workspace/list?prefix=" + url.QueryEscape(sessPrefix)
	listReq := signedWorkspaceRequest(t, http.MethodGet, listTarget, nil, keyMaster, now.Unix(), "")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, listReq)
	require.Equal(t, http.StatusOK, rr.Code)
	var listResp workspaceListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listResp))
	require.Len(t, listResp.Items, 2)
	byID := map[string]int64{}
	for _, it := range listResp.Items {
		byID[it.ID] = it.Size
	}
	assert.Equal(t, int64(3), byID[sessPrefix+"a.txt"])
	assert.Equal(t, int64(2), byID[sessPrefix+"nested/b.txt"])
	assert.NotContains(t, byID, otherSession)

	delTarget := "/v1/workspace/prefix?prefix=" + url.QueryEscape(sessPrefix)
	delReq := signedWorkspaceRequest(t, http.MethodDelete, delTarget, nil, keyMaster, now.Unix(), "")
	rrDel := httptest.NewRecorder()
	r.ServeHTTP(rrDel, delReq)
	require.Equal(t, http.StatusOK, rrDel.Code)
	var delResp workspaceDeletePrefixResponse
	require.NoError(t, json.Unmarshal(rrDel.Body.Bytes(), &delResp))
	assert.Equal(t, 2, delResp.Deleted)

	delReq2 := signedWorkspaceRequest(t, http.MethodDelete, delTarget, nil, keyMaster, now.Unix(), "")
	rrDel2 := httptest.NewRecorder()
	r.ServeHTTP(rrDel2, delReq2)
	require.Equal(t, http.StatusOK, rrDel2.Code)
	var delResp2 workspaceDeletePrefixResponse
	require.NoError(t, json.Unmarshal(rrDel2.Body.Bytes(), &delResp2))
	assert.Equal(t, 0, delResp2.Deleted, "prefix delete must be idempotent")

	getOther := signedWorkspaceRequest(t, http.MethodGet, "/v1/workspace?id="+url.QueryEscape(otherSession), nil, keyMaster, now.Unix(), "")
	rrOther := httptest.NewRecorder()
	r.ServeHTTP(rrOther, getOther)
	assert.Equal(t, http.StatusOK, rrOther.Code, "sibling session must be untouched")
}

// TestWorkspacePrefixDeleteDoesNotBleedIntoSiblingSessionByName is the
// boundary regression for canonicalWorkspacePrefix: a session name that is a
// literal string-prefix of another ("sess1" vs "sess12") must NOT let an
// un-slashed prefix-delete/list of the former reach the latter. Without
// canonicalizing to a trailing "/" before backend.list, plain string-prefix
// matching would treat "sess1" as a prefix of "sess12/...".
func TestWorkspacePrefixDeleteDoesNotBleedIntoSiblingSessionByName(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)

	put := func(id string, payload []byte) {
		t.Helper()
		target := "/v1/workspace?id=" + url.QueryEscape(id)
		req := signedWorkspaceRequest(t, http.MethodPut, target, payload, keyMaster, now.Unix(), "")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}

	victimID := "_workspace_/team-a/agentX/sess1/a.txt"
	siblingID := "_workspace_/team-a/agentX/sess12/b.txt"
	put(victimID, []byte("victim"))
	put(siblingID, []byte("sibling"))

	// A caller-supplied prefix WITHOUT a trailing slash — validateWorkspacePrefix
	// accepts this form (see TestValidateWorkspacePrefix); the handler must
	// still canonicalize it before matching.
	noSlashPrefix := "_workspace_/team-a/agentX/sess1"

	listTarget := "/v1/workspace/list?prefix=" + url.QueryEscape(noSlashPrefix)
	listReq := signedWorkspaceRequest(t, http.MethodGet, listTarget, nil, keyMaster, now.Unix(), "")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, listReq)
	require.Equal(t, http.StatusOK, rr.Code)
	var listResp workspaceListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listResp))
	ids := make([]string, 0, len(listResp.Items))
	for _, it := range listResp.Items {
		ids = append(ids, it.ID)
	}
	assert.Contains(t, ids, victimID)
	assert.NotContains(t, ids, siblingID, "list(%q) must not bleed into sibling session sess12", noSlashPrefix)

	delTarget := "/v1/workspace/prefix?prefix=" + url.QueryEscape(noSlashPrefix)
	delReq := signedWorkspaceRequest(t, http.MethodDelete, delTarget, nil, keyMaster, now.Unix(), "")
	rrDel := httptest.NewRecorder()
	r.ServeHTTP(rrDel, delReq)
	require.Equal(t, http.StatusOK, rrDel.Code)
	var delResp workspaceDeletePrefixResponse
	require.NoError(t, json.Unmarshal(rrDel.Body.Bytes(), &delResp))
	assert.Equal(t, 1, delResp.Deleted, "only sess1's own object must be deleted")

	getSibling := signedWorkspaceRequest(t, http.MethodGet, "/v1/workspace?id="+url.QueryEscape(siblingID), nil, keyMaster, now.Unix(), "")
	rrSibling := httptest.NewRecorder()
	r.ServeHTTP(rrSibling, getSibling)
	assert.Equal(t, http.StatusOK, rrSibling.Code, "sess12's object must survive a sess1 prefix delete")
}

// TestWorkspaceRoutesThroughProductionWiring exercises makeHandler() itself
// (not the hand-built test mux every other test in this file uses), so a
// wiring mistake in the m.HandleFunc registrations — wrong path, wrong
// method — is caught even though it wouldn't affect any other test here.
func TestWorkspaceRoutesThroughProductionWiring(t *testing.T) {
	master := []byte("storagesvc-test-master-key-long-enough")
	storage := NewLocalStorage(t.TempDir())
	sc, err := MakeStorageClient(logr.Discard(), storage)
	require.NoError(t, err)
	ss := MakeStorageService(logr.Discard(), sc, master, nil, 0)
	handler := ss.makeHandler()

	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	id := "_workspace_/team-a/agentX/sess1/wired.txt"
	target := "/v1/workspace?id=" + url.QueryEscape(id)
	payload := []byte("production wiring check")
	ts := time.Now().Unix()

	putReq := signedWorkspaceRequest(t, http.MethodPut, target, payload, keyMaster, ts, "")
	rrPut := httptest.NewRecorder()
	handler.ServeHTTP(rrPut, putReq)
	require.Equal(t, http.StatusOK, rrPut.Code, rrPut.Body.String())

	getReq := signedWorkspaceRequest(t, http.MethodGet, target, nil, keyMaster, ts, "")
	rrGet := httptest.NewRecorder()
	handler.ServeHTTP(rrGet, getReq)
	require.Equal(t, http.StatusOK, rrGet.Code)
	assert.Equal(t, payload, rrGet.Body.Bytes())
}

// TestWorkspaceRejectsNamespaceScopedSigner verifies every workspace route
// 403s a request authenticated as a namespace-scoped (tenant) principal —
// agentruntime, holding the master-derived key, is the sole legitimate
// caller.
func TestWorkspaceRejectsNamespaceScopedSigner(t *testing.T) {
	r, _, _, master, now := newWorkspaceTestServer(t, 0)
	keyTenant := hmacauth.DeriveServiceKeyNS(master, hmacauth.ServiceStoragesvc, "team-a")

	idTarget := "/v1/workspace?id=" + url.QueryEscape("_workspace_/team-a/agentX/sess1/x.txt")
	listTarget := "/v1/workspace/list?prefix=" + url.QueryEscape("_workspace_/team-a/agentX/sess1/")
	prefixTarget := "/v1/workspace/prefix?prefix=" + url.QueryEscape("_workspace_/team-a/agentX/sess1/")

	cases := []struct {
		name, method, target string
		body                 []byte
	}{
		{"put", http.MethodPut, idTarget, []byte("x")},
		{"get", http.MethodGet, idTarget, nil},
		{"delete", http.MethodDelete, idTarget, nil},
		{"list", http.MethodGet, listTarget, nil},
		{"prefix delete", http.MethodDelete, prefixTarget, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := signedWorkspaceRequest(t, tc.method, tc.target, tc.body, keyTenant, now.Unix(), "team-a")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	}
}

// TestArchiveRoutesSealWorkspaceIDs is blocker 2's regression test: a
// workspace object planted directly in storage must be unreachable through
// every archive route (GET/HEAD/DELETE), for BOTH the master and a
// tenant-scoped signer, and must never appear in the archive list — while a
// genuine legacy archive is unaffected by any of this.
func TestArchiveRoutesSealWorkspaceIDs(t *testing.T) {
	r, sc, storage, master, now := newWorkspaceTestServer(t, 0)
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)
	keyTenant := hmacauth.DeriveServiceKeyNS(master, hmacauth.ServiceStoragesvc, "team-a")

	// Plant a workspace object directly through the backend (bypassing the
	// workspace routes, so this test does not depend on PUT working) and a
	// legacy archive for a control assertion.
	wsID := "_workspace_/team-a/agentX/sess1/secret.txt"
	_, err := sc.backend.put(wsID, strings.NewReader("workspace secret"), 17)
	require.NoError(t, err)
	legacyName, err := storage.getUploadFileName("")
	require.NoError(t, err)
	legacyID, err := sc.backend.put(legacyName, strings.NewReader("legacy archive"), 14)
	require.NoError(t, err)

	do := func(method, target string, key []byte, nsHeader string) *httptest.ResponseRecorder {
		req := signedWorkspaceRequest(t, method, target, nil, key, now.Unix(), nsHeader)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	target := "/v1/archive?id=" + url.QueryEscape(wsID)
	cases := []struct {
		name, method string
		key          []byte
		ns           string
	}{
		{"master GET", http.MethodGet, keyMaster, ""},
		{"master HEAD", http.MethodHead, keyMaster, ""},
		{"master DELETE", http.MethodDelete, keyMaster, ""},
		{"tenant GET", http.MethodGet, keyTenant, "team-a"},
		{"tenant HEAD", http.MethodHead, keyTenant, "team-a"},
		{"tenant DELETE", http.MethodDelete, keyTenant, "team-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(tc.method, target, tc.key, tc.ns)
			assert.Equal(t, http.StatusNotFound, rr.Code)
		})
	}

	t.Run("archive list excludes the workspace id but keeps the legacy one", func(t *testing.T) {
		listRR := do(http.MethodGet, "/v1/archive", keyMaster, "")
		require.Equal(t, http.StatusOK, listRR.Code)
		var ids []string
		require.NoError(t, json.Unmarshal(listRR.Body.Bytes(), &ids))
		assert.Contains(t, ids, legacyID)
		assert.NotContains(t, ids, wsID)
		for _, id := range ids {
			assert.False(t, isWorkspaceID(id), "archive list must never contain a workspace id: %q", id)
		}
	})

	t.Run("the workspace object itself is untouched by the DELETE attempts above", func(t *testing.T) {
		wsGet := do(http.MethodGet, "/v1/workspace?id="+url.QueryEscape(wsID), keyMaster, "")
		assert.Equal(t, http.StatusOK, wsGet.Code)
	})
}

// TestArchivePrunerExcludesWorkspaceRoot is the two-way pruner test: a
// workspace object must be ABSENT from the orphan-candidate set (never sent
// for deletion) while a plain orphan archive in the SAME cycle is still
// pruned — a one-way test could pass by breaking the pruner (e.g. an
// exclusion bug that drops everything) rather than by correctly excluding
// only workspace ids. Runs on the LOCAL backend, where candidate ids are
// ABSOLUTE container paths, so the segment-based filter (not a naive
// HasPrefix) is what is actually exercised.
func TestArchivePrunerExcludesWorkspaceRoot(t *testing.T) {
	storage := NewLocalStorage(t.TempDir())
	sc, err := MakeStorageClient(logr.Discard(), storage)
	require.NoError(t, err)

	wsID := "_workspace_/team-a/agentX/sess1/artifact.txt"
	wsNativeID, err := sc.backend.put(wsID, strings.NewReader("workspace payload"), 18)
	require.NoError(t, err)

	orphanName, err := storage.getUploadFileName("") // legacy unscoped uuid id
	require.NoError(t, err)
	orphanNativeID, err := sc.backend.put(orphanName, strings.NewReader("orphan archive"), 14)
	require.NoError(t, err)

	// Both objects predate the pruner's "too recently modified" grace window
	// (1 minute) so both are real candidates.
	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(wsNativeID, old, old))
	require.NoError(t, os.Chtimes(orphanNativeID, old, old))

	pruner := &ArchivePruner{
		logger:        logr.Discard(),
		crdClient:     fClient.NewSimpleClientset(), // no Packages: everything is a candidate
		archiveChan:   make(chan string),
		storageClient: sc,
		pruneInterval: time.Minute,
	}

	var collected []string
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for id := range pruner.archiveChan {
			collected = append(collected, id)
		}
	}()

	pruner.getOrphanArchives(context.Background())
	close(pruner.archiveChan)
	<-collectDone

	assert.NotContains(t, collected, wsNativeID, "the pruner must never treat the workspace object as an orphan archive")
	for _, id := range collected {
		assert.False(t, isWorkspaceID(id), "pruner sent a workspace-marked id for deletion: %q", id)
	}
	assert.Contains(t, collected, orphanNativeID, "a plain orphan archive in the same cycle must still be pruned")
}

// fakeSubDirStorage is a minimal Storage stand-in that reports a configured
// STORAGE_S3_SUB_DIR-shaped subDir, for pinning workspaceBackendKey's
// subDir-join without needing a real S3/dockertest backend.
type fakeSubDirStorage struct{ subDir string }

func (f fakeSubDirStorage) getStorageType() StorageType                { return StorageTypeS3 }
func (f fakeSubDirStorage) dial() (objectStore, error)                 { return nil, nil }
func (f fakeSubDirStorage) getSubDir() string                          { return f.subDir }
func (f fakeSubDirStorage) getContainerName() string                   { return "bucket" }
func (f fakeSubDirStorage) getUploadFileName(_ string) (string, error) { return "", nil }

// recordingObjectStore is a minimal objectStore stand-in that records the
// keys/prefixes it is called with, so a test can assert on exactly what key
// shape reaches the "backend" without a real S3 endpoint.
type recordingObjectStore struct {
	putKeys      []string
	listPrefixes []string
	items        []objectInfo
}

func (r *recordingObjectStore) put(name string, rd io.Reader, _ int64) (string, error) {
	r.putKeys = append(r.putKeys, name)
	if _, err := io.Copy(io.Discard, rd); err != nil {
		return "", err
	}
	return name, nil
}
func (r *recordingObjectStore) open(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (r *recordingObjectStore) size(string) (int64, error) { return 0, nil }
func (r *recordingObjectStore) remove(string) error        { return nil }
func (r *recordingObjectStore) list(prefix string) ([]objectInfo, error) {
	r.listPrefixes = append(r.listPrefixes, prefix)
	return r.items, nil
}
func (r *recordingObjectStore) exists(string) (bool, error) { return true, nil }

// TestWorkspaceBackendKeyJoinsStorageSubDir pins the fix for the S3 subDir
// bypass finding: on a backend configured with STORAGE_S3_SUB_DIR, workspace
// put/list must fold that subDir into the actual backend key/prefix exactly
// as the archive path does via Storage.getUploadFileName — while the WIRE id
// (the PUT/GET/DELETE query param and every list response id) stays exactly
// the caller-chosen "_workspace_/..." shape, unaware that a subDir exists at
// all. No real S3/dockertest is needed: a fake objectStore records the keys
// it was actually called with.
func TestWorkspaceBackendKeyJoinsStorageSubDir(t *testing.T) {
	fakeBackend := &recordingObjectStore{}
	sc := &StorageClient{
		logger:  logr.Discard(),
		config:  &storageConfig{storage: fakeSubDirStorage{subDir: "some/prefix"}},
		backend: fakeBackend,
	}
	ss := MakeStorageService(logr.Discard(), sc, nil, nil, 0)

	wireID := "_workspace_/team-a/agentX/sess1/artifact.txt"
	putTarget := "/v1/workspace?id=" + url.QueryEscape(wireID)
	putReq := httptest.NewRequest(http.MethodPut, putTarget, strings.NewReader("hello"))
	rrPut := httptest.NewRecorder()
	ss.workspacePutHandler(rrPut, putReq)
	require.Equal(t, http.StatusOK, rrPut.Code, rrPut.Body.String())
	require.Len(t, fakeBackend.putKeys, 1)
	assert.Equal(t, "some/prefix/"+wireID, fakeBackend.putKeys[0],
		"backend key must fold in STORAGE_S3_SUB_DIR the same way the archive path's getUploadFileName does")

	// The PUT response reports only the byte size — the wire id never
	// appears in it, so there is nothing there for subDir to leak into.
	var putResp workspacePutResponse
	require.NoError(t, json.Unmarshal(rrPut.Body.Bytes(), &putResp))
	assert.Equal(t, int64(len("hello")), putResp.Size)

	// list: the prefix reaching backend.list must ALSO be subDir-joined (with
	// the trailing "/" preserved — see workspaceBackendKey), but results must
	// normalize back to the caller's un-joined wire id.
	wirePrefix := "_workspace_/team-a/agentX/sess1/"
	fakeBackend.items = []objectInfo{{id: "some/prefix/" + wireID, size: 5}}
	listTarget := "/v1/workspace/list?prefix=" + url.QueryEscape(wirePrefix)
	listReq := httptest.NewRequest(http.MethodGet, listTarget, nil)
	rrList := httptest.NewRecorder()
	ss.workspaceListHandler(rrList, listReq)
	require.Equal(t, http.StatusOK, rrList.Code, rrList.Body.String())
	require.Len(t, fakeBackend.listPrefixes, 1)
	assert.Equal(t, "some/prefix/"+wirePrefix, fakeBackend.listPrefixes[0],
		"list prefix must be subDir-joined with the trailing slash preserved")

	var listResp workspaceListResponse
	require.NoError(t, json.Unmarshal(rrList.Body.Bytes(), &listResp))
	require.Len(t, listResp.Items, 1)
	assert.Equal(t, wireID, listResp.Items[0].ID,
		"the wire id in the list response must be unchanged — subDir is a storage-layout detail invisible to callers")
}
