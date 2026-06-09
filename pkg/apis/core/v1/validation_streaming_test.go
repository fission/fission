// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import "testing"

func TestStreamingConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     StreamingConfig
		wantErr bool
	}{
		{"defaults", StreamingConfig{Enabled: true, Protocol: StreamingAuto}, false},
		{"empty protocol ok", StreamingConfig{Enabled: true}, false},
		{"negative idle", StreamingConfig{Enabled: true, IdleTimeoutSeconds: -1}, true},
		{"negative max", StreamingConfig{Enabled: true, MaxDurationSeconds: -1}, true},
		{"max below idle", StreamingConfig{Enabled: true, IdleTimeoutSeconds: 120, MaxDurationSeconds: 30}, true},
		{"max equals idle ok", StreamingConfig{Enabled: true, IdleTimeoutSeconds: 30, MaxDurationSeconds: 30}, false},
		{"max above idle ok", StreamingConfig{Enabled: true, IdleTimeoutSeconds: 30, MaxDurationSeconds: 120}, false},
		{"bad protocol", StreamingConfig{Enabled: true, Protocol: StreamingProtocol("nope")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestFunctionSpecValidateStreaming asserts FunctionSpec.Validate surfaces a bad
// Streaming config and that a nil Streaming is accepted (backward-compat guard).
func TestFunctionSpecValidateStreaming(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("nil streaming ok", func(t *testing.T) {
		t.Parallel()
		spec := base()
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid streaming surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Streaming = &StreamingConfig{Enabled: true, IdleTimeoutSeconds: -5}
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for negative idle timeout")
		}
	})
}
