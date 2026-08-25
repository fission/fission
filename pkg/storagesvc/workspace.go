// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package storagesvc: this file implements the internal-only session artifact
// workspace object API (G12/G18a) under the reserved workspaceMarker root.
//
// Unlike /v1/archive (server-minted UUID ids), a workspace object id is
// CALLER-CHOSEN — "_workspace_/<namespace>/<agent>/<session>/<path...>" — which
// is a path-traversal surface the archive API never had. Every route validates
// the full id/prefix server-side (validateWorkspaceID / validateWorkspacePrefix)
// before it ever reaches the objectStore backend; the local backend's os.Root
// is defense in depth, not the primary guard (S3 has no such net).
//
// These routes build directly on the objectStore interface (put/open/size/
// remove/list/exists) rather than StorageClient.putFile/getFile — putFile
// takes a multipart.File and mints its own uuid id server-side, neither of
// which fits a machine-to-machine raw-body PUT at a caller-chosen path. The
// local backend's put() returns an ABSOLUTE container path as its id (kept for
// archive in-place-upgrade compatibility); workspace handlers never use that
// return value as the object's id — they key every subsequent GET/DELETE/LIST
// on the REQUEST id (or, for list results, the id normalized back from
// whatever the backend's list() returned — see normalizeWorkspaceID), so the
// id space is uniform across both backends and matches what the caller PUT.
//
// Authorization: every route is master-signer-only — a request whose
// hmacauth.AuthenticatedNamespace is non-empty (a tenant-scoped storage key,
// e.g. a fetcher/builder sidecar's derived key) is rejected with 403.
// agentruntime (holding the master-derived ServiceStoragesvc key) is the sole
// legitimate caller; it is itself what authorizes the identity-bearer/JWT
// caller against its own (namespace, agent) workspace one layer up
// (pkg/agentruntime/workspace.go, a later slice of this feature) before
// signing through here. This mirrors every archive route's posture: when
// FISSION_INTERNAL_AUTH_SECRET is unset the HMAC verifier middleware is not
// registered AT ALL (see makeHandler), so these routes are then as open as
// every archive route already is — that is an accepted, pre-existing
// dev/no-internal-auth trust posture (the same level as the pass-through
// statesvc/agentruntime posture in that install shape), not a gap introduced
// here; it is intentionally NOT special-cased into a workspace-only refusal
// the archive routes don't also have.
//
// The archive routes and the archive/orphan-pruner candidate listing exclude
// every workspace-marked id (see isWorkspaceID in authz.go and its call
// sites) so a workspace object is never reachable through /v1/archive and
// never pruned as an orphaned archive; workspace objects instead live and die
// by their own lifecycle (a session record's archive triggers a prefix purge
// — a later slice of this feature).
package storagesvc

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

const (
	// maxWorkspaceSegmentBytes bounds any single path segment of a workspace
	// id or prefix (namespace, agent, session, or a path component).
	maxWorkspaceSegmentBytes = 128
	// maxWorkspaceIDBytes bounds the full id/prefix string.
	maxWorkspaceIDBytes = 1024
)

// workspaceSegmentCharset is the allowed charset for a non-marker workspace
// path segment: RFC-1123-ish but permissive enough for a session-generated
// filename (dots and underscores allowed mid-segment; a leading underscore is
// rejected separately so a segment can never collide with a reserved marker).
var workspaceSegmentCharset = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateWorkspaceSegment validates a single path segment of a workspace
// id/prefix (everything after the leading workspaceMarker segment): not
// empty, not "." or "..", not leading with "_" (reserved for markers like
// workspaceMarker/archiveTenantMarker), within the length bound, and drawn
// from workspaceSegmentCharset.
func validateWorkspaceSegment(s string) error {
	switch {
	case s == "":
		return errors.New("workspace id contains an empty path segment")
	case s == "." || s == "..":
		return fmt.Errorf("workspace id contains an invalid path segment %q", s)
	case strings.HasPrefix(s, "_"):
		return fmt.Errorf("workspace id path segment %q must not start with '_'", s)
	case len(s) > maxWorkspaceSegmentBytes:
		return fmt.Errorf("workspace id path segment exceeds %d bytes", maxWorkspaceSegmentBytes)
	case !workspaceSegmentCharset.MatchString(s):
		return fmt.Errorf("workspace id path segment %q has invalid characters", s)
	}
	return nil
}

