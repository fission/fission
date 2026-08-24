// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// dispatcher.go implements Dispatcher, the request-path component that turns
// an inbound "/agents/{namespace}/{name}" turn into a session-scoped call to
// the router-internal listener: it resolves the calling AgentEntry from
// AgentView, mints or validates a session id, bookkeeps the SessionRecord in
// SessionStore, forwards the turn, and streams the function's response back
// incrementally.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/uuid"
)

const (
	// HeaderSession is the response header carrying the resolved session id —
	// the single source of truth for which session a turn landed on, whether
	// the id was supplied by the caller or minted here.
	HeaderSession = fv1.DefaultAgentSessionHeader
	// HeaderYield is the function-response header a turn uses to signal it is
	// done producing output for this session (as opposed to merely finishing
	// one turn while more are expected soon).
	HeaderYield = "X-Fission-Agent-Yield"
	// YieldWaiting is the HeaderYield value that flips a session to idle
	// immediately, rather than waiting out its normal IdleAfter window.
	YieldWaiting = "waiting"

	// maxTurnBodyBytes caps a buffered turn request body. The HMAC
	// ServiceSigner binds the signature to a body hash (pkg/auth/hmac/hmac.go),
	// so the request body MUST be fully buffered (and replayable via GetBody)
	// before signing — a streamed client body cannot be signed. 4 MiB,
	// matching the workflow invoker's maxResultBytes; 413 above it.
	maxTurnBodyBytes = 4 << 20

	// streamChunkBytes is the read/write/flush buffer size used to relay the
	// upstream response incrementally, replicating the router's own
	// ReverseProxy FlushInterval=-1 streaming behavior (functionHandler.go).
	streamChunkBytes = 32 << 10
)

// sessionIDPattern bounds a caller-supplied session id: it becomes a
// statestore key and a URL/header value, so it is restricted to a safe,
// portable character set.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Dispatcher serves inbound agent turns.
type Dispatcher struct {
	logger    logr.Logger
	view      *AgentView
	store     *SessionStore
	client    *http.Client // transport pre-wrapped with the HMAC signer
	routerURL string
	retention time.Duration
	now       func() time.Time
}

// NewDispatcher returns a Dispatcher. retention is added to an entry's
// IdleAfter+ArchiveAfter to compute the live-key TTL passed to
// SessionStore.Create/Update (orphan self-expiry covers idle + archive-wait +
// archived-copy retention in one live-key lease).
func NewDispatcher(logger logr.Logger, view *AgentView, store *SessionStore, client *http.Client, routerURL string, retention time.Duration, now func() time.Time) *Dispatcher {
	return &Dispatcher{
		logger:    logger,
		view:      view,
		store:     store,
		client:    client,
		routerURL: routerURL,
		retention: retention,
		now:       now,
	}
}

// Handler serves POST /agents/{namespace}/{name}; other methods get 405 (the
// Go 1.22+ ServeMux does this automatically for a method-qualified pattern).
func (d *Dispatcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{namespace}/{name}", d.dispatch)
	return mux
}

func (d *Dispatcher) dispatch(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	// Step 1: resolve the agent entry.
	entry, ok := d.view.Lookup(ns, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not an agent function")
		return
	}

	// Step 2: resolve (or reject, or mint) the session id. The response
	// header is set as soon as the id resolves — it must precede any
	// WriteHeader call below.
	sessionID, ok := resolveSessionID(r, entry)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	w.Header().Set(HeaderSession, sessionID)

	ctx := r.Context()
	// bgCtx carries no cancellation from the client connection: the
	// post-response bookkeeping (and the transport-failure bookkeeping below)
	// must still land even if the caller has already gone away, since it is
	// what keeps Stats/Status honest and self-heals nothing on its own.
	bgCtx := context.WithoutCancel(ctx)
	liveTTL := entry.IdleAfter + entry.ArchiveAfter + d.retention
	now := d.now()

	// Read + validate the body BEFORE any session record write: an oversized
	// or unreadable request must never create or refresh a session (a bad
	// request is a client-side problem, so a read failure here is a 400, not
	// a 502 — nothing upstream has been touched yet).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTurnBodyBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "reading request body")
		return
	}
	if len(body) > maxTurnBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	// Step 3: bookkeep the session record. A registry write is never a
	// request gate, EXCEPT session quota on Create.
	rec, version, err := d.store.Get(ctx, ns, name, sessionID)
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		newRec := SessionRecord{
			ID:           sessionID,
			Agent:        name,
			Namespace:    ns,
			Status:       StatusActive,
			CreatedAt:    now,
			LastActiveAt: now,
		}
		switch cerr := d.store.Create(ctx, newRec, entry.MaxSessions, liveTTL); {
		case cerr == nil:
			// created.
		case errors.Is(cerr, ErrSessionQuota):
			writeErr(w, http.StatusTooManyRequests, "session quota exceeded")
			return
		case errors.Is(cerr, ErrSessionExists):
			// Another replica (or a concurrent turn) created the same id
			// between our Get miss and this Create — a lost race, not a
			// failure. Fall through to the normal activation path instead of
			// silently dropping this turn's bookkeeping.
			d.logger.Info("session created concurrently, activating existing record", "namespace", ns, "agent", name, "session", sessionID)
			d.casUpdateFresh(ctx, ns, name, sessionID, liveTTL, func(rec *SessionRecord) {
				rec.Status = StatusActive
				rec.LastActiveAt = now
			})
		default:
			d.logger.Error(cerr, "creating session record", "namespace", ns, "agent", name, "session", sessionID)
		}
	case err != nil:
		d.logger.Error(err, "getting session record", "namespace", ns, "agent", name, "session", sessionID)
	default:
		d.casUpdate(ctx, ns, name, sessionID, rec, version, liveTTL, func(rec *SessionRecord) {
			rec.Status = StatusActive
			rec.LastActiveAt = now
		})
	}

	// Step 4: forward the turn.
	upstream := d.routerURL + utils.UrlForFunction(name, ns)
	if entry.SessionSource == fv1.SessionSourceQueryParam {
		upstream += "?" + url.Values{entry.SessionName: {sessionID}}.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "building upstream request")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set(HeaderSession, sessionID)
	if entry.SessionSource == fv1.SessionSourceHeader {
		req.Header.Set(entry.SessionName, sessionID)
	}

	start := d.now()
	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Error(err, "calling upstream function", "namespace", ns, "agent", name, "session", sessionID)
		// A failed turn is still a turn: meter it (Errors included) using
		// bgCtx so the write is not lost if the caller has already gone.
		d.casUpdateFresh(bgCtx, ns, name, sessionID, liveTTL, func(rec *SessionRecord) {
			rec.Stats.Turns++
			rec.Stats.Errors++
			rec.Status = StatusActive
			rec.LastActiveAt = d.now()
		})
		writeErr(w, http.StatusBadGateway, "calling upstream function")
		return
	}
	defer resp.Body.Close()

	// The yield outcome is surfaced to the caller (not just consumed for our
	// own bookkeeping below) so callers can observe done-vs-waiting.
	yield := resp.Header.Get(HeaderYield)
	if yield != "" {
		w.Header().Set(HeaderYield, yield)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	streamResponse(w, resp.Body)
	elapsed := d.now().Sub(start)

	// Step 5: post-response bookkeeping. Reads a fresh record — never the
	// version held from step 3 — because the upstream call above may have
	// taken long enough for another turn to have CAS-written the record
	// underneath it. Uses bgCtx so a client disconnect during/after the
	// stream does not lose this turn's meter.
	d.casUpdateFresh(bgCtx, ns, name, sessionID, liveTTL, func(rec *SessionRecord) {
		rec.Stats.Turns++
		rec.Stats.ActiveMillis += elapsed.Milliseconds()
		if resp.StatusCode >= http.StatusInternalServerError {
			rec.Stats.Errors++
		}
		// LastActiveAt refreshes on BOTH branches: a waiting->idle turn still
		// just happened just now, it is only the session's next-turn clock
		// (IdleAfter/ArchiveAfter) that starts running from it.
		rec.LastActiveAt = d.now()
		if yield == YieldWaiting {
			rec.Status = StatusIdle
		} else {
			rec.Status = StatusActive
		}
	})
}

