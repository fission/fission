// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func TestAgentConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     AgentConfig
		wantErr bool
	}{
		{"empty config is valid (all defaults)", AgentConfig{}, false},
		{"session header source", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: "X-Session"}}, false},
		{"session queryparam source", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceQueryParam, Name: "session"}}, false},
		{"session with bad source", AgentConfig{Session: &AgentSessionConfig{Source: "path", Name: "x"}}, true},
		{"session with empty name", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader}}, true},
		// Header-name charset: a CR/LF or colon in the name would corrupt the
		// forwarded request; pin the token charset.
		{"session name bad chars", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: "bad name\r\n"}}, true},
		{"negative idleAfter", AgentConfig{IdleAfter: dur(-time.Minute)}, true},
		{"negative archiveAfter", AgentConfig{ArchiveAfter: dur(-time.Hour)}, true},
		// ArchiveAfter < IdleAfter would archive a session while still active.
		{"archiveAfter below idleAfter", AgentConfig{IdleAfter: dur(time.Hour), ArchiveAfter: dur(time.Minute)}, true},
		{"archiveAfter equal idleAfter ok", AgentConfig{IdleAfter: dur(time.Hour), ArchiveAfter: dur(time.Hour)}, false},
		{"negative maxSessions", AgentConfig{MaxSessions: -1}, true},
		// Reserved header names: a session source must not shadow the runtime's
		// own Authorization or X-Fission-* headers (case-insensitive).
		{"session name Authorization reserved", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: "Authorization"}}, true},
		{"session name authorization lowercase reserved", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: "authorization"}}, true},
		{"session name X-Fission- prefix reserved", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: "X-Fission-Session"}}, true},
		{"session name x-fission- lowercase prefix reserved", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceQueryParam, Name: "x-fission-foo"}}, true},
		// agentSessionNameRegexp's {1,128} boundary: 128 chars is the last
		// accepted length, 129 the first rejected one.
		{"session name at 128 chars ok", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: strings.Repeat("a", 128)}}, false},
		{"session name at 129 chars rejected", AgentConfig{Session: &AgentSessionConfig{Source: SessionSourceHeader, Name: strings.Repeat("a", 129)}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestFunctionSpecValidateAgent asserts FunctionSpec.Validate surfaces a bad
// Agent config via validateForAdmission — proves the spec.Agent != nil clause
// is reachable and that the wiring is not dead code.
func TestFunctionSpecValidateAgent(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("nil agent ok", func(t *testing.T) {
		t.Parallel()
		spec := base()
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid agent surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Agent = &AgentConfig{MaxSessions: -1}
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for negative maxSessions")
		}
	})
}

// TestFunctionSpecValidateAgentHistoryTrimRequiresState pins the G13
// cross-field rule: HistoryTrimBelowCheckpoint is a knob on the agent's
// session fact logs, which live in the function's state keyspace — a knob
// that can never act (no State means no state keyspace, means no history)
// must be rejected at admission rather than silently doing nothing. The rule
// is cross-field (Agent + State), so it lives in FunctionSpec.
// validateForAdmission, not AgentConfig.Validate (which sees only Agent).
func TestFunctionSpecValidateAgentHistoryTrimRequiresState(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("HistoryTrimBelowCheckpoint without State rejected", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Agent = &AgentConfig{HistoryTrimBelowCheckpoint: true}
		err := spec.Validate()
		if err == nil {
			t.Fatalf("expected error: HistoryTrimBelowCheckpoint requires State")
		}
	})

	t.Run("HistoryTrimBelowCheckpoint with State accepted", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Agent = &AgentConfig{HistoryTrimBelowCheckpoint: true}
		spec.State = &StateConfig{}
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("HistoryTrimBelowCheckpoint false without State is fine", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Agent = &AgentConfig{}
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
