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
			e := entryFromAgentConfig("ns", "fn", &tt.ac, 0)
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
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, 1000)
		assert.EqualValues(t, 1000, e.MaxSessions)
	})
	t.Run("spec MaxSessions wins over the default", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{MaxSessions: 5}, 1000)
		assert.EqualValues(t, 5, e.MaxSessions)
	})
	t.Run("default 0 leaves the agent unbounded", func(t *testing.T) {
		t.Parallel()
		e := entryFromAgentConfig("ns", "fn", &fv1.AgentConfig{}, 0)
		assert.EqualValues(t, 0, e.MaxSessions)
	})
}
