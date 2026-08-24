// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// session.go implements the statestore-backed session record store used by
// the agent runtime's request dispatcher and idle/archive sweeper.

package agentruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// SessionStatus is the lifecycle state of a session record.
type SessionStatus string

const (
	// StatusActive is a session with recent turn activity.
	StatusActive SessionStatus = "active"
	// StatusIdle is a session past its agent's IdleAfter with no activity.
	StatusIdle SessionStatus = "idle"
	// StatusArchived is a session moved out of the live keyspace into the
	// archived keyspace, retained for inspection until its retention TTL.
	StatusArchived SessionStatus = "archived"
)

var (
	// ErrSessionExists is returned by Create when a record with the same ID
	// already exists (the create-only IfVersion=0 write clashed).
	ErrSessionExists = errors.New("agentruntime: session already exists")
	// ErrSessionQuota is returned by Create when the agent's MaxSessions
	// live-key budget is already spent (mapped from statestore.ErrQuotaExceeded).
	ErrSessionQuota = errors.New("agentruntime: session quota exceeded")
)

// SessionStats is the per-session meter set (G19: full meter schema from day
// one; Tokens/Cost reserved for the gateway integration, always 0 for now).
type SessionStats struct {
	Turns        int64 `json:"turns"`
	Errors       int64 `json:"errors"`
	ToolCalls    int64 `json:"toolCalls"`
	ActiveMillis int64 `json:"activeMillis"`
	TokensIn     int64 `json:"tokensIn"`
	TokensOut    int64 `json:"tokensOut"`
	CostMicroUSD int64 `json:"costMicroUSD"`
}

// SessionRecord is the persisted state of one agent session.
type SessionRecord struct {
	ID           string        `json:"id"`
	Agent        string        `json:"agent"`
	Namespace    string        `json:"namespace"`
	Status       SessionStatus `json:"status"`
	CreatedAt    time.Time     `json:"createdAt"`
	LastActiveAt time.Time     `json:"lastActiveAt"`
	CurrentPod   string        `json:"currentPod,omitempty"` // best-effort UI truth, never routing truth
	Stats        SessionStats  `json:"stats"`
}

// SessionStore persists SessionRecords in a statestore KVStore. Live records
// live in one keyspace and archived records in a sibling keyspace, so the
// CountedKV live-key budget meters only live sessions.
type SessionStore struct {
	kv  statestore.KVStore
	now func() time.Time
}

// NewSessionStore returns a SessionStore backed by kv. now is the store's
// injected clock.
func NewSessionStore(kv statestore.KVStore, now func() time.Time) *SessionStore {
	return &SessionStore{kv: kv, now: now}
}

// sessionScope is the live keyspace for one agent's sessions.
func sessionScope(ns, agent string) statestore.Scope {
	return statestore.Scope{Namespace: ns, Owner: "agent/" + agent, Keyspace: "sessions"}
}

// archivedScope is the sibling keyspace holding archived records. It is
// separate from sessionScope so an archived record's retention TTL never
// counts against the live CountedKV MaxSessions budget.
func archivedScope(ns, agent string) statestore.Scope {
	return statestore.Scope{Namespace: ns, Owner: "agent/" + agent, Keyspace: "sessions-archived"}
}

// Get returns the live record for id and its KV version, for CAS-back via
// Update or Archive.
func (s *SessionStore) Get(ctx context.Context, ns, agent, id string) (SessionRecord, int64, error) {
	v, err := s.kv.Get(ctx, sessionScope(ns, agent), id)
	if err != nil {
		return SessionRecord{}, 0, err
	}
	var rec SessionRecord
	if err := json.Unmarshal(v.Data, &rec); err != nil {
		return SessionRecord{}, 0, err
	}
	return rec, v.Version, nil
}

// Create writes a new active record create-only (IfVersion=0) with liveTTL
// (orphan self-expiry: if the agent Function is deleted, records vanish
// without a sweeper). maxSessions>0 routes through CountedKV.SetCounted so
// the budget check and the write are one atomic step.
//
// Returns ErrSessionQuota on statestore.ErrQuotaExceeded, ErrSessionExists on
// an IfVersion=0 clash.
func (s *SessionStore) Create(ctx context.Context, rec SessionRecord, maxSessions int64, liveTTL time.Duration) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	scope := sessionScope(rec.Namespace, rec.Agent)
	opts := statestore.SetOptions{IfVersion: new(int64), TTL: liveTTL}

	if ck, ok := s.kv.(statestore.CountedKV); ok && maxSessions > 0 {
		err = ck.SetCounted(ctx, scope, rec.ID, data, opts, maxSessions)
	} else {
		err = s.kv.Set(ctx, scope, rec.ID, data, opts)
	}
	switch {
	case errors.Is(err, statestore.ErrQuotaExceeded):
		return ErrSessionQuota
	case errors.Is(err, statestore.ErrVersionConflict):
		return ErrSessionExists
	default:
		return err
	}
}

// Update CAS-writes rec at version with liveTTL (refreshing the orphan
// expiry). statestore.ErrVersionConflict passes through unmapped.
func (s *SessionStore) Update(ctx context.Context, rec SessionRecord, version int64, liveTTL time.Duration) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	scope := sessionScope(rec.Namespace, rec.Agent)
	opts := statestore.SetOptions{IfVersion: new(version), TTL: liveTTL}
	return s.kv.Set(ctx, scope, rec.ID, data, opts)
}

// Archive moves the record to the archived keyspace: it writes the archived
// copy (Status=archived, TTL=retention, unconditional set) FIRST, then
// CAS-deletes the live key at version. A delete conflict means a concurrent
// turn revived the session — the archived copy is removed and Archive
// aborts, leaving the live record as-is; the next sweep retries.
func (s *SessionStore) Archive(ctx context.Context, rec SessionRecord, version int64, retention time.Duration) error {
	archived := rec
	archived.Status = StatusArchived

	data, err := json.Marshal(archived)
	if err != nil {
		return err
	}
	aScope := archivedScope(rec.Namespace, rec.Agent)
	if err := s.kv.Set(ctx, aScope, rec.ID, data, statestore.SetOptions{TTL: retention}); err != nil {
		return err
	}

	lScope := sessionScope(rec.Namespace, rec.Agent)
	if err := s.kv.Delete(ctx, lScope, rec.ID, version); err != nil {
		// Concurrent revive: undo the speculative archived write and abort.
		_ = s.kv.Delete(ctx, aScope, rec.ID, 0)
		return err
	}
	return nil
}

// List pages LIVE records for one agent (prefix "" = all keys in the scope).
// Page keys come from KVStore.List; each key is decoded via Get. A key that
// expires between List and Get (an orphan TTL race) is skipped rather than
// failing the page.
func (s *SessionStore) List(ctx context.Context, ns, agent, pageToken string, limit int) ([]SessionRecord, string, error) {
	scope := sessionScope(ns, agent)
	kp, err := s.kv.List(ctx, scope, "", statestore.Page{Token: pageToken, Limit: limit})
	if err != nil {
		return nil, "", err
	}
	recs := make([]SessionRecord, 0, len(kp.Keys))
	for _, key := range kp.Keys {
		rec, _, err := s.Get(ctx, ns, agent, key)
		if errors.Is(err, statestore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		recs = append(recs, rec)
	}
	return recs, kp.Next, nil
}
