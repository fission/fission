// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func fnWith(s *fv1.StreamingConfig) *fv1.Function {
	return &fv1.Function{Spec: fv1.FunctionSpec{Streaming: s}}
}

func TestResolveProxyPolicy(t *testing.T) {
	t.Parallel()
	const def = 60 * time.Second
	tests := []struct {
		name         string
		fn           *fv1.Function
		fnTimeout    time.Duration
		wantStream   bool
		wantProtocol fv1.StreamingProtocol
		wantIdle     time.Duration
		wantMax      time.Duration
	}{
		// Presence is the on switch: nil = classic, non-nil = streaming.
		{"classic nil", fnWith(nil), 30 * time.Second, false, "", 0, 30 * time.Second},
		// Streaming never inherits FunctionTimeout as a ceiling — idle governs,
		// max is explicit-only (0 = unlimited).
		{"streaming defaults: no inherited ceiling", fnWith(&fv1.StreamingConfig{}), 30 * time.Second, true, fv1.StreamingAuto, def, 0},
		{"streaming explicit protocol", fnWith(&fv1.StreamingConfig{Protocol: fv1.StreamingSSE}), 30 * time.Second, true, fv1.StreamingSSE, def, 0},
		{"streaming idle override", fnWith(&fv1.StreamingConfig{IdleTimeoutSeconds: 15}), 30 * time.Second, true, fv1.StreamingAuto, 15 * time.Second, 0},
		{"streaming max override", fnWith(&fv1.StreamingConfig{MaxDurationSeconds: 600}), 30 * time.Second, true, fv1.StreamingAuto, def, 600 * time.Second},
		{"streaming no fnTimeout still no ceiling", fnWith(&fv1.StreamingConfig{}), 0, true, fv1.StreamingAuto, def, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := resolveProxyPolicy(tc.fn, tc.fnTimeout, def)
			if p.streaming != tc.wantStream {
				t.Fatalf("streaming=%v want %v", p.streaming, tc.wantStream)
			}
			if p.protocol != tc.wantProtocol {
				t.Fatalf("protocol=%q want %q", p.protocol, tc.wantProtocol)
			}
			if p.idleTimeout != tc.wantIdle {
				t.Fatalf("idle=%v want %v", p.idleTimeout, tc.wantIdle)
			}
			if p.maxDuration != tc.wantMax {
				t.Fatalf("max=%v want %v", p.maxDuration, tc.wantMax)
			}
		})
	}
}
