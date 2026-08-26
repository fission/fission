// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// workspace.go implements the session artifact workspace API (G12/G18a):
// PUT/GET/DELETE/LIST routes mounted on the agent runtime mux that proxy to
// storagesvc's internal-only /v1/workspace surface (pkg/storagesvc/
// workspace.go). This is the ONLY path pods use to reach that surface — see
// pkg/storagesvc/workspace.go's package doc for why agentruntime, not
// storagesvc, is where the identity bearer is verified (it is the only place
// that can verify it WITH revocation, via the live AgentView).
//
// Security model, in the order every handler applies it:
//
//  1. Auth (outer middleware, main.go): IdentityOrJWTOwnWorkspace — the G16
//     identity bearer under a STRICTER own-workspace-only closure than
//     dispatch's (see identity.go's verifyOwnWorkspace), or the existing JWT
//     namespace-scoped middleware for debugging parity.
//  2. Disabled check: STORAGESVC_URL unset means client is nil and every
//     route answers 503 rather than agentruntime guessing a URL.
//  3. Path validation (this layer's own defense in depth, mirroring
//     pkg/storagesvc/workspace.go's rules exactly): namespace/name are
//     DNS-1123 labels, session matches sessionIDPattern and does not
//     collide with the checkpoint keyspace, and {path...} segments are
//     charset/length-bounded and reject ".", "..", empty, and a leading "_"
//     (reserved for the "_workspace_" root marker).
//  4. Existence anchor: store.Get(ns, agent, session) must find a LIVE
//     session record BEFORE any storagesvc call. This is the GC anchor, not
//     tidiness — the session-archive purge (sweeper.go, a later slice) fires
//     at record archive, and the archive pruner excludes the workspace root
//     entirely, so a workspace object under a session id that was never
//     recorded here would be deleted by NOTHING, ever: a permanent orphan
//     any identity/JWT holder could otherwise mint by PUTting to an
//     arbitrary session id. It doubles as the authorization anchor (the
//     record only exists under the (ns, agent) SessionStore itself scoped
//     it to) and keeps the dispatcher the sole session-minting authority
//     (Q32). Residual orphans from records that TTL out unswept (e.g. a
//     deleted agent Function) join the already-named orphan-sweep
//     follow-up, not solved here.
//  5. Quota (PUT only): a per-artifact byte cap (AGENT_MAX_ARTIFACT_BYTES), a
//     per-session byte budget (AGENT_MAX_SESSION_ARTIFACT_BYTES), and a
//     per-session artifact-COUNT budget (AGENT_MAX_SESSION_ARTIFACTS — the
//     bytes-only budget's zero-byte-artifact bypass, and what structurally
//     bounds a session's LIST response size) — see handlePut's doc for the
//     delta-accounting and pre-check/write/settle ordering.
//
// Streaming: the request body is spooled (spoolWorkspaceBody) rather than
// buffered whole — bounded in-memory up to workspaceSpoolThresholdBytes,
// spilled to a temp file above it — so hmacauth.Signer's streaming-hash path
// (it reads a fresh copy via GetBody to hash, while the transport streams
// the original Body) never requires holding an artifact up to the full
// AGENT_MAX_ARTIFACT_BYTES cap in RAM at once. GET is a plain io.Copy
// passthrough with Content-Length set from storagesvc's own stream.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/fission/fission/pkg/statestore"
)

const (
	// workspaceMarker is the reserved root segment of every workspace object
	// id/prefix, matching pkg/storagesvc/authz.go's workspaceMarker exactly
	// (the wire contract the two packages share). Kept as a local copy
	// rather than an import: storagesvc's constant is unexported, and this
	// is the only string this package needs from it.
	workspaceMarker = "_workspace_"

	// maxWorkspaceSegmentBytes / maxWorkspacePathBytes bound a single
	// {path...} segment and the full path, mirroring
	// pkg/storagesvc/workspace.go's maxWorkspaceSegmentBytes/
	// maxWorkspaceIDBytes (that bound is on the FULL id, marker+ns+agent+
	// session+path; this bound is on the path portion alone, so it is
	// intentionally smaller — well within storagesvc's own ceiling once the
	// marker/ns/agent/session prefix is added back).
	maxWorkspaceSegmentBytes = 128
	maxWorkspacePathBytes    = 1024

	// workspaceSpoolThresholdBytes bounds how much of a PUT body this
	// handler keeps in memory before spilling to a temp file, mirroring
	// storagesvc's own uploadSpoolThresholdBytes (storagesvc.go) and the
	// HMAC verifier's SpoolThresholdBytes spill pattern (pkg/auth/hmac/
	// spool.go) — on the SIGNING side here instead of the verifying side.
	workspaceSpoolThresholdBytes int64 = 4 << 20

	// workspaceIdleConnsPerHost sizes the internal storagesvc client's idle
	// connection pool, matching pkg/storagesvc/client's own
	// storagesvcIdleConnsPerHost sizing for the same single upstream.
	workspaceIdleConnsPerHost = 16

	// workspaceListMaxResponseBytes bounds how much of a storagesvc LIST
	// response workspaceClient.list will buffer. Tied to the artifact-COUNT
	// budget (AGENT_MAX_SESSION_ARTIFACTS, defaultMaxSessionArtifacts items
	// in main.go): a session's list JSON is at most roughly
	// defaultMaxSessionArtifacts items * (workspaceMarker + ns + agent +
	// session + a maxWorkspacePathBytes path + a size field + JSON
	// punctuation), which comfortably fits well under this cap at the
	// default count budget — a legitimate list never truncates. A caller
	// that somehow produced a bigger response already violated the count
	// budget upstream (or storagesvc/agentruntime disagree on it); either
	// way this is defense in depth, not the primary bound: without it an
	// unbounded io.ReadAll of a hostile or corrupted upstream response would
	// be a shared-control-plane memory amplification (one LIST call turning
	// into an unbounded allocation), not merely a slow one.
	workspaceListMaxResponseBytes int64 = 8 << 20
)

