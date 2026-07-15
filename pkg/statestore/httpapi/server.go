// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// NewHandler serves caps over the HTTP wire contract. The returned handler is
// unauthenticated; the embedded store head wraps it with the ServiceStatestore
// HMAC verifier, exactly like the other internal listeners.
func NewHandler(caps statestore.Capabilities) http.Handler {
	h := &handler{caps: caps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealthz, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET "+PathReadyz, h.readyz)
	mux.HandleFunc("POST "+PathKVGet, h.kvGet)
	mux.HandleFunc("POST "+PathKVSet, h.kvSet)
	mux.HandleFunc("POST "+PathKVDelete, h.kvDelete)
	mux.HandleFunc("POST "+PathKVList, h.kvList)
	mux.HandleFunc("POST "+PathEventAppend, h.eventAppend)
	mux.HandleFunc("POST "+PathEventRead, h.eventRead)
	mux.HandleFunc("POST "+PathEventTrim, h.eventTrim)
	mux.HandleFunc("POST "+PathEventHead, h.eventHead)
	mux.HandleFunc("POST "+PathQueueEnqueue, h.queueEnqueue)
	mux.HandleFunc("POST "+PathQueueLease, h.queueLease)
	mux.HandleFunc("POST "+PathQueueAck, h.queueAck)
	mux.HandleFunc("POST "+PathQueueNack, h.queueNack)
	mux.HandleFunc("POST "+PathQueueKill, h.queueKill)
	mux.HandleFunc("POST "+PathQueueDeadLetter, h.queueDeadLetters)
	mux.HandleFunc("POST "+PathQueueRedrive, h.queueRedrive)
	mux.HandleFunc("POST "+PathQueuePurge, h.queuePurge)
	mux.HandleFunc("POST "+PathQueueStats, h.queueStats)
	return mux
}

type handler struct{ caps statestore.Capabilities }

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.caps.Ping(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// decode reads a JSON request body into dst, writing a 400 on failure. The body
// is bounded so an oversized or unbounded request cannot balloon memory.
func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var dst T
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(&dst); err != nil {
		writeCode(w, http.StatusBadRequest, Error{Code: CodeBadRequest, Message: err.Error()})
		return dst, false
	}
	return dst, true
}

func writeErr(w http.ResponseWriter, err error) {
	status, code := ErrToCode(err)
	writeCode(w, status, Error{Code: code, Message: err.Error()})
}

func writeCode(w http.ResponseWriter, status int, e Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// cap accessors that translate an unavailable capability into a wire error.
func (h *handler) kv(w http.ResponseWriter) (statestore.KVStore, bool) {
	kv, err := h.caps.KV()
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return kv, true
}

func (h *handler) el(w http.ResponseWriter) (statestore.EventLog, bool) {
	el, err := h.caps.EventLog()
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return el, true
}

func (h *handler) q(w http.ResponseWriter) (statestore.Queue, bool) {
	q, err := h.caps.Queue()
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return q, true
}

func (h *handler) kvGet(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[KVGetReq](w, r)
	if !ok {
		return
	}
	kv, ok := h.kv(w)
	if !ok {
		return
	}
	v, err := kv.Get(r.Context(), req.Scope, req.Key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, KVGetResp{Value: v.Data, Version: v.Version})
}

func (h *handler) kvSet(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[KVSetReq](w, r)
	if !ok {
		return
	}
	kv, ok := h.kv(w)
	if !ok {
		return
	}
	err := kv.Set(r.Context(), req.Scope, req.Key, req.Value, statestore.SetOptions{
		IfVersion: req.IfVersion,
		TTL:       time.Duration(req.TTLNanos),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) kvDelete(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[KVDeleteReq](w, r)
	if !ok {
		return
	}
	kv, ok := h.kv(w)
	if !ok {
		return
	}
	if err := kv.Delete(r.Context(), req.Scope, req.Key, req.IfVersion); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) kvList(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[KVListReq](w, r)
	if !ok {
		return
	}
	kv, ok := h.kv(w)
	if !ok {
		return
	}
	page, err := kv.List(r.Context(), req.Scope, req.Prefix, req.Page)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, KVListResp{Keys: page.Keys, Next: page.Next})
}

func (h *handler) eventAppend(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[EventAppendReq](w, r)
	if !ok {
		return
	}
	el, ok := h.el(w)
	if !ok {
		return
	}
	head, err := el.Append(r.Context(), req.Stream, req.ExpectedSeq, req.Events)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, EventAppendResp{Head: head})
}

func (h *handler) eventRead(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[EventReadReq](w, r)
	if !ok {
		return
	}
	el, ok := h.el(w)
	if !ok {
		return
	}
	evs, err := el.Read(r.Context(), req.Stream, req.FromSeq, req.Limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, EventReadResp{Events: evs})
}

func (h *handler) eventTrim(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[EventTrimReq](w, r)
	if !ok {
		return
	}
	el, ok := h.el(w)
	if !ok {
		return
	}
	if err := el.Trim(r.Context(), req.Stream, req.BelowSeq); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) eventHead(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[EventHeadReq](w, r)
	if !ok {
		return
	}
	el, ok := h.el(w)
	if !ok {
		return
	}
	head, err := el.Head(r.Context(), req.Stream)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, EventHeadResp{Head: head})
}

func (h *handler) queueEnqueue(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueEnqueueReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	id, err := q.Enqueue(r.Context(), req.Queue, statestore.Message{Body: req.Body}, statestore.EnqueueOptions{
		Delay:    time.Duration(req.DelayNanos),
		DedupKey: req.DedupKey,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueueEnqueueResp{ID: id})
}

func (h *handler) queueLease(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueLeaseReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	msgs, err := q.Lease(r.Context(), req.Queue, req.N, time.Duration(req.LeaseForNanos))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueueLeaseResp{Messages: msgs})
}

func (h *handler) queueAck(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueAckReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	if err := q.Ack(r.Context(), req.Receipt); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) queueNack(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueNackReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	if err := q.Nack(r.Context(), req.Receipt, time.Duration(req.RetryAfterNanos)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) queueKill(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueKillReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	if err := q.Kill(r.Context(), req.Receipt, req.Reason); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) queueDeadLetters(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueDeadLettersReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	msgs, err := q.DeadLetters(r.Context(), req.Queue, req.Page)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueueDeadLettersResp{Messages: msgs})
}

func (h *handler) queueRedrive(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueRedriveReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	n, err := q.Redrive(r.Context(), req.Queue, req.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueueRedriveResp{Redriven: n})
}

func (h *handler) queuePurge(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueuePurgeReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	n, err := q.Purge(r.Context(), req.Queue)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueuePurgeResp{Purged: n})
}

func (h *handler) queueStats(w http.ResponseWriter, r *http.Request) {
	req, ok := decode[QueueStatsReq](w, r)
	if !ok {
		return
	}
	q, ok := h.q(w)
	if !ok {
		return
	}
	st, err := q.Stats(r.Context(), req.Queue)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, QueueStatsResp{
		Visible:               st.Visible,
		Leased:                st.Leased,
		Dead:                  st.Dead,
		OldestVisibleAgeNanos: st.OldestVisibleAge.Nanoseconds(),
	})
}
