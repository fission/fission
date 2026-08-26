// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statesvc/stateapi"
	"github.com/fission/fission/pkg/utils/httpx"
)

// maxListLimit bounds one listing page; larger requests are clamped. Listing
// is the most abusable endpoint (RFC-0023 open question) — pagination is
// mandatory, not cooperative.
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// eventLogSmallBodyCap bounds POST /v1/eventlog/read and .../head request
// bodies: neither carries a value payload, only a stream name and small
// integers, so a small fixed cap (unlike append's payload-sized formula) is
// enough.
const eventLogSmallBodyCap = 4096

// maxEventTypeBytes bounds EventInput.Type, which the append-body envelope
// otherwise leaves capped only by the whole-request LimitReader (bodyCap in
// eventAppend) — a caller could otherwise spend the payload-sized budget on
// type-string bytes instead of the per-event Payload cap.
const maxEventTypeBytes = 128

func writeError(w http.ResponseWriter, status int, code, msg string) {
	_ = httpx.WriteJSON(w, status, stateapi.Error{Error: msg, Code: code})
}

// writeStoreErr maps substrate errors onto the HTTP surface. ErrQuotaExceeded
// reaching here is always the key budget (the handler pre-checks value size
// with the same resolved quota and answers 413 before touching the store).
func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		writeError(w, http.StatusNotFound, stateapi.CodeNotFound, "key not found")
	case errors.Is(err, statestore.ErrVersionConflict):
		writeError(w, http.StatusPreconditionFailed, stateapi.CodeVersionConflict, "version precondition failed")
	case errors.Is(err, statestore.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, stateapi.CodeQuotaKeys, "keyspace live-key quota exceeded")
	case errors.Is(err, statestore.ErrCapabilityUnavailable):
		writeError(w, http.StatusServiceUnavailable, stateapi.CodeUnavailable, "state backend unavailable")
	default:
		writeError(w, http.StatusInternalServerError, stateapi.CodeInternal, "internal error")
	}
}

// handler serves the scoped keyed-state API. Every request's Scope was
// established by the auth middleware; the raw driver is never reachable —
// kv and el are the scoped/metered stores built over it.
type handler struct {
	kv     statestore.KVStore
	el     statestore.EventLog
	index  *FunctionIndex
	logger logr.Logger
}

// newHandler builds the authenticated API handler. ready gates /readyz.
func newHandler(kv statestore.KVStore, el statestore.EventLog, index *FunctionIndex, auth *authenticator, ready func() bool, logger logr.Logger) http.Handler {
	h := &handler{kv: kv, el: el, index: index, logger: logger}

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/state/{key}", h.get)
	api.HandleFunc("PUT /v1/state/{key}", h.put)
	api.HandleFunc("DELETE /v1/state/{key}", h.del)
	api.HandleFunc("POST /v1/state/{key}/cas", h.cas)
	api.HandleFunc("GET /v1/state", h.list)
	api.HandleFunc("POST /v1/eventlog/append", h.eventAppend)
	api.HandleFunc("POST /v1/eventlog/read", h.eventRead)
	api.HandleFunc("POST /v1/eventlog/head", h.eventHead)
	authed := auth.middleware(h.requireKnownKeyspace(api))

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	root.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	root.Handle("/v1/state", authed)
	root.Handle("/v1/state/", authed)
	// The root mux only forwards /v1/state paths above; /v1/eventlog/ needs
	// its own registration or the eventlog routes 404 before ever reaching
	// the inner api mux (and requireKnownKeyspace).
	root.Handle("/v1/eventlog/", authed)
	return root
}

