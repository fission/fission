// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// view.go implements AgentView, the per-replica in-memory read model of
// Functions with Spec.Agent != nil. It follows the pkg/mcp Registry pattern:
// reconcile-maintained, RWMutex-guarded, and safe for concurrent
// reconcile/serve.

package agentruntime

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	// DefaultIdleAfter is the platform default for AgentConfig.IdleAfter when
	// it is nil or zero.
	DefaultIdleAfter = 5 * time.Minute
	// DefaultArchiveAfter is the platform default for AgentConfig.ArchiveAfter
	// when it is nil or zero.
	DefaultArchiveAfter = 24 * time.Hour
)

// AgentEntry is the resolved, defaults-applied view of one agent-enabled
// Function.
type AgentEntry struct {
	Namespace, Name string

	// SessionSource and SessionName are where the dispatcher extracts the
	// session id from an incoming request. Defaults (when Spec.Agent.Session
	// is nil): fv1.SessionSourceHeader / fv1.DefaultAgentSessionHeader.
	SessionSource fv1.SessionSource
	SessionName   string

	// IdleAfter and ArchiveAfter are the resolved lifecycle policy. Defaults
	// (when nil or zero on the spec): DefaultIdleAfter / DefaultArchiveAfter.
	IdleAfter    time.Duration
	ArchiveAfter time.Duration

	// MaxSessions is the live-session quota, unchanged from the spec (0 means
	// unbounded).
	MaxSessions int64

	// StateKeyspace is the resolved state keyspace (fv1.StateConfig.
	// EffectiveKeyspace) this agent's session history lives under. Empty
	// means Spec.State == nil, which means history is disabled for this
	// agent (no keyspace, nothing to read or trim).
	StateKeyspace string

	// HistoryTrim mirrors AgentConfig.HistoryTrimBelowCheckpoint: whether the
	// sweeper may trim this agent's session fact logs below each session's
	// committed checkpoint coverage boundary. Only meaningful when
	// StateKeyspace is non-empty — admission rejects a true value with no
	// State, so the two never disagree on a validly-admitted config.
	HistoryTrim bool
}

// AgentView is the per-replica in-memory read model of Functions with
// Agent != nil, maintained by AgentReconciler and read by the request
// dispatcher and idle/archive sweeper. Each replica keeps its own copy (no
// leader election), so the reconcile is idempotent map mutation.
type AgentView struct {
	mu   sync.RWMutex
	byFn map[types.NamespacedName]AgentEntry
}

// NewAgentView returns an empty AgentView.
func NewAgentView() *AgentView {
	return &AgentView{byFn: map[types.NamespacedName]AgentEntry{}}
}

// Upsert inserts or replaces the entry for e.Namespace/e.Name.
func (v *AgentView) Upsert(e AgentEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.byFn[types.NamespacedName{Namespace: e.Namespace, Name: e.Name}] = e
}

// Remove drops the entry for nn, if present.
func (v *AgentView) Remove(nn types.NamespacedName) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.byFn, nn)
}

// Lookup returns the entry for ns/name, if present.
func (v *AgentView) Lookup(ns, name string) (AgentEntry, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	e, ok := v.byFn[types.NamespacedName{Namespace: ns, Name: name}]
	return e, ok
}

// List returns a snapshot of all entries, in no particular order.
func (v *AgentView) List() []AgentEntry {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]AgentEntry, 0, len(v.byFn))
	for _, e := range v.byFn {
		out = append(out, e)
	}
	return out
}

// entryFromAgentConfig builds the defaults-applied AgentEntry for a Function
// whose Spec.Agent is non-nil. maxSessionsDefault is the platform-wide live
// session ceiling applied when the spec sets no MaxSessions of its own (0
// disables the default, leaving the agent unbounded). sc is the function's
// Spec.State (nil when the function has no state config): when non-nil, its
// EffectiveKeyspace resolves StateKeyspace; when nil, StateKeyspace stays
// empty, which is the "history disabled" signal Tasks 4-5 key off of. The
// fetcher's additional Environment gate (AllowedFunctionsPerContainerInfinite
// -> no token) is deliberately not replicated here: a Function-only
// reconciler cannot see the Environment, and the divergence is benign — with
// no token ever written, the stream is never written either, so the
// registry's Head is 0 and history reads as empty regardless of keyspace.
func entryFromAgentConfig(ns, name string, ac *fv1.AgentConfig, sc *fv1.StateConfig, maxSessionsDefault int64) AgentEntry {
	e := AgentEntry{
		Namespace:     ns,
		Name:          name,
		SessionSource: fv1.SessionSourceHeader,
		SessionName:   fv1.DefaultAgentSessionHeader,
		IdleAfter:     DefaultIdleAfter,
		ArchiveAfter:  DefaultArchiveAfter,
		MaxSessions:   ac.MaxSessions,
		HistoryTrim:   ac.HistoryTrimBelowCheckpoint,
	}
	if sc != nil {
		e.StateKeyspace = sc.EffectiveKeyspace(name)
	}
	if ac.Session != nil {
		e.SessionSource = ac.Session.Source
		e.SessionName = ac.Session.Name
	}
	if ac.IdleAfter != nil && ac.IdleAfter.Duration != 0 {
		e.IdleAfter = ac.IdleAfter.Duration
	}
	if ac.ArchiveAfter != nil && ac.ArchiveAfter.Duration != 0 {
		e.ArchiveAfter = ac.ArchiveAfter.Duration
	}
	// The cross-field ArchiveAfter >= IdleAfter invariant that AgentConfig.
	// Validate enforces only when BOTH are set on the spec must ALSO hold on the
	// resolved values, where the two defaults (5m / 24h) are applied
	// independently. Without this clamp, IdleAfter:48h with ArchiveAfter unset
	// (defaulting to 24h) would archive a session on the first sweep after it
	// goes idle — archived while still within its idle window. Clamp on the
	// resolved values so no defaulting combination can violate the invariant.
	e.ArchiveAfter = max(e.ArchiveAfter, e.IdleAfter)
	// Apply the platform-wide MaxSessions ceiling when the spec opted out of a
	// per-agent quota (0). A default of 0 means "no platform ceiling", so an
	// unset spec quota stays unbounded.
	if e.MaxSessions == 0 {
		e.MaxSessions = maxSessionsDefault
	}
	return e
}