// workspaceSegmentCharset is the allowed charset for a non-marker workspace
// path segment, identical to pkg/storagesvc/workspace.go's
// workspaceSegmentCharset.
var workspaceSegmentCharset = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateWorkspaceSegment validates one "/"-split segment of a caller
// {path...} value: not empty, not "." or "..", not leading with "_"
// (reserved for the workspaceMarker root and any future marker), within the
// length bound, and drawn from workspaceSegmentCharset. Mirrors
// pkg/storagesvc/workspace.go's validateWorkspaceSegment exactly — this
// layer's defense in depth does not rely on storagesvc's own validation
// (Global Constraints: "never rely on localstorage's os.Root — S3 has no
// such net").
func validateWorkspaceSegment(s string) error {
	switch {
	case s == "":
		return errors.New("workspace path contains an empty segment")
	case s == "." || s == "..":
		return fmt.Errorf("workspace path contains an invalid segment %q", s)
	case strings.HasPrefix(s, "_"):
		return fmt.Errorf("workspace path segment %q must not start with '_'", s)
	case len(s) > maxWorkspaceSegmentBytes:
		return fmt.Errorf("workspace path segment exceeds %d bytes", maxWorkspaceSegmentBytes)
	case !workspaceSegmentCharset.MatchString(s):
		return fmt.Errorf("workspace path segment %q has invalid characters", s)
	}
	return nil
}

// validateWorkspacePath validates a full {path...} value: bounded length,
// every "/"-split segment individually valid. An EMPTY path — which is what
// Go's ServeMux hands {path...} for a trailing-slash request against the
// file route (".../session/") — is rejected here too: strings.Split("", "/")
// yields one empty-string segment, and validateWorkspaceSegment rejects
// that, so a trailing-slash PUT/GET/DELETE 400s rather than being silently
// treated as the list route (which is a DIFFERENT, shorter pattern with no
// trailing slash at all — see mountWorkspaceRoutes).
func validateWorkspacePath(p string) error {
	if len(p) > maxWorkspacePathBytes {
		return fmt.Errorf("workspace path exceeds %d bytes", maxWorkspacePathBytes)
	}
	for _, s := range strings.Split(p, "/") {
		if err := validateWorkspaceSegment(s); err != nil {
			return err
		}
	}
	return nil
}

// validateWorkspaceRouteIdentity validates the namespace/name/session path
// values common to every workspace route, writing a 400 and returning false
// on the first failure. namespace/name are validated as DNS-1123 labels
// (ParseParentRef, session.go, is this package's existing precedent for that
// rule) — this also closes the encoded-segment traversal surface Go's
// ServeMux otherwise opens: {namespace}/{name} are decoded by the mux before
// PathValue returns them, so a request built with "%2F" or "%2E" segments
// arrives here already decoded, and DNS-1123 rejects '/' and a bare "."/".."
// outright. session is validated with the dispatcher's own sessionIDPattern
// plus the same CheckpointKeyPrefix collision rule resolveSessionID applies
// (dispatcher.go) — a workspace session id is never minted here, only
// checked against what dispatch already validated when it created the
// record.
func validateWorkspaceRouteIdentity(w http.ResponseWriter, ns, name, session string) bool {
	if len(validation.IsDNS1123Label(ns)) != 0 {
		writeErr(w, http.StatusBadRequest, "invalid namespace")
		return false
	}
	if len(validation.IsDNS1123Label(name)) != 0 {
		writeErr(w, http.StatusBadRequest, "invalid agent name")
		return false
	}
	if !sessionIDPattern.MatchString(session) || strings.HasPrefix(session, CheckpointKeyPrefix) {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return false
	}
	// sessionIDPattern's charset (identical to workspaceSegmentCharset) still
	// admits ".", "..", and a leading "_" — none of which resolveSessionID
	// itself ever mints, but a caller can still SUPPLY a session id matching
	// one, and this route treats {session} as a wire path segment.
	// validateWorkspaceSegment closes exactly that gap: without it, such a
	// session id sails through this check and only fails downstream at
	// storagesvc's own validateWorkspaceID (pkg/storagesvc/workspace.go),
	// surfacing as an opaque 502 here instead of this layer's own 400 —
	// contradicting this function's "mirrors storagesvc's rules exactly"
	// defense-in-depth claim. Every session id this check newly rejects was
	// already guaranteed to fail at storagesvc, so this only replaces a 502
	// with a 400 — no previously-working id is affected.
	if err := validateWorkspaceSegment(session); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid session id: "+err.Error())
		return false
	}
	return true
}

// workspaceSessionPrefix is the canonical "_workspace_/<ns>/<agent>/<session>/"
// root every object/prefix under one session shares.
func workspaceSessionPrefix(ns, agent, session string) string {
	return workspaceMarker + "/" + ns + "/" + agent + "/" + session + "/"
}