// requireKnownKeyspace is defense-in-depth behind token verification: a
// correctly-derived token whose Function no longer exists (deleted, opted
// out) stops working, because tokens are stateless and cannot be revoked
// individually. Admin requests bypass it so operators can inspect and clean
// orphaned keyspaces.
func (h *handler) requireKnownKeyspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := scopeFrom(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, stateapi.CodeInternal, "missing auth scope")
			return
		}
		if !sc.admin && !h.index.Known(sc.scope.Namespace, sc.scope.Keyspace) {
			writeError(w, http.StatusForbidden, stateapi.CodeForbidden, "no function claims this keyspace")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	v, err := h.kv.Get(r.Context(), sc.scope, r.PathValue("key"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.Header().Set(stateapi.HeaderVersion, strconv.FormatInt(v.Version, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(v.Data)
}

// setOptions assembles SetOptions from If-Match / TTL headers. The keyspace's
// DefaultTTL applies when no TTL header is present.
func (h *handler) setOptions(r *http.Request, sc authedScope) (statestore.SetOptions, error) {
	var o statestore.SetOptions
	if im := r.Header.Get("If-Match"); im != "" {
		ver, err := strconv.ParseInt(im, 10, 64)
		if err != nil || ver < 0 {
			return o, errors.New("If-Match must be a non-negative integer version")
		}
		o.IfVersion = &ver
	}
	if ttlHdr := r.Header.Get(stateapi.HeaderTTL); ttlHdr != "" {
		ttl, err := time.ParseDuration(ttlHdr)
		if err != nil || ttl < 0 {
			return o, errors.New(stateapi.HeaderTTL + " must be a non-negative Go duration (e.g. 300s)")
		}
		o.TTL = ttl
	} else {
		o.TTL = h.index.DefaultTTL(sc.scope.Namespace, sc.scope.Keyspace)
	}
	return o, nil
}

// readValue reads the request body under the keyspace's MaxValueBytes cap,
// answering 413 with the machine-readable quota code on overflow.
func (h *handler) readValue(w http.ResponseWriter, body io.Reader, sc authedScope) ([]byte, bool) {
	maxBytes := h.index.Resolve(sc.scope).MaxValueBytes
	val, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "reading request body: "+err.Error())
		return nil, false
	}
	if int64(len(val)) > maxBytes {
		writeValueTooLarge(w)
		return nil, false
	}
	return val, true
}

// writeValueTooLarge answers the shared MaxValueBytes rejection (PUT and CAS
// both hit it), keeping the status and machine-readable code in one place.
func writeValueTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, stateapi.CodeQuotaValueBytes, "value exceeds the keyspace MaxValueBytes quota")
}

// writeEventConflict answers an EventLog.Append CAS mismatch: 412 (matching
// writeStoreErr's ErrVersionConflict mapping) with the stream's current head
// attached, so the caller can resynchronize without a separate Head call.
func writeEventConflict(w http.ResponseWriter, head int64) {
	_ = httpx.WriteJSON(w, http.StatusPreconditionFailed, stateapi.Error{
		Error: "version precondition failed",
		Code:  stateapi.CodeVersionConflict,
		Head:  &head,
	})
}

func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	o, err := h.setOptions(r, sc)
	if err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, err.Error())
		return
	}
	val, ok := h.readValue(w, r.Body, sc)
	if !ok {
		return
	}
	if err := h.kv.Set(r.Context(), sc.scope, r.PathValue("key"), val, o); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) del(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	var ifVersion int64
	if im := r.Header.Get("If-Match"); im != "" {
		ver, err := strconv.ParseInt(im, 10, 64)
		if err != nil || ver <= 0 {
			writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "If-Match must be a positive integer version")
			return
		}
		ifVersion = ver
	}
	if err := h.kv.Delete(r.Context(), sc.scope, r.PathValue("key"), ifVersion); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) cas(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	maxBytes := h.index.Resolve(sc.scope).MaxValueBytes
	// Envelope cap: base64 inflation (4/3) plus field overhead.
	body := io.LimitReader(r.Body, maxBytes*2+4096)
	var req stateapi.CASRequest
	// Deliberately strict (v2 defaults) even though responses keep v1
	// emission for SDK compatibility: requests are machine-generated by the
	// SDKs with exact keys, and strict decode surfaces an SDK encoding bug
	// as a clear 400 instead of a silently zero-filled field.
	if err := json.UnmarshalRead(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "invalid CAS body: "+err.Error())
		return
	}
	if req.ExpectVersion < 0 {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "expectVersion must be >= 0")
		return
	}
	if int64(len(req.Value)) > maxBytes {
		writeValueTooLarge(w)
		return
	}
	o := statestore.SetOptions{IfVersion: &req.ExpectVersion, TTL: h.index.DefaultTTL(sc.scope.Namespace, sc.scope.Keyspace)}
	if err := h.kv.Set(r.Context(), sc.scope, r.PathValue("key"), req.Value, o); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	limit := defaultListLimit
	if ls := r.URL.Query().Get("limit"); ls != "" {
		n, err := strconv.Atoi(ls)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxListLimit)
	}
	kp, err := h.kv.List(r.Context(), sc.scope, r.URL.Query().Get("prefix"), statestore.Page{
		Token: r.URL.Query().Get("cursor"),
		Limit: limit,
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stateapi.ListResponse{Keys: kp.Keys, Cursor: kp.Next})
}