// resolveSessionID extracts the session id from r per entry's configured
// source, minting one when absent. It reports false when a caller-supplied
// id fails sessionIDPattern.
func resolveSessionID(r *http.Request, entry AgentEntry) (string, bool) {
	var raw string
	if entry.SessionSource == fv1.SessionSourceQueryParam {
		raw = r.URL.Query().Get(entry.SessionName)
	} else {
		raw = r.Header.Get(entry.SessionName)
	}
	if raw == "" {
		return uuid.NewString(), true
	}
	if !sessionIDPattern.MatchString(raw) {
		return "", false
	}
	return raw, true
}

// streamResponse relays src to w incrementally: a read/write loop with a
// streamChunkBytes buffer, flushing after every write. Plain io.Copy buffers
// until completion; this replicates the router's own ReverseProxy
// FlushInterval=-1 streaming behavior instead.
func streamResponse(w http.ResponseWriter, src io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, streamChunkBytes)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if rerr != nil {
			return
		}
	}
}

// casUpdate mutates rec via mutate and CAS-writes it at version with
// liveTTL. On statestore.ErrVersionConflict it retries exactly once with a
// freshly fetched record and version — never the stale one. Any other
// failure, or a second conflict, is logged and swallowed: registry writes are
// bookkeeping, never a request gate.
func (d *Dispatcher) casUpdate(ctx context.Context, ns, agent, id string, rec SessionRecord, version int64, liveTTL time.Duration, mutate func(*SessionRecord)) {
	mutate(&rec)
	err := d.store.Update(ctx, rec, version, liveTTL)
	if err == nil {
		return
	}
	if !errors.Is(err, statestore.ErrVersionConflict) {
		d.logger.Error(err, "updating session record", "namespace", ns, "agent", agent, "session", id)
		return
	}

	fresh, freshVersion, gerr := d.store.Get(ctx, ns, agent, id)
	if gerr != nil {
		d.logger.Error(gerr, "re-getting session record after conflict", "namespace", ns, "agent", agent, "session", id)
		return
	}
	mutate(&fresh)
	if uerr := d.store.Update(ctx, fresh, freshVersion, liveTTL); uerr != nil {
		d.logger.Error(uerr, "updating session record after retry", "namespace", ns, "agent", agent, "session", id)
	}
}

// casUpdateFresh re-Gets the record immediately before the CAS write, then
// delegates to casUpdate. Used whenever an unbounded amount of work (an
// upstream call) may have happened since any previously held version was
// read.
func (d *Dispatcher) casUpdateFresh(ctx context.Context, ns, agent, id string, liveTTL time.Duration, mutate func(*SessionRecord)) {
	rec, version, err := d.store.Get(ctx, ns, agent, id)
	if err != nil {
		d.logger.Error(err, "getting session record for update", "namespace", ns, "agent", agent, "session", id)
		return
	}
	d.casUpdate(ctx, ns, agent, id, rec, version, liveTTL, mutate)
}

// writeErr answers with a small {"error": msg} JSON object at status.
func writeErr(w http.ResponseWriter, status int, msg string) {
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
