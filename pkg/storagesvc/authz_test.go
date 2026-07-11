// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils/httpmux"
)

func TestArchiveNamespace(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"legacy bare uuid", uuid, ""},
		{"legacy s3 subdir/uuid", "sub/" + uuid, ""},
		{"legacy local absolute", "/data/fission-functions/" + uuid, ""},
		{"scoped local", "/data/fission-functions/" + archiveTenantMarker + "/team-a/" + uuid, "team-a"},
		{"scoped s3 no subdir", archiveTenantMarker + "/team-a/" + uuid, "team-a"},
		{"scoped s3 with subdir", "sub/dir/" + archiveTenantMarker + "/team-b/" + uuid, "team-b"},
		{"marker without trailing uuid is not a namespace", archiveTenantMarker + "/team-a", ""},
		{"segment after marker is not a valid label", archiveTenantMarker + "/Not_A_Label/" + uuid, ""},
		{"marker as a coincidental subdir with single trailing segment", archiveTenantMarker + "/" + uuid, ""},
		// Traversal: the parsed namespace must reflect what the backend resolves
		// (the cleaned path), not the literal prefix — else a sibling-escape id
		// would authorize as one tenant but read another's file.
		{"traversal resolves to the real owner, not the literal prefix", archiveTenantMarker + "/team-a/../team-b/" + uuid, "team-b"},
		{"traversal escaping the marker is unowned", archiveTenantMarker + "/team-a/../../etc/passwd", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, archiveNamespace(tc.id))
		})
	}
}

func TestIdHasParentTraversal(t *testing.T) {
	assert.True(t, idHasParentTraversal("_tenant_/a/../b/uuid"))
	assert.True(t, idHasParentTraversal("/container/_tenant_/a/../b/uuid"))
	assert.True(t, idHasParentTraversal("../../etc/passwd"))
	assert.False(t, idHasParentTraversal("_tenant_/team-a/uuid"))
	assert.False(t, idHasParentTraversal("550e8400-e29b-41d4-a716-446655440000"))
	assert.False(t, idHasParentTraversal("/container/_tenant_/team-a/uuid"))
}

func TestValidNamespaceLabel(t *testing.T) {
	assert.True(t, validNamespaceLabel("team-a"))
	assert.True(t, validNamespaceLabel("default"))
	assert.False(t, validNamespaceLabel(""))
	assert.False(t, validNamespaceLabel("../escape"))
	assert.False(t, validNamespaceLabel("a/b"))
	assert.False(t, validNamespaceLabel("UpperCase"))
	assert.False(t, validNamespaceLabel(strings.Repeat("a", 64)))
}

// TestGetUploadFileNameScoping locks the round-trip: a namespace-scoped upload
// name parses back to its namespace via archiveNamespace, for both backends and
// regardless of the S3 subDir, while an empty namespace yields a legacy id.
func TestGetUploadFileNameScoping(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		s := NewLocalStorage(t.TempDir())
		scoped, err := s.getUploadFileName("team-a")
		require.NoError(t, err)
		assert.Equal(t, "team-a", archiveNamespace(scoped))
		legacy, err := s.getUploadFileName("")
		require.NoError(t, err)
		assert.Empty(t, archiveNamespace(legacy))
	})
	t.Run("s3 empty subdir", func(t *testing.T) {
		t.Setenv("STORAGE_S3_SUB_DIR", "")
		s := NewS3Storage()
		scoped, err := s.getUploadFileName("team-a")
		require.NoError(t, err)
		assert.Equal(t, "team-a", archiveNamespace(scoped))
		legacy, err := s.getUploadFileName("")
		require.NoError(t, err)
		assert.Empty(t, archiveNamespace(legacy))
	})
	t.Run("s3 with subdir", func(t *testing.T) {
		t.Setenv("STORAGE_S3_SUB_DIR", "some/prefix")
		s := NewS3Storage()
		scoped, err := s.getUploadFileName("team-b")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(scoped, "some/prefix/"), "keeps the configured subDir")
		assert.Equal(t, "team-b", archiveNamespace(scoped))
	})
}

