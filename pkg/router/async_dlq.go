// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/fission/fission/pkg/mqtrigger/mqpub"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// RFC-0024 async dead-letter-queue admin API. It lives on the INTERNAL listener
// (ClusterIP-only svc/router-internal), so every request is HMAC-verified with the
// ServiceRouterInternal key and gated by the internal NetworkPolicy allowlist —
// fail-closed by construction and independent of the public listener's optional
// JWT auth. It is NOT on the public listener, where an operator running with the
// default authentication.enabled=false would otherwise expose an unauthenticated
// cross-namespace read/redrive/purge surface. `fission function dlq` signs its
// requests with FISSION_INTERNAL_AUTH_SECRET the same way `test --async` does. When
// async invocation is disabled the handlers return 501 rather than 404, so the
// surface is discoverable. All operate on the single global asyncinvoke.DefaultQueue;
// per-namespace scoping of access is a follow-up.
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
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
	Function  string `json:"function,omitempty"`
	// Topic is set for dead-lettered broker egress jobs (?queue=mq-egress-<type>)
	// instead of Function.
	Topic      string    `json:"topic,omitempty"`
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
	// EgressJob is set instead of Envelope for a dead-lettered broker egress
	// job (?queue=mq-egress-<type>) — payload included, so the operator can
	// inspect the event that failed to publish.
	EgressJob *mqpub.EgressJob `json:"egressJob,omitempty"`
}

type dlqRedriveReq struct {
	IDs []string `json:"ids"`
}

type dlqMutateResp struct {
	Count int64 `json:"count"`
}

// registerAsyncDLQRoutes adds the DLQ admin endpoints to the INTERNAL mux, where
// the listener-level HMAC verifier + DenyAllCORS + SecurityHeaders already wrap
// every handler. Called from both the full and incremental mux builders so they
// register identically.
func (ts *HTTPTriggerSet) registerAsyncDLQRoutes(internal *httpmux.Mux) {
	internal.HandleFunc(dlqPathList, ts.dlqList).Methods(http.MethodGet)
	internal.HandleFunc(dlqPathShow, ts.dlqShow).Methods(http.MethodGet)
	internal.HandleFunc(dlqPathRedrive, ts.dlqRedrive).Methods(http.MethodPost)
	internal.HandleFunc(dlqPathPurge, ts.dlqPurge).Methods(http.MethodPost)
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

// dlqEgressQueueRegexp bounds the ?queue= value to a well-formed egress queue
// name: mq-egress- plus an MQ-type token. MQ types are lowercase alphanumeric
// with hyphens (kafka, nats-jetstream, ...), so anything outside that charset
// is a malformed request, not a queue.
var dlqEgressQueueRegexp = regexp.MustCompile(`^mq-egress-[a-z0-9-]+$`)

// dlqQueueName resolves the ?queue= parameter: empty means the async invocation
// queue; otherwise it must be an RFC-0027 broker egress queue (mq-egress-<type>).
// Allowlisted by shape, not free-form — the DLQ surface must not become a
// read/redrive/purge primitive over arbitrary statestore queues.
func dlqQueueName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("queue")
	if name == "" || name == asyncinvoke.DefaultQueue {
		return asyncinvoke.DefaultQueue, true
	}
	if dlqEgressQueueRegexp.MatchString(name) {
		return name, true
	}
	http.Error(w, "queue must be empty (async invocations) or an mq-egress-<type> egress queue", http.StatusBadRequest)
	return "", false
}