// validateWorkspaceID validates a full workspace object id:
// "_workspace_/<namespace>/<agent>/<session>[/<path...>]". Segments are
// checked literally (no path.Clean first) so a raw ".." segment anywhere —
// including one crafted to walk back out of the workspace root entirely, e.g.
// "_workspace_/ns/agent/sess/../../../_tenant_/x" — is rejected outright by
// validateWorkspaceSegment before any cleaning could resolve it to something
// that looks legitimate.
func validateWorkspaceID(id string) error {
	if len(id) > maxWorkspaceIDBytes {
		return fmt.Errorf("workspace id exceeds %d bytes", maxWorkspaceIDBytes)
	}
	segs := strings.Split(id, "/")
	if len(segs) == 0 || segs[0] != workspaceMarker {
		return fmt.Errorf("workspace id must start with %q", workspaceMarker)
	}
	rest := segs[1:]
	if len(rest) < 3 {
		return errors.New("workspace id must include namespace/agent/session")
	}
	for _, s := range rest {
		if err := validateWorkspaceSegment(s); err != nil {
			return err
		}
	}
	return nil
}

// canonicalWorkspacePrefix appends a trailing "/" if prefix doesn't already
// end with one. Both backends' list() do a plain string-prefix match, so an
// un-slashed prefix like "_workspace_/ns/agent/sess1" would also match a
// sibling session named "sess12" — a cross-session bulk match/delete the
// caller never named. validateWorkspacePrefix deliberately accepts the
// un-slashed form (a caller-friendly convenience matching the plan's route
// doc, which shows the prefix WITH a trailing slash); every handler must
// canonicalize with this before calling backend.list so matching is always
// segment-safe.
func canonicalWorkspacePrefix(prefix string) string {
	if strings.HasSuffix(prefix, "/") {
		return prefix
	}
	return prefix + "/"
}