// workspaceID is the full wire id for one artifact path within a session.
func workspaceID(ns, agent, session, path string) string {
	return workspaceSessionPrefix(ns, agent, session) + path
}

// stripWorkspacePrefix converts a storagesvc-returned list item id back into
// the caller-facing relative path within the (ns, agent, session) scope
// requested. ok is false if id is not actually under that session's prefix
// (defensive: should be unreachable given the list call itself was scoped to
// the prefix, but a caller must not surface a raw "_workspace_/..." id to
// the client if it somehow were).
func stripWorkspacePrefix(id, ns, agent, session string) (path string, ok bool) {
	return strings.CutPrefix(id, workspaceSessionPrefix(ns, agent, session))
}

// workspaceSpool holds a PUT request body drained once: in memory when it
// fits within workspaceSpoolThresholdBytes, spilled to a temp file above
// that. Mirrors pkg/auth/hmac/spool.go's bodySpool, but on the signing
// (client) side instead of the verifying (server) side, and without a hash
// — hmacauth.Signer computes its own hash from a fresh reader() call via
// GetBody; this spool only needs to hand out that fresh copy on demand.
type workspaceSpool struct {
	mem      []byte
	fileName string // "" when mem holds the body.
	size     int64
}

// spoolWorkspaceBody drains src (already wrapped by the caller in
// io.LimitReader(body, cap+1) for over-read detection — see handlePut) into
// a workspaceSpool. Reading exactly threshold bytes with more remaining
// spills the rest (plus the already-buffered prefix) to a temp file, so
// caller memory stays bounded by threshold regardless of how large the
// (LimitReader-capped) body is.
func spoolWorkspaceBody(src io.Reader, threshold int64) (*workspaceSpool, error) {
	var buf bytes.Buffer
	// A body of EXACTLY threshold bytes takes the file-spill branch below
	// (io.CopyN's LimitReader synthesizes its own EOF once it has copied
	// threshold bytes, without needing the underlying src to report EOF —
	// so err is nil, not io.EOF, even though there is nothing left to read).
	// Harmless: the object is still correct, just spilled to disk one byte
	// earlier than strictly necessary at that exact boundary.
	n1, err := io.CopyN(&buf, src, threshold)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if errors.Is(err, io.EOF) {
		// Fewer than `threshold` bytes total: fits entirely in memory.
		return &workspaceSpool{mem: buf.Bytes(), size: n1}, nil
	}

	f, ferr := os.CreateTemp("", "fission-agentruntime-workspace-*")
	if ferr != nil {
		return nil, ferr
	}
	defer f.Close()
	if _, werr := f.Write(buf.Bytes()); werr != nil {
		_ = os.Remove(f.Name())
		return nil, werr
	}
	n2, cerr := io.Copy(f, src)
	if cerr != nil {
		_ = os.Remove(f.Name())
		return nil, cerr
	}
	return &workspaceSpool{fileName: f.Name(), size: n1 + n2}, nil
}

// reader returns a FRESH, independent reader over the spooled bytes — safe
// to call more than once (once for the outgoing request's Body, again from
// its GetBody closure for hmacauth.Signer's streaming-hash pass). The
// file-backed case opens its OWN *os.File per call (os.Open, not a shared
// handle rewound with Seek): sharing one handle would let the hash pass's
// read consume the same file position the transport later streams from,
// silently truncating the wire body to empty after the hash was computed.
func (s *workspaceSpool) reader() (io.ReadCloser, error) {
	if s.fileName == "" {
		return io.NopCloser(bytes.NewReader(s.mem)), nil
	}
	return os.Open(s.fileName)
}

// cleanup removes the spool's temp file, if any. Idempotent; safe to defer
// immediately after a successful spoolWorkspaceBody call.
func (s *workspaceSpool) cleanup() {
	if s.fileName == "" {
		return
	}
	_ = os.Remove(s.fileName)
	s.fileName = ""
}

// errWorkspaceNotFound is returned by workspaceClient.get for a storagesvc
// 404.
var errWorkspaceNotFound = errors.New("agentruntime: workspace artifact not found")

// workspaceClient is a SMALL internal HTTP client to storagesvc's
// /v1/workspace surface, built here rather than extending
// pkg/storagesvc/client (whose ClientInterface targets /v1/archive's
// multipart-upload, server-minted-id shape — a poor fit for a raw-body,
// caller-chosen-id, machine-to-machine PUT/GET/DELETE/LIST). It mirrors the
// dispatcher's own internal-client construction pattern (main.go: otelhttp
// transport, hmacauth.ServiceSigner over the master secret) rather than
// reusing storagesvc/client's exported constructors, which are shaped for
// the archive upload/download contract.
type workspaceClient struct {
	baseURL string
	http    *http.Client
}

// newWorkspaceClient returns a workspaceClient posting to baseURL (e.g.
// "http://storagesvc.fission") over rt.
func newWorkspaceClient(baseURL string, rt http.RoundTripper) *workspaceClient {
	return &workspaceClient{baseURL: strings.TrimSuffix(baseURL, "/"), http: &http.Client{Transport: rt}}
}

// readLimitedBody reads up to 4KiB of r for an error message — bounded so a
// misbehaving or hostile upstream can't inflate an error log entry
// unboundedly.
func readLimitedBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(b))
}

