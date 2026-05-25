// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestParseForCondition(t *testing.T) {
	tests := []struct {
		in       string
		wantType string
		wantSt   metav1.ConditionStatus
		wantErr  bool
	}{
		{"condition=Ready", "Ready", metav1.ConditionTrue, false},
		{"condition=Ready=True", "Ready", metav1.ConditionTrue, false},
		{"condition=Ready=False", "Ready", metav1.ConditionFalse, false},
		{"condition=BuildSucceeded=Unknown", "BuildSucceeded", metav1.ConditionUnknown, false},
		{"", "", "", true},
		{"Ready", "", "", true},                 // missing condition= prefix
		{"condition=", "", "", true},            // empty type
		{"condition=Ready=Maybe", "", "", true}, // bad status
	}
	for _, tt := range tests {
		typ, st, err := ParseForCondition(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseForCondition(%q) err=%v wantErr=%v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && (typ != tt.wantType || st != tt.wantSt) {
			t.Errorf("ParseForCondition(%q) = (%q,%q), want (%q,%q)", tt.in, typ, st, tt.wantType, tt.wantSt)
		}
	}
}

func conds(status metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{{Type: "Ready", Status: status}}
}

func TestWaitForCondition(t *testing.T) {
	ctx := context.Background()

	t.Run("met immediately", func(t *testing.T) {
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionTrue), nil }
		if err := WaitForCondition(ctx, get, "Ready", metav1.ConditionTrue, time.Millisecond); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
	})

	t.Run("met after a few polls", func(t *testing.T) {
		n := 0
		get := func(context.Context) ([]metav1.Condition, error) {
			n++
			if n < 3 {
				return conds(metav1.ConditionFalse), nil
			}
			return conds(metav1.ConditionTrue), nil
		}
		if err := WaitForCondition(ctx, get, "Ready", metav1.ConditionTrue, time.Millisecond); err != nil {
			t.Fatalf("expected eventual success, got %v", err)
		}
		if n < 3 {
			t.Fatalf("expected at least 3 polls, got %d", n)
		}
	})

	t.Run("keeps polling through NotFound", func(t *testing.T) {
		n := 0
		get := func(context.Context) ([]metav1.Condition, error) {
			n++
			if n < 2 {
				return nil, fmt.Errorf("functions.fission.io %q not found", "x")
			}
			return conds(metav1.ConditionTrue), nil
		}
		if err := WaitForCondition(ctx, get, "Ready", metav1.ConditionTrue, time.Millisecond); err != nil {
			t.Fatalf("not-found should not abort the wait, got %v", err)
		}
	})

	t.Run("timeout reports last seen", func(t *testing.T) {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionFalse), nil }
		err := WaitForCondition(tctx, get, "Ready", metav1.ConditionTrue, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "last seen \"False\"") {
			t.Fatalf("expected timeout mentioning last seen False, got %v", err)
		}
	})

	t.Run("non-notfound error returned immediately", func(t *testing.T) {
		get := func(context.Context) ([]metav1.Condition, error) { return nil, fmt.Errorf("boom") }
		if err := WaitForCondition(ctx, get, "Ready", metav1.ConditionTrue, time.Millisecond); err == nil || err.Error() != "boom" {
			t.Fatalf("expected the raw error, got %v", err)
		}
	})
}

func TestRunWait(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.WaitFor, "condition=Ready")
		in.Set(flagkey.WaitTimeout, 2*time.Second)
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionTrue), nil }
		if err := RunWait(in, "Function", "hello", get); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
	})

	t.Run("invalid --for", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.WaitFor, "Ready")
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionTrue), nil }
		if err := RunWait(in, "Function", "hello", get); err == nil {
			t.Fatal("expected an error for invalid --for")
		}
	})

	t.Run("timeout wraps kind/name", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.WaitFor, "condition=Ready")
		in.Set(flagkey.WaitTimeout, 20*time.Millisecond)
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionFalse), nil }
		err := RunWait(in, "Function", "hello", get)
		if err == nil || !strings.Contains(err.Error(), "Function/hello") {
			t.Fatalf("expected timeout error mentioning Function/hello, got %v", err)
		}
	})
}
