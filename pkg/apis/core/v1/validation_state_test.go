// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"strings"
	"testing"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStateConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     StateConfig
		wantErr bool
	}{
		{"empty ok (keyspace defaults to fn name)", StateConfig{}, false},
		{"valid keyspace", StateConfig{Keyspace: "cart-v2.prod"}, false},
		{"keyspace uppercase rejected", StateConfig{Keyspace: "Cart"}, true},
		{"keyspace underscore rejected", StateConfig{Keyspace: "a_b"}, true},
		{"keyspace colon rejected", StateConfig{Keyspace: "a:b"}, true},
		{"keyspace hash rejected", StateConfig{Keyspace: "a#meta"}, true},
		{"keyspace leading dash rejected", StateConfig{Keyspace: "-a"}, true},
		{"keyspace trailing dot rejected", StateConfig{Keyspace: "a."}, true},
		{"keyspace too long", StateConfig{Keyspace: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, true},
		{"negative maxKeys rejected", StateConfig{MaxKeys: -1}, true},
		{"negative maxValueBytes rejected", StateConfig{MaxValueBytes: -5}, true},
		{"positive quotas ok", StateConfig{MaxKeys: 100, MaxValueBytes: 1024}, false},
		{"negative defaultTTL rejected", StateConfig{DefaultTTL: &metav1.Duration{Duration: -time.Second}}, true},
		{"positive defaultTTL ok", StateConfig{DefaultTTL: &metav1.Duration{Duration: time.Minute}}, false},
		{"sticky header ok", StateConfig{Sticky: &StickyConfig{Source: StickySourceHeader, Name: "X-Session-Id"}}, false},
		{"sticky queryparam ok", StateConfig{Sticky: &StickyConfig{Source: StickySourceQueryParam, Name: "session"}}, false},
		{"sticky bad source rejected", StateConfig{Sticky: &StickyConfig{Source: "cookie", Name: "sid"}}, true},
		{"sticky missing name rejected", StateConfig{Sticky: &StickyConfig{Source: StickySourceHeader}}, true},
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

// TestFunctionSpecValidateState asserts FunctionSpec.Validate surfaces a bad
// State config, rejects the container executor combination (no fetcher sidecar
// to deliver a scoped token), and accepts nil State (backward-compat guard).
func TestFunctionSpecValidateState(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("nil state ok", func(t *testing.T) {
		t.Parallel()
		spec := base()
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid state surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.State = &StateConfig{Keyspace: "Bad_Keyspace"}
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for invalid keyspace")
		}
	})

	t.Run("container executor rejected", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.State = &StateConfig{}
		spec.PodSpec = &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "c", Image: "img"}}}
		spec.InvokeStrategy = InvokeStrategy{
			ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypeContainer, MaxScale: 1},
			StrategyType:      StrategyTypeExecution,
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "state API requires") {
			t.Fatalf("expected state-with-container-executor error, got: %v", err)
		}
	})
}

func TestStateConfigEffectiveDefaults(t *testing.T) {
	t.Parallel()

	sc := StateConfig{}
	if got := sc.EffectiveKeyspace("counter"); got != "counter" {
		t.Fatalf("EffectiveKeyspace default = %q, want fn name", got)
	}
	if got := sc.EffectiveMaxValueBytes(); got != DefaultStateMaxValueBytes {
		t.Fatalf("EffectiveMaxValueBytes default = %d", got)
	}
	if got := sc.EffectiveMaxKeys(); got != DefaultStateMaxKeys {
		t.Fatalf("EffectiveMaxKeys default = %d", got)
	}

	sc = StateConfig{Keyspace: "ks", MaxValueBytes: 1024, MaxKeys: 5}
	if got := sc.EffectiveKeyspace("counter"); got != "ks" {
		t.Fatalf("EffectiveKeyspace explicit = %q", got)
	}
	if got := sc.EffectiveMaxValueBytes(); got != 1024 {
		t.Fatalf("EffectiveMaxValueBytes explicit = %d", got)
	}
	if got := sc.EffectiveMaxKeys(); got != 5 {
		t.Fatalf("EffectiveMaxKeys explicit = %d", got)
	}
}