// workspacePutStoragesvcResp mirrors storagesvc's workspacePutResponse wire
// shape ({"size": N}) — a local decode target rather than an import, since
// storagesvc's type is unexported.
type workspacePutStoragesvcResp struct {
	Size int64 `json:"size"`
}

// put streams sp's spooled body to storagesvc's PUT /v1/workspace?id=<id>,
// returning the size storagesvc reports it actually received (the ground
// truth of what got persisted, rather than trusting the caller's own count
// unconditionally — see handlePut).
func (c *workspaceClient) put(ctx context.Context, id string, sp *workspaceSpool) (int64, error) {
	first, err := sp.reader()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/v1/workspace?id="+url.QueryEscape(id), nil)
	if err != nil {
		_ = first.Close()
		return 0, err
	}
	req.Body = first
	req.ContentLength = sp.size
	// GetBody, not the request constructor's body argument: hmacauth.Signer
	// streams its hash from a FRESH GetBody() reader while the transport
	// streams the original req.Body — see this file's package doc and
	// workspaceSpool.reader's doc for why a shared handle would break this.
	req.GetBody = func() (io.ReadCloser, error) { return sp.reader() }

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("storagesvc PUT %s: status %d: %s", id, resp.StatusCode, readLimitedBody(resp.Body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	var out workspacePutStoragesvcResp
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decoding storagesvc PUT response: %w", err)
	}
	return out.Size, nil
}

// get issues GET /v1/workspace?id=<id>. The caller must Close the returned
// body. Returns errWorkspaceNotFound on a storagesvc 404. size is -1 when
// storagesvc's Content-Length header was absent or unparsable — the caller
// (handleGet) must treat that as "unknown", NOT as an actual 0-byte body:
// blindly parsing with ", _" and defaulting to 0 would make handleGet
// declare "Content-Length: 0" while still copying a non-empty stream, which
// Go's server enforces by silently truncating the response to empty. In
// contract storagesvc always sets the header (workspaceGetHandler stats
// before streaming), so -1 is a robustness path, not a live one.
func (c *workspaceClient) get(ctx context.Context, id string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/workspace?id="+url.QueryEscape(id), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, 0, errWorkspaceNotFound
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, 0, fmt.Errorf("storagesvc GET %s: status %d: %s", id, resp.StatusCode, readLimitedBody(resp.Body))
	}
	size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		size = -1
	}
	return resp.Body, size, nil
}

// del issues DELETE /v1/workspace?id=<id>. Idempotent on the storagesvc
// side (a missing object is a 200, not a 404 — see pkg/storagesvc/
// workspace.go's workspaceDeleteHandler).
func (c *workspaceClient) del(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/workspace?id="+url.QueryEscape(id), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("storagesvc DELETE %s: status %d: %s", id, resp.StatusCode, readLimitedBody(resp.Body))
	}
	return nil
}

// workspaceDeletePrefixStoragesvcResp mirrors storagesvc's
// workspaceDeletePrefixResponse wire shape ({"deleted": N}).
type workspaceDeletePrefixStoragesvcResp struct {
	Deleted int `json:"deleted"`
}

// deletePrefix issues DELETE /v1/workspace/prefix?prefix=<prefix> — the
// idempotent bulk-delete primitive the sweeper's archive-time workspace
// purge (sweeper.go, via the workspacePurger interface) is built on. Same
// signer/transport as every other workspaceClient method (constructed once in
// newWorkspaceClient); nothing route-specific here. The returned count is
// storagesvc's own report of how many objects it actually removed — logged by
// the caller at V(1), not used for any correctness decision: storagesvc's own
// per-item delete failures are already log-don't-retry on its side (see
// pkg/storagesvc/workspace.go's workspaceDeletePrefixHandler doc), so
// `deleted == N` here is informational, never a completeness guarantee.
func (c *workspaceClient) deletePrefix(ctx context.Context, prefix string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/workspace/prefix?prefix="+url.QueryEscape(prefix), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("storagesvc DELETE prefix %s: status %d: %s", prefix, resp.StatusCode, readLimitedBody(resp.Body))
	}
	// Same over-read-detection shape as workspaceClient.list: a response past
	// the cap is treated as an error, not silently truncated-and-parsed.
	body, err := io.ReadAll(io.LimitReader(resp.Body, workspaceListMaxResponseBytes+1))
	if err != nil {
		return 0, err
	}
	if int64(len(body)) > workspaceListMaxResponseBytes {
		return 0, fmt.Errorf("storagesvc DELETE prefix %s: response exceeds the %d byte cap", prefix, workspaceListMaxResponseBytes)
	}
	var out workspaceDeletePrefixStoragesvcResp
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decoding storagesvc DELETE prefix response: %w", err)
	}
	return out.Deleted, nil
}

// workspaceListStoragesvcItem/Resp mirror storagesvc's workspaceItem/
// workspaceListResponse wire shapes.
type workspaceListStoragesvcItem struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}
type workspaceListStoragesvcResp struct {
	Items []workspaceListStoragesvcItem `json:"items"`
}

