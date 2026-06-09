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
		{"classic nil", fnWith(nil), 30 * time.Second, false, "", 0, 30 * time.Second},
		{"classic disabled", fnWith(&fv1.StreamingConfig{Enabled: false}), 30 * time.Second, false, "", 0, 30 * time.Second},
		{"streaming defaults", fnWith(&fv1.StreamingConfig{Enabled: true}), 30 * time.Second, true, fv1.StreamingAuto, def, 30 * time.Second},
		{"streaming explicit protocol", fnWith(&fv1.StreamingConfig{Enabled: true, Protocol: fv1.StreamingSSE}), 30 * time.Second, true, fv1.StreamingSSE, def, 30 * time.Second},
		{"streaming idle override", fnWith(&fv1.StreamingConfig{Enabled: true, IdleTimeoutSeconds: 15}), 30 * time.Second, true, fv1.StreamingAuto, 15 * time.Second, 30 * time.Second},
		{"streaming max override", fnWith(&fv1.StreamingConfig{Enabled: true, MaxDurationSeconds: 600}), 30 * time.Second, true, fv1.StreamingAuto, def, 600 * time.Second},
		{"streaming no ceiling", fnWith(&fv1.StreamingConfig{Enabled: true}), 0, true, fv1.StreamingAuto, def, 0},
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
