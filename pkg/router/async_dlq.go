// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
)

// RFC-0024 async dead-letter-queue admin API. It lives on the PUBLIC listener so
// it is gated by the same JWT authMiddleware as the auth-login endpoint (these
// paths are deliberately NOT in auth.go's exemption list) — a coarse operator
// gate; per-namespace scoping of the JWT is a follow-up. When async invocation is
// disabled the handlers return 501 rather than 404, so the surface is
// discoverable. All operate on the single global asyncinvoke.DefaultQueue.
const (
	dlqPathList    = "/v1/async/dlq/list"
	dlqPathShow    = "/v1/async/dlq/show"
	dlqPathRedrive = "/v1/async/dlq/redrive"
	dlqPathPurge   = "/v1/async/dlq/purge"

	// dlqDefaultLimit / dlqMaxLimit bound a list page; dlqShowScanCap bounds the
	// scan a show performs looking for one id (the Queue has no get-by-id), so a
	// large dead set cannot turn a single show into unbounded work.
	dlqDefaultLimit = 100
	dlqMaxLimit     = 1000
	dlqShowScanCap  = 10000
	dlqMaxBodyBytes = 1 << 20
)

// dlqMessage is the list/show summary of one dead-lettered async invocation. The
// namespace/function are decoded from the envelope for display and filtering; a
// message whose envelope will not decode still lists (with them empty) so a
// corrupt record is visible, not hidden.
type dlqMessage struct {
	ID         string    `json:"id"`
	Namespace  string    `json:"namespace,omitempty"`
	Function   string    `json:"function,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Attempts   int       `json:"attempts"`
	EnqueuedAt time.Time `json:"enqueuedAt"`
	DiedAt     time.Time `json:"diedAt"`
}

type dlqListResp struct {
	Messages  []dlqMessage `json:"messages"`
	NextToken string       `json:"nextToken,omitempty"`
}

type dlqShowResp struct {
	dlqMessage
	Envelope *asyncinvoke.Envelope `json:"envelope,omitempty"`
}

type dlqRedriveReq struct {
	IDs []string `json:"ids"`
}

type dlqMutateResp struct {
	Count int64 `json:"count"`
}

// registerAsyncDLQRoutes adds the DLQ admin endpoints to the public mux. Called
// from registerRouterOwnedRoutes so both the full and incremental mux builders
// register them identically.
func (ts *HTTPTriggerSet) registerAsyncDLQRoutes(public *httpmux.Mux) {
	deny := httpsecurity.DenyAllCORS
	public.Handle(dlqPathList, deny(http.HandlerFunc(ts.dlqList))).Methods(http.MethodGet, http.MethodOptions)
	public.Handle(dlqPathShow, deny(http.HandlerFunc(ts.dlqShow))).Methods(http.MethodGet, http.MethodOptions)
	public.Handle(dlqPathRedrive, deny(http.HandlerFunc(ts.dlqRedrive))).Methods(http.MethodPost, http.MethodOptions)
	public.Handle(dlqPathPurge, deny(http.HandlerFunc(ts.dlqPurge))).Methods(http.MethodPost, http.MethodOptions)
}

// dlqQueue returns the async DLQ queue, or writes 501 and returns false when async
// invocation is not enabled on this router.
func (ts *HTTPTriggerSet) dlqQueue(w http.ResponseWriter) (statestore.Queue, bool) {
	if ts.asyncInvoker == nil || !ts.asyncInvoker.enabled() {
		http.Error(w, "async invocation is not enabled on this cluster", http.StatusNotImplemented)
		return nil, false
	}
	return ts.asyncInvoker.queue, true
}

// dlqList returns a page of dead-lettered invocations, optionally filtered to one
// namespace (a display convenience, not an authorization boundary — the JWT gate
// is coarse in phase 3). ?limit bounds the page; ?token continues from a prior
// page's nextToken.
func (ts *HTTPTriggerSet) dlqList(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	limit := dlqParseLimit(r.URL.Query().Get("limit"))
	nsFilter := r.URL.Query().Get("namespace")
	dead, err := q.DeadLetters(r.Context(), asyncinvoke.DefaultQueue, statestore.Page{
		Token: r.URL.Query().Get("token"),
		Limit: limit,
	})
	if err != nil {
		ts.logger.Error(err, "listing async dead letters")
		http.Error(w, "listing dead letters", http.StatusInternalServerError)
		return
	}
	resp := dlqListResp{}
	for _, d := range dead {
		m := dlqSummary(d)
		if nsFilter != "" && m.Namespace != nsFilter {
			continue
		}
		resp.Messages = append(resp.Messages, m)
	}
	// A full page implies there may be more; the last raw id is the continuation
	// token (paging keys on the id regardless of the namespace filter).
	if len(dead) == limit && len(dead) > 0 {
		resp.NextToken = dead[len(dead)-1].ID
	}
	dlqWriteJSON(w, ts, resp)
}

// dlqShow returns the full decoded envelope for one dead-lettered invocation by
// id. The Queue has no get-by-id, so it scans the dead set (bounded by
// dlqShowScanCap) — a rare operator action on a set operators keep small.
func (ts *HTTPTriggerSet) dlqShow(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id query parameter is required", http.StatusBadRequest)
		return
	}
	token := ""
	scanned := 0
	for scanned < dlqShowScanCap {
		dead, err := q.DeadLetters(r.Context(), asyncinvoke.DefaultQueue, statestore.Page{Token: token, Limit: dlqDefaultLimit})
		if err != nil {
			ts.logger.Error(err, "reading async dead letters")
			http.Error(w, "reading dead letters", http.StatusInternalServerError)
			return
		}
		if len(dead) == 0 {
			break
		}
		for i := range dead {
			if dead[i].ID == id {
				dlqWriteShow(w, ts, dead[i])
				return
			}
		}
		scanned += len(dead)
		if len(dead) < dlqDefaultLimit {
			break
		}
		token = dead[len(dead)-1].ID
	}
	http.Error(w, "dead-lettered message not found", http.StatusNotFound)
}

// dlqRedrive re-enqueues the given dead-lettered invocations (attempts reset) so
// they are delivered again. Ids that are not currently dead are silently skipped
// by the store; Count reports the number requested.
func (ts *HTTPTriggerSet) dlqRedrive(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	var req dlqRedriveReq
	if !dlqDecodeJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 {
		http.Error(w, "ids must not be empty", http.StatusBadRequest)
		return
	}
	if err := q.Redrive(r.Context(), asyncinvoke.DefaultQueue, req.IDs); err != nil {
		ts.logger.Error(err, "redriving async dead letters")
		http.Error(w, "redriving dead letters", http.StatusInternalServerError)
		return
	}
	dlqWriteJSON(w, ts, dlqMutateResp{Count: int64(len(req.IDs))})
}

// dlqPurge permanently deletes every dead-lettered invocation and reports the
// count removed.
func (ts *HTTPTriggerSet) dlqPurge(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	n, err := q.Purge(r.Context(), asyncinvoke.DefaultQueue)
	if err != nil {
		ts.logger.Error(err, "purging async dead letters")
		http.Error(w, "purging dead letters", http.StatusInternalServerError)
		return
	}
	dlqWriteJSON(w, ts, dlqMutateResp{Count: n})
}

// dlqSummary maps a DeadMessage to the list summary, decoding the envelope for the
// namespace/function (best-effort — a corrupt record still lists).
func dlqSummary(d statestore.DeadMessage) dlqMessage {
	m := dlqMessage{
		ID:         d.ID,
		Reason:     d.Reason,
		Attempts:   d.Attempts,
		EnqueuedAt: d.EnqueuedAt,
		DiedAt:     d.DiedAt,
	}
	if env, err := asyncinvoke.Decode(d.Body); err == nil {
		m.Namespace, m.Function = env.Namespace, env.Function
	}
	return m
}

func dlqWriteShow(w http.ResponseWriter, ts *HTTPTriggerSet, d statestore.DeadMessage) {
	resp := dlqShowResp{dlqMessage: dlqSummary(d)}
	if env, err := asyncinvoke.Decode(d.Body); err == nil {
		resp.Envelope = &env
	}
	dlqWriteJSON(w, ts, resp)
}

func dlqParseLimit(raw string) int {
	if raw == "" {
		return dlqDefaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return dlqDefaultLimit
	}
	if n > dlqMaxLimit {
		return dlqMaxLimit
	}
	return n
}

func dlqDecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, dlqMaxBodyBytes))
	if err := dec.Decode(v); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func dlqWriteJSON(w http.ResponseWriter, ts *HTTPTriggerSet, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		ts.logger.Error(err, "encoding async DLQ response")
	}
}