// list issues GET /v1/workspace/list?prefix=<prefix>. The response body is
// read through workspaceListMaxResponseBytes — see that constant's doc for
// why an unbounded io.ReadAll here would be a memory-amplification vector,
// not merely a slow one.
func (c *workspaceClient) list(ctx context.Context, prefix string) ([]workspaceListStoragesvcItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/workspace/list?prefix="+url.QueryEscape(prefix), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("storagesvc LIST %s: status %d: %s", prefix, resp.StatusCode, readLimitedBody(resp.Body))
	}
	// Read one byte past the cap: landing exactly at cap+1 (rather than a
	// silently truncated cap bytes that json.Unmarshal might still happen to
	// parse as valid-but-wrong JSON) means the response really did exceed
	// the bound, mirroring the PUT-side over-read-detection shape
	// (spoolWorkspaceBody's LimitReader(cap+1)) rather than the truncate-
	// and-hope alternative.
	body, err := io.ReadAll(io.LimitReader(resp.Body, workspaceListMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > workspaceListMaxResponseBytes {
		return nil, fmt.Errorf("storagesvc LIST %s: response exceeds the %d byte cap", prefix, workspaceListMaxResponseBytes)
	}
	var out workspaceListStoragesvcResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding storagesvc LIST response: %w", err)
	}
	return out.Items, nil
}

// WorkspaceHandler serves the session workspace routes. See this file's
// package doc for the full security model.
type WorkspaceHandler struct {
	logger    logr.Logger
	view      *AgentView
	store     *SessionStore
	client    *workspaceClient // nil when STORAGESVC_URL is unset: every route answers 503.
	retention time.Duration

	maxArtifactBytes        int64
	maxSessionArtifactBytes int64 // 0 means unlimited.
	maxSessionArtifacts     int64 // 0 means unlimited.
}

// NewWorkspaceHandler returns a WorkspaceHandler. client nil disables the
// feature (every route answers 503 rather than agentruntime guessing a
// storagesvc URL — see main.go's STORAGESVC_URL handling). retention is the
// same archive-retention duration NewDispatcher receives, used identically
// to compute a settled record's refreshed live-key TTL (see liveTTL).
// maxSessionArtifacts bounds the number of DISTINCT artifact paths a session
// may hold (0 = unlimited) — the count-side counterpart to
// maxSessionArtifactBytes, closing the zero-byte-artifact bypass a
// bytes-only budget leaves open (an unbounded number of 0-byte PUTs always
// passes a bytes check) and structurally bounding how large a single LIST
// response for the session can ever legitimately be.
func NewWorkspaceHandler(logger logr.Logger, view *AgentView, store *SessionStore, client *workspaceClient, retention time.Duration, maxArtifactBytes, maxSessionArtifactBytes, maxSessionArtifacts int64) *WorkspaceHandler {
	return &WorkspaceHandler{
		logger: logger, view: view, store: store, client: client, retention: retention,
		maxArtifactBytes: maxArtifactBytes, maxSessionArtifactBytes: maxSessionArtifactBytes,
		maxSessionArtifacts: maxSessionArtifacts,
	}
}

// liveTTL computes the same live-key TTL Dispatcher's own handler uses
// (entry.IdleAfter + entry.ArchiveAfter + retention — dispatcher.go:491),
// so a workspace-triggered counter settle refreshes the record's orphan
// self-expiry by the SAME lease every turn does, rather than shortening it.
// Falls back to the platform defaults when the AgentEntry is no longer in
// the view (the Function was deleted after the session was created but
// before its record was archived/swept) — TTL=0 would mean "no expiry" to
// statestore.KVStore.Set, which would strip the orphan-expiry safety net
// entirely, so a conservative non-zero fallback is required, never zero.
func (wh *WorkspaceHandler) liveTTL(ns, agent string) time.Duration {
	if e, ok := wh.view.Lookup(ns, agent); ok {
		return e.IdleAfter + e.ArchiveAfter + wh.retention
	}
	return DefaultIdleAfter + DefaultArchiveAfter + wh.retention
}

// requireSession is the existence anchor every handler runs BEFORE any
// storagesvc call — see this file's package doc, point 4.
func (wh *WorkspaceHandler) requireSession(w http.ResponseWriter, r *http.Request, ns, agent, session string) (SessionRecord, bool) {
	rec, _, err := wh.store.Get(r.Context(), ns, agent, session)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "session not found")
			return SessionRecord{}, false
		}
		wh.logger.Error(err, "getting session record", "namespace", ns, "agent", agent, "session", session)
		writeErr(w, http.StatusInternalServerError, "getting session")
		return SessionRecord{}, false
	}
	return rec, true
}

// probeExisting looks up id's CURRENT size (and whether it exists at all)
// via a LIST scoped to the session prefix, filtered to the exact id — the
// only sizing primitive the frozen storagesvc wire contract offers (no
// HEAD/info route). Both handlePut (to settle an overwrite by DELTA rather
// than double-counting the write) and handleDelete (to know how much to
// decrement) share this lookup.
//
// A LIST failure is logged at V(1) and treated as "does not exist"
// (size 0, found false) — the accepted-drift fallback: handlePut then
// settles as a plain create and handleDelete simply skips its counter
// decrement (documented at its own call site). Neither caller-visible
// outcome (the PUT/DELETE still succeeds) depends on the probe succeeding —
// only the bookkeeping does.
//
// Every caller-visible fallback of a stale or failed probe — a LIST outage
// read as "does not exist", or a raced probe whose oldSize is stale by
// settle time — is engineered to be fail-CLOSED: it only ever OVER-counts
// the tracked budget relative to true storage, never under-counts it. That
// is enforced at the CALL sites, not by this function alone: handlePut
// clamps its byte delta to a non-negative contribution (max(0, new-old)) so
// a stale/negative delta from a same-path shrink race can never credit
// budget back, and a spuriously "not found" probe only ever causes an
// extra ArtifactCount++ (a permanent but bounded phantom), never a missed
// decrement that would let real usage outrun the tracked total. See
// handlePut's doc comment for the specific race shapes this closes.
func (wh *WorkspaceHandler) probeExisting(ctx context.Context, ns, agent, session, id string) (size int64, found bool) {
	items, err := wh.client.list(ctx, workspaceSessionPrefix(ns, agent, session))
	if err != nil {
		wh.logger.V(1).Info("probing existing workspace object failed; treating as not-yet-existing", "id", id, "namespace", ns, "agent", agent, "session", session, "error", err.Error())
		return 0, false
	}
	for _, it := range items {
		if it.ID == id {
			return it.Size, true
		}
	}
	return 0, false
}

