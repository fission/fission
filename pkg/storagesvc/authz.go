// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// archiveTenantMarker is the fixed path segment that precedes the owning
// namespace in a namespace-scoped archive id (".../_tenant_/<namespace>/<uuid>").
// It contains an underscore, which an RFC-1123 namespace label cannot, so it can
// never collide with a real namespace or a UUID. It is RESERVED: it must not be
// used as a storage sub-directory (SUBDIR / STORAGE_S3_SUB_DIR).
const archiveTenantMarker = "_tenant_"

// workspaceMarker is the fixed path segment that roots the reserved session
// artifact workspace object space (".../_workspace_/<ns>/<agent>/<session>/...",
// see workspace.go). Reserved exactly like archiveTenantMarker: an underscore
// makes it impossible for an RFC-1123 namespace label to collide with it, and
// it must never be used as a storage sub-directory (SUBDIR / STORAGE_S3_SUB_DIR).
// The archive pruner and the /v1/archive routes must never treat an id
// containing this marker as an archive — see isWorkspaceID and its call sites.
const workspaceMarker = "_workspace_"

// isWorkspaceID reports whether id contains the reserved workspaceMarker as a
// path segment, independent of any backend-added prefix — the local backend's
// id is the ABSOLUTE container path, and an S3 id may carry a
// STORAGE_S3_SUB_DIR prefix — so a naive strings.HasPrefix(id, workspaceMarker)
// would miss both. Same segment-based approach as archiveNamespace (path.Clean
// first so a crafted ".."-bearing id is judged on what the backend will
// actually resolve, not the literal string).
//
// This is the seal between the workspace object space and the archive routes:
// the archive handlers (download/delete/info) and the archive/orphan-pruner
// candidate listing (getItemIDsWithFilter) all reject/exclude on this check so
// a workspace object is never reachable as if it were an archive — including
// for the master principal, which authorizedFor's legacy-id grandfather would
// otherwise wave through unconditionally.
func isWorkspaceID(id string) bool {
	for _, s := range strings.Split(path.Clean(filepath.ToSlash(id)), "/") {
		if s == workspaceMarker {
			return true
		}
	}
	return false
}

// rejectWorkspaceID is the archive routes' seal against workspace ids (see
// isWorkspaceID). Every archive handler (download/delete/info) MUST call it
// BEFORE ss.authorizedFor, whose legacy-id grandfather would otherwise wave a
// _workspace_-marked id through as an unscoped legacy archive. It writes a 404
// (never a 403 — a workspace object simply is not an archive) with the caller's
// route-specific message and returns true when the id was rejected.
func rejectWorkspaceID(w http.ResponseWriter, fileID, notFoundMsg string) bool {
	if isWorkspaceID(fileID) {
		http.Error(w, notFoundMsg, http.StatusNotFound)
		return true
	}
	return false
}

// archiveNamespace returns the namespace that owns the archive id, or "" for a
// legacy/unscoped id (one with no marker). It locates the marker segment and
// returns the following segment when it is a valid namespace label and is itself
// followed by a (uuid) segment — independent of the backend's storage prefix
// (container path for local, subDir for S3), so it needs no backend state and is
// robust to a subDir change between releases. "" maps a legacy id to the
// grandfathered path in authorizedFor.
//
// The id is path.Clean'd first so the namespace we authorize against is the one
// the storage backend will actually resolve to. Without this, a crafted id like
// "_tenant_/<ownNS>/../<victimNS>/<uuid>" would parse as ownNS (authorized) yet
// the local backend (relName → filepath.Clean) collapses the ".." and resolves
// the victim's file — a cross-tenant read/delete. idHasParentTraversal is the
// belt-and-suspenders reject; cleaning here keeps parse and resolve in agreement
// regardless.
func archiveNamespace(id string) string {
	segs := strings.Split(path.Clean(filepath.ToSlash(id)), "/")
	for i, s := range segs {
		if s != archiveTenantMarker {
			continue
		}
		// Require <namespace> at i+1 and a trailing <uuid> segment at i+2 so a
		// directory that happens to be named like the marker (e.g. a misconfigured
		// subDir) with a single trailing segment is not mistaken for a namespace.
		if i+2 < len(segs) && validNamespaceLabel(segs[i+1]) {
			return segs[i+1]
		}
		return ""
	}
	return ""
}

// idHasParentTraversal reports whether the id contains a ".." path segment. Such
// an id is never legitimate (real ids are clean uuids / _tenant_/ns/uuid /
// absolute container paths) and is rejected outright before authorization, so a
// parse-vs-resolve disagreement can never be reached.
func idHasParentTraversal(id string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(id), "/"), "..")
}

// validNamespaceLabel reports whether ns is a syntactically valid RFC-1123
// namespace label. Used both to parse a namespace out of an id and to reject a
// malformed authenticated namespace before it is joined into a storage path —
// load-bearing for S3, which has no os.Root path confinement.
func validNamespaceLabel(ns string) bool {
	return len(validation.IsDNS1123Label(ns)) == 0
}

// authorizedFor reports whether the request's authenticated principal may access
// the archive id. The principal is the namespace whose HMAC key verified the
// request (hmacauth.AuthenticatedNamespace) — never a caller-controlled header:
//   - "" (master / control-plane / pre-tenancy caller): unrestricted, unchanged.
//   - a tenant namespace N: may access archives owned by N, plus legacy/unscoped
//     archives (grandfathered — UUID-unguessability, as before this change), but
//     not another tenant's namespace-scoped archive.
func (ss *StorageService) authorizedFor(r *http.Request, id string) bool {
	authNS, _ := hmacauth.AuthenticatedNamespace(r.Context())
	if authNS == "" {
		return true
	}
	// Reject traversal before authorizing: a ".." could otherwise steer a request
	// authorized as the caller's namespace to another tenant's archive.
	if idHasParentTraversal(id) {
		return false
	}
	arcNS := archiveNamespace(id)
	if arcNS == "" {
		legacyArchiveAccess.Add(r.Context(), 1)
		return true
	}
	return arcNS == authNS
}