// validateWorkspacePrefix validates a workspace list/prefix-delete prefix:
// "_workspace_/<namespace>/<agent>/<session>/" (a trailing slash is optional;
// stripped before splitting). Same per-segment rules as validateWorkspaceID,
// requiring at least the namespace/agent/session segments — a prefix may not
// stop short at the namespace or agent level, which would otherwise let a
// single prefix-delete reach every session of an agent (or every agent of a
// namespace) at once. Callers MUST run the validated prefix through
// canonicalWorkspacePrefix before using it for backend.list — see that
// function's doc.
func validateWorkspacePrefix(prefix string) error {
	if len(prefix) > maxWorkspaceIDBytes {
		return fmt.Errorf("workspace prefix exceeds %d bytes", maxWorkspaceIDBytes)
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	segs := strings.Split(trimmed, "/")
	if len(segs) == 0 || segs[0] != workspaceMarker {
		return fmt.Errorf("workspace prefix must start with %q", workspaceMarker)
	}
	rest := segs[1:]
	if len(rest) < 3 {
		return errors.New("workspace prefix must include namespace/agent/session")
	}
	for _, s := range rest {
		if err := validateWorkspaceSegment(s); err != nil {
			return err
		}
	}
	return nil
}

// normalizeWorkspaceID converts a backend-native object id — for the local
// backend an ABSOLUTE container path, for S3 already the bare key — from
// objectStore.list back into the canonical request-style workspace id
// ("_workspace_/..."), by locating the workspaceMarker segment and rejoining
// from there. Same segment-based approach as archiveNamespace/isWorkspaceID,
// so it needs no backend-specific knowledge (no container path threading into
// this file). ok is false if the marker is not present as a segment — it
// should always be, since callers only apply this to items already returned
// by a list(prefix) call whose prefix itself starts with the marker.
func normalizeWorkspaceID(nativeID string) (id string, ok bool) {
	segs := strings.Split(filepath.ToSlash(nativeID), "/")
	for i, s := range segs {
		if s == workspaceMarker {
			return strings.Join(segs[i:], "/"), true
		}
	}
	return "", false
}

// workspaceBackendKey translates a wire-shape workspace id/prefix (already
// validated by validateWorkspaceID/validateWorkspacePrefix) into the actual
// objectStore key: the same STORAGE_S3_SUB_DIR the archive path folds in via
// Storage.getUploadFileName (s3storage.go: path.Join(ss.subDir, ...)) — the
// mechanism an operator uses to let several Fission installs (or several
// environments) share one S3 bucket without colliding. Every workspace
// backend call (put/open/size/remove/list/prefix-delete) MUST go through this
// — skipping it writes/reads at the bucket root, outside the operator's
// install-isolation prefix, so two installs sharing a bucket could collide on
// an identical namespace/agent/session name.
//
// The local backend's getSubDir() always returns "" (mirroring
// localStorage.getUploadFileName, which never joins a subDir either), so
// path.Join is then a no-op and every existing local-backend id/behavior —
// including every test in this package — is unchanged.
//
// This is purely a storage-layout detail: the wire id/prefix shape callers
// see (PUT/GET/DELETE query params, list response ids via normalizeWorkspaceID,
// which is already prefix-agnostic and needs no change) never carries the
// subDir. isWorkspaceID/normalizeWorkspaceID already tolerate an arbitrary
// prefix in front of the marker, so this introduces no new sealing/pruner gap.
func (ss *StorageService) workspaceBackendKey(wireID string) string {
	key := path.Join(ss.storageClient.config.storage.getSubDir(), wireID)
	// path.Join runs path.Clean, which strips a trailing "/" — load-bearing
	// for a canonicalWorkspacePrefix'd prefix (list/prefix-delete): losing it
	// here would reopen the exact sess1-vs-sess12 string-prefix bleed
	// canonicalWorkspacePrefix exists to close, only now via the subDir join
	// instead of a missing trailing slash. Restore it whenever the input had
	// one; plain object ids (put/get/delete) never end in "/", so this is a
	// no-op for them.
	if strings.HasSuffix(wireID, "/") && !strings.HasSuffix(key, "/") {
		key += "/"
	}
	return key
}

// rejectNamespaceScoped writes 403 and returns true if the request's
// authenticated principal is namespace-scoped (a tenant-derived storagesvc
// key). Every workspace route calls this first: agentruntime (holding the
// master-derived key) is the sole legitimate caller, so a tenant fetcher/
// builder key — which legitimately reaches /v1/archive — must never reach a
// workspace object. An empty/absent principal (master, or the verifier not
// registered at all) is unrestricted, matching every other handler's
// AuthenticatedNamespace convention in this package.
func rejectNamespaceScoped(w http.ResponseWriter, r *http.Request) bool {
	if ns, _ := hmacauth.AuthenticatedNamespace(r.Context()); ns != "" {
		http.Error(w, "workspace routes are master-signer-only", http.StatusForbidden)
		return true
	}
	return false
}

func (ss *StorageService) getPrefixFromRequest(r *http.Request) (string, error) {
	values := r.URL.Query()
	prefixes, ok := values["prefix"]
	if !ok || len(prefixes) == 0 {
		return "", errors.New("missing `prefix' query param")
	}
	return prefixes[0], nil
}

type workspacePutResponse struct {
	Size int64 `json:"size"`
}

type workspaceItem struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

type workspaceListResponse struct {
	Items []workspaceItem `json:"items"`
}

type workspaceDeletePrefixResponse struct {
	Deleted int `json:"deleted"`
}

// countingReader wraps an io.Reader and counts the bytes it yields, so the PUT
// handler can report the actual number of bytes streamed to the backend
// without a second stat() round-trip (and without trusting a caller-supplied
// Content-Length, which S3 also receives but which the local backend ignores
// entirely).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// workspacePutHandler handles PUT /v1/workspace?id=<id>: a raw-body,
// streamed write. Unlike archive uploads (multipart, buffered) this is a
// machine-to-machine hop — no multipart, no StorageClient.putFile (which
// would mint its own id) — straight to the objectStore backend, keyed on the
// caller's (validated) id.
func (ss *StorageService) workspacePutHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	if rejectNamespaceScoped(w, r) {
		return
	}

	id, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateWorkspaceID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	defer body.Close()

	cr := &countingReader{r: body}
	// The local backend ignores the size hint entirely; the S3 backend passes
	// it through to minio's PutObject, which accepts -1 for an unknown-length
	// streamed upload (chunked transfer, no Content-Length header) — so
	// r.ContentLength (0, a real length, or -1) is always a valid hint here.
	if _, err := ss.storageClient.backend.put(ss.workspaceBackendKey(id), cr, r.ContentLength); err != nil {
		logger.Error(err, "error writing workspace object", "id", id)
		http.Error(w, "error writing workspace object", http.StatusInternalServerError)
		return
	}

	resp, err := json.Marshal(workspacePutResponse{Size: cr.n})
	if err != nil {
		logger.Error(err, "error marshaling workspace put response", "id", id)
		http.Error(w, "error marshaling response", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(resp); err != nil {
		logger.Error(err, "error writing HTTP response", "id", id)
	}
}

// workspaceGetHandler handles GET /v1/workspace?id=<id>: an io.Copy
// passthrough stream with Content-Length set from a size() stat — no
// multipart, no buffering.
func (ss *StorageService) workspaceGetHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	if rejectNamespaceScoped(w, r) {
		return
	}

	id, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateWorkspaceID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	size, err := ss.storageClient.backend.size(ss.workspaceBackendKey(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		logger.Error(err, "error stat-ing workspace object", "id", id)
		http.Error(w, "error retrieving workspace object", http.StatusInternalServerError)
		return
	}

	f, err := ss.storageClient.backend.open(ss.workspaceBackendKey(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		logger.Error(err, "error opening workspace object", "id", id)
		http.Error(w, "error retrieving workspace object", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err := io.Copy(w, f); err != nil {
		logger.Error(err, "error streaming workspace object", "id", id)
	}
}

// workspaceDeleteHandler handles DELETE /v1/workspace?id=<id>. Idempotent: a
// missing object is a 200, not a 404 — the caller (agentruntime, and the
// session-archive purge behind it) may retry or race a concurrent delete.
func (ss *StorageService) workspaceDeleteHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	if rejectNamespaceScoped(w, r) {
		return
	}

	id, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateWorkspaceID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := ss.storageClient.backend.remove(ss.workspaceBackendKey(id)); err != nil && !errors.Is(err, ErrNotFound) {
		logger.Error(err, "error deleting workspace object", "id", id)
		http.Error(w, "error deleting workspace object", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// workspaceListHandler handles GET /v1/workspace/list?prefix=<prefix>: every
// object under prefix, with sizes, ids normalized back to the canonical
// request-style form (see normalizeWorkspaceID).
func (ss *StorageService) workspaceListHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	if rejectNamespaceScoped(w, r) {
		return
	}

	prefix, err := ss.getPrefixFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateWorkspacePrefix(prefix); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	infos, err := ss.storageClient.backend.list(ss.workspaceBackendKey(canonicalWorkspacePrefix(prefix)))
	if err != nil {
		logger.Error(err, "error listing workspace objects", "prefix", prefix)
		http.Error(w, "error listing workspace objects", http.StatusInternalServerError)
		return
	}

	items := make([]workspaceItem, 0, len(infos))
	for _, info := range infos {
		id, ok := normalizeWorkspaceID(info.id)
		if !ok {
			// Cannot happen for an id this store itself returned from a
			// prefix search whose prefix already starts with workspaceMarker;
			// skip defensively rather than surface a native (backend-shaped)
			// id to the caller.
			logger.Error(nil, "workspace list item has no marker segment; skipping", "id", info.id)
			continue
		}
		items = append(items, workspaceItem{ID: id, Size: info.size})
	}

	resp, err := json.Marshal(workspaceListResponse{Items: items})
	if err != nil {
		logger.Error(err, "error marshaling workspace list response", "prefix", prefix)
		http.Error(w, "error marshaling response", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(resp); err != nil {
		logger.Error(err, "error writing HTTP response", "prefix", prefix)
	}
}

// workspaceDeletePrefixHandler handles DELETE /v1/workspace/prefix?prefix=<prefix>:
// idempotent bulk delete of every object under prefix — the session-archive
// purge's primitive (a later slice of this feature). A per-item removal error
// is logged and the item is not counted as deleted; it does not abort the
// rest of the sweep (log-don't-retry, matching the purge's own stance). This
// means `deleted == N` is NOT a guarantee that every matched object was
// removed — a caller that needs "fully cleared" must not infer it from the
// count; log-don't-retry accepts silently-undercounted residue by design.
func (ss *StorageService) workspaceDeletePrefixHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	if rejectNamespaceScoped(w, r) {
		return
	}

	prefix, err := ss.getPrefixFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateWorkspacePrefix(prefix); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	infos, err := ss.storageClient.backend.list(ss.workspaceBackendKey(canonicalWorkspacePrefix(prefix)))
	if err != nil {
		logger.Error(err, "error listing workspace objects for prefix delete", "prefix", prefix)
		http.Error(w, "error deleting workspace objects", http.StatusInternalServerError)
		return
	}

	deleted := 0
	for _, info := range infos {
		if err := ss.storageClient.backend.remove(info.id); err != nil && !errors.Is(err, ErrNotFound) {
			logger.Error(err, "error deleting workspace object during prefix delete", "id", info.id)
			continue
		}
		deleted++
	}

	resp, err := json.Marshal(workspaceDeletePrefixResponse{Deleted: deleted})
	if err != nil {
		logger.Error(err, "error marshaling workspace prefix delete response", "prefix", prefix)
		http.Error(w, "error marshaling response", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(resp); err != nil {
		logger.Error(err, "error writing HTTP response", "prefix", prefix)
	}
}