// workspaceDisabledMsg is the 503 body every route answers with when
// STORAGESVC_URL is unset — see main.go's envStoragesvcURL handling.
const workspaceDisabledMsg = "workspace disabled: STORAGESVC_URL not configured"

// workspacePutResp is the wire shape of a successful PUT.
type workspacePutResp struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// handlePut serves PUT /workspace/{namespace}/{name}/{session}/{path...}.
//
// Delta accounting: storagesvc's PUT is an OVERWRITE-in-place (Task 1's
// wire contract has no separate create/replace verb), so unconditionally
// adding the new size on every PUT would turn the per-session byte budget
// into a cumulative-WRITE budget rather than a stored-bytes one — a session
// that legitimately rewrites one 32MiB file 8 times would hit the default
// 256MiB budget and 429-lock, and a later DELETE would only decrement by the
// object's CURRENT size, leaving permanent phantom residue (a nonzero
// counter backed by zero live objects). probeExisting looks up the prior
// size (and whether the path existed at all) BEFORE the write; both the
// pre-check and the post-write settle use that delta — oldSize subtracted,
// newSize added, ArtifactCount incremented only when the path did not
// already exist. A rewrite of an unchanged-size file nets to a zero-byte
// delta, exactly matching what actually changed on storagesvc.
//
// The delta is CLAMPED to a minimum of zero (max(0, newSize-oldSize)), both
// in the pre-check and the settle — a shrinking overwrite of the SAME path
// (newSize < oldSize) contributes NO byte-budget credit, ever, rather than
// crediting the difference back. This is deliberately fail-CLOSED, not an
// oversight: crediting a negative delta is exactly what let a fix-round
// review (R2) turn a race into a genuine quota-bypass shape — two PUTs
// racing the SAME shrinking path both probe the SAME stale oldSize and both
// settle a negative delta against a fresh read, so the tracked total drops
// by (N-1)x(oldSize-newSize) more than the real storage did; repeated
// shrink/regrow cycles could otherwise manufacture unbounded byte-budget
// headroom bounded only by maxSessionArtifacts*maxArtifactBytes rather than
// maxSessionArtifactBytes. Clamping to zero makes every settle either exact
// or an OVER-count relative to true storage, never an under-count — the
// safe direction for a resource-exhaustion backstop, at the cost that a
// same-path shrink alone never reclaims budget. A caller that needs the
// budget back must actually DELETE the path (which decrements by the real
// stored size, see handleDelete) rather than overwriting it smaller.
//
// Quota ordering (Global Constraints): pre-check the per-session budgets
// (bytes AND count — see maxSessionArtifacts) -> write to storagesvc ->
// CAS-settle the counters via casUpdateFresh. The settle closure is
// func(*SessionRecord) with no error return, so the budget checks CANNOT
// live inside it — they must happen before the write, as a pre-check. That
// ordering leaves two accepted, fail-closed overshoot races, neither bounded
// to "one artifact's worth":
//   - N PUTs racing a NEW path can all read the pre-settle (not-yet-exceeded)
//     budget and all write before any settle lands, overshooting BYTES by up
//     to (N-1)*maxArtifactBytes — there is no per-session in-flight-write
//     semaphore bounding N itself.
//   - N PUTs racing to CREATE the SAME not-yet-existing path can all
//     probeExisting before any of their writes land, all observe
//     existed=false, and all settle ArtifactCount++ — the final COUNT is
//     permanently inflated by (N-1) with no self-correction (one subsequent
//     DELETE only decrements by 1). This is the same failure family the
//     probe-failure fallback already accepts elsewhere (a LIST outage on an
//     existing path also yields existed=false and an identical permanent,
//     fail-closed phantom +1) — a stale "does not exist yet" read, whatever
//     its cause, always over-counts, never under-counts.
//
// Both mirror the G19 honesty notes on other best-effort session meters; a
// per-session in-flight cap or per-path settle serialization is a follow-up,
// not built here.
func (wh *WorkspaceHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	if wh.client == nil {
		writeErr(w, http.StatusServiceUnavailable, workspaceDisabledMsg)
		return
	}
	ns, agent, session := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("session")
	if !validateWorkspaceRouteIdentity(w, ns, agent, session) {
		return
	}
	path := r.PathValue("path")
	if err := validateWorkspacePath(path); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	rec, ok := wh.requireSession(w, r, ns, agent, session)
	if !ok {
		return
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	defer body.Close()

	// LimitReader(cap+1): reading exactly cap+1 bytes means the body
	// exceeded the cap. A plain LimitReader(cap) would silently accept and
	// spool a TRUNCATED artifact instead of rejecting the request outright
	// — that silent-truncation shape is exactly the bug this over-read
	// detection exists to prevent (Global Constraints).
	sp, err := spoolWorkspaceBody(io.LimitReader(body, wh.maxArtifactBytes+1), workspaceSpoolThresholdBytes)
	if err != nil {
		wh.logger.Error(err, "spooling workspace PUT body", "namespace", ns, "agent", agent, "session", session, "path", path)
		writeErr(w, http.StatusInternalServerError, "reading request body")
		return
	}
	defer sp.cleanup()

	if sp.size > wh.maxArtifactBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("artifact exceeds the %d byte cap", wh.maxArtifactBytes))
		return
	}

	id := workspaceID(ns, agent, session, path)
	oldSize, existed := wh.probeExisting(r.Context(), ns, agent, session, id)

	// Clamped to zero — see this function's doc comment: a shrinking
	// overwrite must never be credited as freed budget, only an actual
	// DELETE does that. The pre-check uses the SAME clamped estimate the
	// settle below will apply, so a request that will not actually reduce
	// the tracked total is never pre-approved on the assumption that it will.
	estimatedDelta := max(0, sp.size-oldSize)
	if wh.maxSessionArtifactBytes > 0 && rec.Stats.ArtifactBytes+estimatedDelta > wh.maxSessionArtifactBytes {
		writeErr(w, http.StatusTooManyRequests, "session artifact byte budget exceeded")
		return
	}
	// Zero-byte artifacts (and any artifact that shrinks the byte delta to
	// ~0) still consume ONE slot of this budget when the path is new — the
	// count check is what closes the bypass a bytes-only budget leaves open
	// (an unbounded number of 0-byte PUTs always satisfies a bytes check).
	if !existed && wh.maxSessionArtifacts > 0 && rec.Stats.ArtifactCount+1 > wh.maxSessionArtifacts {
		writeErr(w, http.StatusTooManyRequests, "session artifact count budget exceeded")
		return
	}

	size, err := wh.client.put(r.Context(), id, sp)
	if err != nil {
		wh.logger.Error(err, "writing workspace object", "namespace", ns, "agent", agent, "session", session, "path", path)
		writeErr(w, http.StatusBadGateway, "writing workspace object")
		return
	}

	casUpdateFresh(r.Context(), wh.logger, wh.store, ns, agent, session, wh.liveTTL(ns, agent), func(rec *SessionRecord) {
		if !existed {
			rec.Stats.ArtifactCount++
		}
		// max(0, size-oldSize), NOT size-oldSize: the delta itself is
		// clamped to a non-negative contribution BEFORE it is added — see
		// this function's doc comment for why a raw (possibly negative)
		// delta here is a genuine quota-bypass shape under a same-path
		// shrink race, not merely a cosmetic accounting choice. The
		// outer max(0, ...) around the whole addition is a second,
		// independent safety net against the total itself ever going
		// negative (e.g. from otherwise-unanticipated drift) — both clamps
		// are load-bearing, neither subsumes the other.
		rec.Stats.ArtifactBytes = max(0, rec.Stats.ArtifactBytes+max(0, size-oldSize))
	})

	writeJSON(w, http.StatusOK, workspacePutResp{Path: path, Size: size})
}

