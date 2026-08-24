// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
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