// eventAppend serves POST /v1/eventlog/append. expectedSeq < 0 (the wire form
// of statestore.AppendAny) is rejected here: this API is CAS-only, so
// AppendAny is never reachable through statesvc.
func (h *handler) eventAppend(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	maxValueBytes := h.index.Resolve(sc.scope).MaxValueBytes
	// Envelope cap: MaxAppendEvents payloads at the keyspace's MaxValueBytes
	// cap, base64-inflated (4/3), plus field/envelope overhead. Deliberately
	// NOT the KV route's maxBytes*2+4096 formula (handler.go's cas), which
	// sizes for a single value and would truncate a legitimate full batch.
	bodyCap := int64(stateapi.MaxAppendEvents)*maxValueBytes*4/3 + 64<<10
	body := io.LimitReader(r.Body, bodyCap)
	var req stateapi.EventAppendRequest
	if err := json.UnmarshalRead(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "invalid append body: "+err.Error())
		return
	}
	if req.ExpectedSeq < 0 {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "expectedSeq must be >= 0")
		return
	}
	if len(req.Events) == 0 {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "events must not be empty")
		return
	}
	if len(req.Events) > stateapi.MaxAppendEvents {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "events exceeds the per-append limit")
		return
	}
	if !stateapi.ValidStream(req.Stream) {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadStream, "invalid stream name")
		return
	}
	events := make([]statestore.Event, len(req.Events))
	for i, ev := range req.Events {
		if ev.Type == "" || len(ev.Type) > maxEventTypeBytes {
			writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "event type must be 1-128 bytes")
			return
		}
		if int64(len(ev.Payload)) > maxValueBytes {
			writeValueTooLarge(w)
			return
		}
		events[i] = statestore.Event{Type: ev.Type, Payload: ev.Payload}
	}

	stream := stateapi.StreamName(sc.scope.Namespace, sc.scope.Keyspace, req.Stream)
	head, err := h.el.Append(r.Context(), stream, req.ExpectedSeq, events)
	if err != nil {
		if errors.Is(err, statestore.ErrVersionConflict) {
			writeEventConflict(w, head)
			return
		}
		writeStoreErr(w, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stateapi.EventAppendResponse{Head: head})
}

// eventRead serves POST /v1/eventlog/read.
func (h *handler) eventRead(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	body := io.LimitReader(r.Body, eventLogSmallBodyCap)
	var req stateapi.EventReadRequest
	if err := json.UnmarshalRead(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "invalid read body: "+err.Error())
		return
	}
	if !stateapi.ValidStream(req.Stream) {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadStream, "invalid stream name")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = stateapi.DefaultReadLimit
	}
	limit = min(limit, stateapi.MaxReadLimit)

	stream := stateapi.StreamName(sc.scope.Namespace, sc.scope.Keyspace, req.Stream)
	events, err := h.el.Read(r.Context(), stream, req.FromSeq, limit)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]stateapi.EventOutput, len(events))
	for i, ev := range events {
		out[i] = stateapi.EventOutput{Seq: ev.Seq, Type: ev.Type, Payload: ev.Payload, At: ev.At}
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stateapi.EventReadResponse{Events: out})
}

// eventHead serves POST /v1/eventlog/head.
func (h *handler) eventHead(w http.ResponseWriter, r *http.Request) {
	sc, _ := scopeFrom(r.Context())
	body := io.LimitReader(r.Body, eventLogSmallBodyCap)
	var req stateapi.EventHeadRequest
	if err := json.UnmarshalRead(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadRequest, "invalid head body: "+err.Error())
		return
	}
	if !stateapi.ValidStream(req.Stream) {
		writeError(w, http.StatusBadRequest, stateapi.CodeBadStream, "invalid stream name")
		return
	}
	stream := stateapi.StreamName(sc.scope.Namespace, sc.scope.Keyspace, req.Stream)
	head, err := h.el.Head(r.Context(), stream)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, stateapi.EventHeadResponse{Head: head})
}