// handleGet serves GET /workspace/{namespace}/{name}/{session}/{path...}: an
// io.Copy passthrough stream with Content-Length set from storagesvc's own
// response — no buffering, no signing needed on this hop (GET carries no
// body to sign).
func (wh *WorkspaceHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if wh.client == nil {
		writeErr(w, http.StatusServiceUnavailable, workspaceDisabledMsg)
		return
	}
	ns, agent, session := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("session")
	if !validateWorkspaceRouteIdentity(w, ns, agent, session) {
		return
	}
	path := r.PathValue("path")
	if err := validateWorkspacePath(path); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := wh.requireSession(w, r, ns, agent, session); !ok {
		return
	}

	id := workspaceID(ns, agent, session, path)
	body, size, err := wh.client.get(r.Context(), id)
	if err != nil {
		if errors.Is(err, errWorkspaceNotFound) {
			writeErr(w, http.StatusNotFound, "artifact not found")
			return
		}
		wh.logger.Error(err, "reading workspace object", "namespace", ns, "agent", agent, "session", session, "path", path)
		writeErr(w, http.StatusBadGateway, "reading workspace object")
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	// size == -1 means storagesvc's Content-Length was absent/unparsable
	// (workspaceClient.get's doc) — omit the header rather than declaring a
	// wrong length: Go's server enforces whatever Content-Length it is told,
	// so writing "0" here while still copying a non-empty body would
	// silently truncate the response to empty. Omitting it instead falls
	// back to chunked transfer encoding, which is always correct.
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if _, err := io.Copy(w, body); err != nil {
		wh.logger.Error(err, "streaming workspace object", "namespace", ns, "agent", agent, "session", session, "path", path)
	}
}

// workspaceDeleteResp is the wire shape of a successful DELETE.
type workspaceDeleteResp struct {
	Path string `json:"path"`
}

// handleDelete serves DELETE /workspace/{namespace}/{name}/{session}/{path...}.
// Idempotent (a missing artifact is still a 200, matching storagesvc's own
// DELETE contract). The byte size needed to decrement Stats.ArtifactBytes
// comes from probeExisting — the frozen storagesvc wire contract has no
// HEAD/info route, so this is a LIST scoped to the session prefix, filtered
// to the exact id (the cheapest primitive the contract offers; a GET-then-
// discard would transfer the whole artifact just to read a header). That
// list is O(N) in the session's live artifact count per delete — O(N^2) for
// a client tearing a session down file-by-file — but N is now structurally
// bounded by maxSessionArtifacts (handlePut's count budget), and this same
// call is bounded in RESPONSE SIZE by workspaceListMaxResponseBytes, so
// neither the per-call cost nor the memory it buffers is unbounded. A probe
// failure is treated as "does not exist" (see probeExisting's doc): the
// delete itself still proceeds and still returns 200 (idempotent-delete is
// the contract), it only means the counter settle below is skipped —
// accepted drift, the same "residue accepted" stance the purge/prefix-delete
// paths take elsewhere in this feature.
func (wh *WorkspaceHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if wh.client == nil {
		writeErr(w, http.StatusServiceUnavailable, workspaceDisabledMsg)
		return
	}
	ns, agent, session := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("session")
	if !validateWorkspaceRouteIdentity(w, ns, agent, session) {
		return
	}
	path := r.PathValue("path")
	if err := validateWorkspacePath(path); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := wh.requireSession(w, r, ns, agent, session); !ok {
		return
	}

	id := workspaceID(ns, agent, session, path)
	size, found := wh.probeExisting(r.Context(), ns, agent, session, id)

	if err := wh.client.del(r.Context(), id); err != nil {
		wh.logger.Error(err, "deleting workspace object", "namespace", ns, "agent", agent, "session", session, "path", path)
		writeErr(w, http.StatusBadGateway, "deleting workspace object")
		return
	}

	if found {
		// Clamped at 0: a settle racing a concurrent delete of the same
		// path, or drift from a prior failed-list skip, must never drive
		// the counters negative.
		casUpdateFresh(r.Context(), wh.logger, wh.store, ns, agent, session, wh.liveTTL(ns, agent), func(rec *SessionRecord) {
			rec.Stats.ArtifactCount = max(0, rec.Stats.ArtifactCount-1)
			rec.Stats.ArtifactBytes = max(0, rec.Stats.ArtifactBytes-size)
		})
	}

	writeJSON(w, http.StatusOK, workspaceDeleteResp{Path: path})
}

// workspaceListItemResp is one entry in handleList's response.
type workspaceListItemResp struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// workspaceListResp is the wire shape of handleList's response.
type workspaceListResp struct {
	Items []workspaceListItemResp `json:"items"`
}

// handleList serves GET /workspace/{namespace}/{name}/{session} — every
// artifact currently stored under the session, with sizes.
func (wh *WorkspaceHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if wh.client == nil {
		writeErr(w, http.StatusServiceUnavailable, workspaceDisabledMsg)
		return
	}
	ns, agent, session := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("session")
	if !validateWorkspaceRouteIdentity(w, ns, agent, session) {
		return
	}
	if _, ok := wh.requireSession(w, r, ns, agent, session); !ok {
		return
	}

	prefix := workspaceSessionPrefix(ns, agent, session)
	items, err := wh.client.list(r.Context(), prefix)
	if err != nil {
		wh.logger.Error(err, "listing workspace session", "namespace", ns, "agent", agent, "session", session)
		writeErr(w, http.StatusBadGateway, "listing workspace")
		return
	}

	out := make([]workspaceListItemResp, 0, len(items))
	for _, it := range items {
		path, ok := stripWorkspacePrefix(it.ID, ns, agent, session)
		if !ok || path == "" {
			wh.logger.Error(nil, "workspace list item outside expected session prefix; skipping", "id", it.ID, "namespace", ns, "agent", agent, "session", session)
			continue
		}
		out = append(out, workspaceListItemResp{Path: path, Size: it.Size})
	}
	writeJSON(w, http.StatusOK, workspaceListResp{Items: out})
}

// mountWorkspaceRoutes registers the four session workspace routes on mux,
// each wrapped by auth (main.go passes
// IdentityOrJWTOwnWorkspace(identityVerifier, authz.Middleware)).
//
// Factored out of main.go so workspace_test.go exercises the EXACT same
// pattern registration production uses on a real *http.ServeMux — the
// trailing-slash-is-400/encoded-segment/list-vs-file behavior these routes
// pin depends on Go's actual ServeMux pattern-matching semantics, which a
// hand-built request with r.SetPathValue set directly would bypass.
func mountWorkspaceRoutes(mux *http.ServeMux, wh *WorkspaceHandler, auth func(http.Handler) http.Handler) {
	mux.Handle("PUT /workspace/{namespace}/{name}/{session}/{path...}", auth(http.HandlerFunc(wh.handlePut)))
	mux.Handle("GET /workspace/{namespace}/{name}/{session}/{path...}", auth(http.HandlerFunc(wh.handleGet)))
	mux.Handle("DELETE /workspace/{namespace}/{name}/{session}/{path...}", auth(http.HandlerFunc(wh.handleDelete)))
	mux.Handle("GET /workspace/{namespace}/{name}/{session}", auth(http.HandlerFunc(wh.handleList)))
}
