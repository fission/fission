// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func agentDur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

// TestEntryFromAgentConfig_ArchiveClampedToIdle pins fix #3: entryFromAgentConfig
// must clamp the resolved ArchiveAfter up to at least the resolved IdleAfter,
// so a config that sets only IdleAfter (leaving ArchiveAfter to default to 24h)
// can never archive a session while it is still inside its idle window.
func TestEntryFromAgentConfig_ArchiveClampedToIdle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		ac          fv1.AgentConfig
		wantIdle    time.Duration
		wantArchive time.Duration
	}{
		{
			name:        "idle above default archive clamps archive up",
			ac:          fv1.AgentConfig{IdleAfter: agentDur(48 * time.Hour)},
			wantIdle:    48 * time.Hour,
			wantArchive: 48 * time.Hour, // clamped up from the 24h default
		},
		{
			name:        "both defaults keep the normal ordering",
			ac:          fv1.AgentConfig{},
			wantIdle:    DefaultIdleAfter,
			wantArchive: DefaultArchiveAfter,
		},
		{
			name:        "explicit archive above idle is unchanged",
			ac:          fv1.AgentConfig{IdleAfter: agentDur(time.Hour), ArchiveAfter: agentDur(6 * time.Hour)},
			wantIdle:    time.Hour,
			wantArchive: 6 * time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := entryFromAgentConfig("ns", "fn", &tt.ac, nil, 0)
			assert.Equal(t, tt.wantIdle, e.IdleAfter)
			assert.Equal(t, tt.wantArchive, e.ArchiveAfter)
			assert.GreaterOrEqual(t, e.ArchiveAfter, e.IdleAfter, "ArchiveAfter must never resolve below IdleAfter")
		})
	}
}

// TestEntryFromAgentConfig_MaxSessionsDefault pins fix #5: the platform-wide
// default ceiling applies only when the spec sets no MaxSessions of its own,
// and a default of 0 leaves an unqualified agent unbounded.
func TestEntryFromAgentConfig_MaxSessionsDefault(t *testing.T) {
	t.Parallel()

	t.Run("default applied when spec omits MaxSessions", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, nil, 1000)
		assert.EqualValues(t, 1000, e.MaxSessions)
	})
	t.Run("spec MaxSessions wins over the default", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{MaxSessions: 5}, nil, 1000)
		assert.EqualValues(t, 5, e.MaxSessions)
	})
	t.Run("default 0 leaves the agent unbounded", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, nil, 0)
		assert.EqualValues(t, 0, e.MaxSessions)
	})
}

// TestEntryFromAgentConfig_StateKeyspace pins the G13 state-keyspace
// plumbing: StateKeyspace is empty iff the function has no State config
// (history disabled), and otherwise resolves via StateConfig.
// EffectiveKeyspace (explicit Keyspace, or the function name as the
// fallback). HistoryTrim carries AgentConfig.HistoryTrimBelowCheckpoint
// through unchanged.
func TestEntryFromAgentConfig_StateKeyspace(t *testing.T) {
	t.Parallel()

	t.Run("no State -> empty keyspace, history disabled", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, nil, 0)
		assert.Empty(t, e.StateKeyspace)
		assert.False(t, e.HistoryTrim)
	})

	t.Run("State with no explicit Keyspace falls back to the function name", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, &fv1.StateConfig{}, 0)
		assert.Equal(t, "fn", e.StateKeyspace)
	})

	t.Run("State with explicit Keyspace wins", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, &fv1.StateConfig{Keyspace: "custom"}, 0)
		assert.Equal(t, "custom", e.StateKeyspace)
	})

	t.Run("HistoryTrimBelowCheckpoint carried through when State is set", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{HistoryTrimBelowCheckpoint: true}, &fv1.StateConfig{}, 0)
		assert.True(t, e.HistoryTrim)
		assert.NotEmpty(t, e.StateKeyspace)
	})
}
