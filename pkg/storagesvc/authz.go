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
		legacyArchiveAccess.WithLabelValues().Inc()
		return true
	}
	return arcNS == authNS
}
