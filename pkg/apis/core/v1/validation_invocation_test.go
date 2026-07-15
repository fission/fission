// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInvocationConfigValidate(t *testing.T) {
	t.Parallel()
	md := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }
	tests := []struct {
		name    string
		cfg     InvocationConfig
		wantErr bool
	}{
		{"zero value ok", InvocationConfig{}, false},
		{"valid full", InvocationConfig{
			Retry:  RetryPolicy{MaxAttempts: new(3), BackoffBase: md(time.Second), BackoffCap: md(time.Minute), Jitter: new(false)},
			MaxAge: md(6 * time.Hour),
		}, false},
		{"maxAttempts zero", InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(0)}}, true},
		{"maxAttempts negative", InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(-1)}}, true},
		{"maxAttempts one ok", InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(1)}}, false},
		{"maxAttempts at cap ok", InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(MaxAsyncAttempts)}}, false},
		{"maxAttempts over cap", InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(MaxAsyncAttempts + 1)}}, true},
		{"maxAge at cap ok", InvocationConfig{MaxAge: md(MaxAsyncMaxAge)}, false},
		{"maxAge over cap", InvocationConfig{MaxAge: md(MaxAsyncMaxAge + time.Hour)}, true},
		{"negative backoffBase", InvocationConfig{Retry: RetryPolicy{BackoffBase: md(-time.Second)}}, true},
		{"negative backoffCap", InvocationConfig{Retry: RetryPolicy{BackoffCap: md(-time.Second)}}, true},
		{"cap below base", InvocationConfig{Retry: RetryPolicy{BackoffBase: md(time.Minute), BackoffCap: md(time.Second)}}, true},
		{"cap equals base ok", InvocationConfig{Retry: RetryPolicy{BackoffBase: md(time.Second), BackoffCap: md(time.Second)}}, false},
		{"zero maxAge", InvocationConfig{MaxAge: md(0)}, true},
		{"negative maxAge", InvocationConfig{MaxAge: md(-time.Hour)}, true},
		{"positive maxAge ok", InvocationConfig{MaxAge: md(time.Hour)}, false},
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

// TestFunctionSpecValidateInvocation asserts FunctionSpec.Validate surfaces a bad
// Invocation config through validateForAdmission (the webhook path) and that a nil
// Invocation is accepted (backward-compat guard).
func TestFunctionSpecValidateInvocation(t *testing.T) {
	t.Parallel()

	base := func() FunctionSpec {
		return FunctionSpec{
			Environment: EnvironmentReference{Name: "env", Namespace: "default"},
		}
	}

	t.Run("nil invocation ok", func(t *testing.T) {
		t.Parallel()
		if err := base().Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid invocation surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Invocation = &InvocationConfig{Retry: RetryPolicy{MaxAttempts: new(0)}}
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for zero MaxAttempts")
		}
	})

	t.Run("valid onSuccess function destination ok", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Invocation = &InvocationConfig{
			OnSuccess: &DestinationRef{Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "next"}},
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid onFailure destination surfaced", func(t *testing.T) {
		t.Parallel()
		spec := base()
		spec.Invocation = &InvocationConfig{OnFailure: &DestinationRef{}} // neither function nor topic
		if err := spec.Validate(); err == nil {
			t.Fatalf("expected error for empty destination")
		}
	})
}

func TestDestinationRefValidate(t *testing.T) {
	t.Parallel()
	fnRef := func(name string) *FunctionReference {
		return &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: name}
	}
	tests := []struct {
		name    string
		dest    DestinationRef
		wantErr bool
	}{
		{"function ok", DestinationRef{Function: fnRef("next")}, false},
		{"neither set", DestinationRef{}, true},
		{"both set", DestinationRef{Function: fnRef("next"), Topic: &TopicRef{MessageQueueType: "kafka", Topic: "t"}}, true},
		{"topic not yet supported", DestinationRef{Topic: &TopicRef{MessageQueueType: "kafka", Topic: "t"}}, true},
		{"function weights rejected", DestinationRef{Function: &FunctionReference{Type: FunctionReferenceTypeFunctionWeights, Name: "w"}}, true},
		{"empty function name", DestinationRef{Function: fnRef("  ")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.dest.Validate("test")
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