// TestArchiveAuthzHandlers exercises the full authorization wiring: the real
// namespace-scoped verifier sets the principal, and the handlers enforce it. A
// tenant reaches its own and legacy archives but a 404 (not 403) hides another
// tenant's; the master principal is unrestricted.
func TestArchiveAuthzHandlers(t *testing.T) {
	master := []byte("storagesvc-test-master-key-long-enough")
	now := time.Unix(1715000123, 0)

	storage := NewLocalStorage(t.TempDir())
	sc, err := MakeStorageClient(logr.Discard(), storage)
	require.NoError(t, err)
	ss := MakeStorageService(logr.Discard(), sc, master, nil, 0)

	m := httpmux.New(httpmux.WithMiddleware(hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc,
		hmacauth.VerifierOpts{SkewSec: 60, Now: func() time.Time { return now }})))
	m.HandleFunc("/v1/archive", ss.getOrListHandler).Methods(http.MethodGet) // ?id= → download
	m.HandleFunc("/v1/archive", ss.deleteHandler).Methods(http.MethodDelete)
	m.HandleFunc("/v1/archive", ss.infoHandler).Methods(http.MethodHead)
	r := m.Handler()

	// Seed archives directly through the backend (the upload path is covered by
	// TestGetUploadFileNameScoping); returns the stored id.
	seed := func(ns, content string) string {
		name, err := storage.getUploadFileName(ns)
		require.NoError(t, err)
		id, err := sc.backend.put(name, strings.NewReader(content), int64(len(content)))
		require.NoError(t, err)
		return id
	}
	idA := seed("team-a", "a-archive")
	idB := seed("team-b", "b-archive")
	idLegacy := seed("", "legacy-archive")

	do := func(method, id string, signKey []byte, nsHeader string) int {
		target := "/v1/archive?id=" + url.QueryEscape(id)
		req := httptest.NewRequest(method, target, nil)
		ts := now.Unix()
		sig := hmacauth.Sign(signKey, method, req.URL.RequestURI(), nil, ts)
		req.Header.Set(hmacauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(hmacauth.HeaderSignature, sig)
		if nsHeader != "" {
			req.Header.Set(hmacauth.HeaderNamespace, nsHeader)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Code
	}

	keyA := hmacauth.DeriveServiceKeyNS(master, hmacauth.ServiceStoragesvc, "team-a")
	keyMaster := hmacauth.DeriveServiceKey(master, hmacauth.ServiceStoragesvc)

	t.Run("tenant reads its own archive", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idA, keyA, "team-a"))
		assert.Equal(t, http.StatusOK, do(http.MethodHead, idA, keyA, "team-a"))
	})
	t.Run("tenant is 404 on another tenant's archive (no existence oracle)", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, do(http.MethodGet, idB, keyA, "team-a"))
		assert.Equal(t, http.StatusNotFound, do(http.MethodHead, idB, keyA, "team-a"))
		assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, idB, keyA, "team-a"))
	})
	t.Run("tenant reads a legacy unscoped archive (grandfathered)", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idLegacy, keyA, "team-a"))
	})
	t.Run("master principal is unrestricted", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idA, keyMaster, ""))
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idB, keyMaster, ""))
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idLegacy, keyMaster, ""))
	})
	// Regression: a path-traversal id that CLEANS to tenant B's archive but is
	// prefixed with the attacker's own namespace must NOT grant access. Without the
	// fix archiveNamespace parsed the literal prefix ("team-a") and authorized,
	// while the local backend collapsed the ".." and resolved team-b's file.
	t.Run("path traversal cannot reach another tenant's archive", func(t *testing.T) {
		crafted := strings.Replace(idB, "/"+archiveTenantMarker+"/team-b/", "/"+archiveTenantMarker+"/team-a/../team-b/", 1)
		require.NotEqual(t, idB, crafted, "craft must differ from the real id")
		require.Equal(t, "team-b", archiveNamespace(crafted), "cleaned id resolves to the real owner")
		assert.Equal(t, http.StatusNotFound, do(http.MethodGet, crafted, keyA, "team-a"))
		assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, crafted, keyA, "team-a"))
		assert.Equal(t, http.StatusNotFound, do(http.MethodHead, crafted, keyA, "team-a"))
		// And team-b's archive is intact (master can still read it).
		assert.Equal(t, http.StatusOK, do(http.MethodGet, idB, keyMaster, ""))
	})

	// Guard the local nested-dir put: a scoped id round-trips on disk.
	t.Run("scoped archive is retrievable by exact id", func(t *testing.T) {
		require.Equal(t, "team-a", archiveNamespace(idA))
		_, statErr := sc.backend.size(idA)
		require.NoError(t, statErr)
		require.True(t, strings.Contains(filepath.ToSlash(idA), "/"+archiveTenantMarker+"/team-a/"))
	})
}
