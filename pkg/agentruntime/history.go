// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// history.go implements the agentruntime-side reader over the per-session
// EventLog stream and checkpoint record that the SDK writes through statesvc's
// scoped routes (RFC-0027 G13 / RFC-0023 §6.2). It never appends: turn history
// is written by the SDK through statesvc's HTTP surface, using the caller's
// own function-state bearer token — HistoryStore only reads, trims, and
// manages the checkpoint record from the platform side, over the agent
// runtime's DIRECT statestore handles (no HTTP hop back through statesvc).
package agentruntime

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"time"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statesvc"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// HistoryStreamPrefix namespaces every session's caller-visible EventLog
// stream, so the platform's history streams cannot collide with topic/*
// (RFC-0027) or any other client-chosen stream name in the same keyspace.
const HistoryStreamPrefix = "agentlog/"

// CheckpointKeyPrefix namespaces the checkpoint KV key within the
// function-state keyspace. "." is the separator (not "/"): the checkpoint id
// is a single path segment, one key per session, never a sub-hierarchy.
const CheckpointKeyPrefix = "agentckpt."

// HistoryClientStream is the caller-visible stream name the SDK uses when it
// appends turn history through statesvc's scoped EventLog routes. HistoryStore
// derives the same name on the read side so both sides agree on the stream
// without either hand-deriving stateapi.StreamName's internal shape twice.
func HistoryClientStream(sessionID string) string { return HistoryStreamPrefix + sessionID }

// CheckpointRecord mirrors the RFC §6.2 checkpoint schema. Payload is opaque
// to the platform: HistoryStore stores and returns it verbatim, never
// interprets it.
//
// Payload is jsontext.Value, not json.RawMessage: encoding/json/v2 does not
// export that name (it lives only as an alias in v1, see pkg/mcp/registry.go).
// It is a pure rename here too — this type never decodes or re-marshals the
// bytes it carries beyond the outer CheckpointRecord envelope.
type CheckpointRecord struct {
	V                 int            `json:"v"`
	CoveredThroughSeq int64          `json:"coveredThroughSeq"`
	Kind              string         `json:"kind"`
	Payload           jsontext.Value `json:"payload"`
	CreatedAt         time.Time      `json:"createdAt"`
	Turn              int64          `json:"turn"`
}

// HistoryStore reads session turn history and manages the session checkpoint
// record, both scoped to the same function-state keyspace the pod's bearer
// token binds (see stateScope). It holds no append path: the SDK is the sole
// writer of history, via statesvc; the platform side only consumes.
type HistoryStore struct {
	el statestore.EventLog
	kv statestore.KVStore
}

// NewHistoryStore returns a HistoryStore over el and kv, the agent runtime's
// direct statestore handles (opened once at startup, see main.go).
func NewHistoryStore(el statestore.EventLog, kv statestore.KVStore) *HistoryStore {
	return &HistoryStore{el: el, kv: kv}
}

// stateScope is the FUNCTION-STATE scope (ns, statesvc.StateOwner, keyspace)
// — the same scope the pod's bearer token binds, which is WHY history and the
// checkpoint live here rather than under some agentruntime-owned Owner (RFC
// divergence, pre-ruled: the SDK holds no other credential to write with).
func stateScope(ns, keyspace string) statestore.Scope {
	return statestore.Scope{Namespace: ns, Owner: statesvc.StateOwner, Keyspace: keyspace}
}

// historyStream returns the internal, scope-qualified EventLog stream for one
// session's history, via stateapi.StreamName — the single source of the
// fnstate/ naming shape (never hand-derived here).
func historyStream(ns, keyspace, sessionID string) string {
	return stateapi.StreamName(ns, keyspace, HistoryClientStream(sessionID))
}

// Read returns up to limit history events for sessionID with Seq > fromSeq, in
// order.
func (h *HistoryStore) Read(ctx context.Context, ns, keyspace, sessionID string, fromSeq int64, limit int) ([]statestore.Event, error) {
	return h.el.Read(ctx, historyStream(ns, keyspace, sessionID), fromSeq, limit)
}

// Head returns sessionID's current history head sequence (0 for a session
// with no history yet).
func (h *HistoryStore) Head(ctx context.Context, ns, keyspace, sessionID string) (int64, error) {
	return h.el.Head(ctx, historyStream(ns, keyspace, sessionID))
}

// GetCheckpoint returns sessionID's checkpoint record. statestore.ErrNotFound
// passes through unmapped when no checkpoint has been written yet.
func (h *HistoryStore) GetCheckpoint(ctx context.Context, ns, keyspace, sessionID string) (CheckpointRecord, error) {
	v, err := h.kv.Get(ctx, stateScope(ns, keyspace), CheckpointKeyPrefix+sessionID)
	if err != nil {
		return CheckpointRecord{}, err
	}
	var rec CheckpointRecord
	if err := json.Unmarshal(v.Data, &rec); err != nil {
		return CheckpointRecord{}, err
	}
	return rec, nil
}

// DeleteCheckpoint removes sessionID's checkpoint record unconditionally
// (ifVersion 0): idempotent whether or not a checkpoint currently exists, so a
// caller never has to Get-then-Delete to avoid an error on an absent record.
func (h *HistoryStore) DeleteCheckpoint(ctx context.Context, ns, keyspace, sessionID string) error {
	return h.kv.Delete(ctx, stateScope(ns, keyspace), CheckpointKeyPrefix+sessionID, 0)
}

// TrimBelow drops sessionID's history events with Seq < belowSeq (history GC,
// e.g. after a checkpoint covers them). The stream's append point and head are
// unaffected — only Read's visible tail shrinks.
func (h *HistoryStore) TrimBelow(ctx context.Context, ns, keyspace, sessionID string, belowSeq int64) error {
	return h.el.Trim(ctx, historyStream(ns, keyspace, sessionID), belowSeq)
}