// dlqList returns a page of dead-lettered invocations, optionally filtered to one
// namespace (a display convenience, not an authorization boundary — the internal
// listener's HMAC gate is coarse in phase 3). ?limit bounds the page; ?token
// continues from a prior page's nextToken.
func (ts *HTTPTriggerSet) dlqList(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	queueName, ok := dlqQueueName(w, r)
	if !ok {
		return
	}
	limit := dlqParseLimit(r.URL.Query().Get("limit"))
	nsFilter := r.URL.Query().Get("namespace")
	dead, err := q.DeadLetters(r.Context(), queueName, statestore.Page{
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
	queueName, ok := dlqQueueName(w, r)
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
		dead, err := q.DeadLetters(r.Context(), queueName, statestore.Page{Token: token, Limit: dlqDefaultLimit})
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
	if scanned >= dlqShowScanCap {
		// Distinguish "absent" from "beyond the scan bound" so an operator does not
		// conclude a still-present message is gone.
		http.Error(w, "dead-lettered message not found within the first "+strconv.Itoa(dlqShowScanCap)+" scanned; narrow the dead set (redrive/purge) and retry", http.StatusNotFound)
		return
	}
	http.Error(w, "dead-lettered message not found", http.StatusNotFound)
}

// dlqRedrive re-enqueues the given dead-lettered invocations (attempts reset) so
// they are delivered again. Ids that are not currently dead are skipped by the
// store; Count reports the number actually re-enqueued (may be < len(ids)).
func (ts *HTTPTriggerSet) dlqRedrive(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	queueName, ok := dlqQueueName(w, r)
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
	n, err := q.Redrive(r.Context(), queueName, req.IDs)
	if err != nil {
		ts.logger.Error(err, "redriving async dead letters")
		http.Error(w, "redriving dead letters", http.StatusInternalServerError)
		return
	}
	dlqWriteJSON(w, ts, dlqMutateResp{Count: n})
}

// dlqPurge permanently deletes every dead-lettered invocation and reports the
// count removed.
func (ts *HTTPTriggerSet) dlqPurge(w http.ResponseWriter, r *http.Request) {
	q, ok := ts.dlqQueue(w)
	if !ok {
		return
	}
	queueName, ok := dlqQueueName(w, r)
	if !ok {
		return
	}
	n, err := q.Purge(r.Context(), queueName)
	if err != nil {
		ts.logger.Error(err, "purging async dead letters")
		http.Error(w, "purging dead letters", http.StatusInternalServerError)
		return
	}
	dlqWriteJSON(w, ts, dlqMutateResp{Count: n})
}

// dlqSummary maps a DeadMessage to the list summary, decoding the body for
// display fields (best-effort — a corrupt record still lists): an async
// invocation envelope yields namespace/function, a broker egress job yields
// namespace/topic.
func dlqSummary(d statestore.DeadMessage) dlqMessage {
	m := dlqMessage{
		ID:         d.ID,
		Reason:     d.Reason,
		Attempts:   d.Attempts,
		EnqueuedAt: d.EnqueuedAt,
		DiedAt:     d.DiedAt,
	}
	if env, err := asyncinvoke.Decode(d.Body); err == nil && env.Function != "" {
		m.Namespace, m.Function = env.Namespace, env.Function
		return m
	}
	var job mqpub.EgressJob
	if err := json.Unmarshal(d.Body, &job); err == nil && job.Topic != "" {
		m.Namespace, m.Topic = job.Namespace, job.Topic
	}
	return m
}

func dlqWriteShow(w http.ResponseWriter, ts *HTTPTriggerSet, d statestore.DeadMessage) {
	resp := dlqShowResp{dlqMessage: dlqSummary(d)}
	// The gates mirror dlqSummary's classification: a lenient json.Unmarshal
	// happily decodes an EgressJob body into a half-empty Envelope, so decode
	// success alone must not pick the shape.
	if env, err := asyncinvoke.Decode(d.Body); err == nil && env.Function != "" {
		resp.Envelope = &env
	} else if job := new(mqpub.EgressJob); json.Unmarshal(d.Body, job) == nil && job.Topic != "" {
		resp.EgressJob = job
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
		// Distinguish an over-limit body (the explicit dlqMaxBodyBytes bound → 413)
		// from malformed JSON (400), mirroring the async enqueue path's mapping.
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "request body exceeds the DLQ API limit", http.StatusRequestEntityTooLarge)
			return false
		}
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
