// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"errors"
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

	// TestWaitForCondition/canceled reports canceled, not timed out: pins the
	// reuse-review #6 fix (PollUntil/PollDeadlineVerb) that distinguishes an
	// explicit ctx cancellation from a plain deadline.
	t.Run("canceled reports canceled, not timed out", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionFalse), nil }
		err := WaitForCondition(cctx, get, "Ready", metav1.ConditionTrue, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "canceled while") {
			t.Fatalf("expected a cancellation error, got %v", err)
		}
		if strings.Contains(err.Error(), "timed out") {
			t.Fatalf("cancellation must not be reported as a timeout, got %v", err)
		}
	})
}

// TestPollUntil exercises the shared core directly: it stops on done, on a
// check error, and distinguishes a deadline from an explicit cancellation on
// its own ctx.Done() branch.
func TestPollUntil(t *testing.T) {
	t.Run("stops when check reports done", func(t *testing.T) {
		n := 0
		check := func(context.Context) (bool, error) {
			n++
			return n >= 3, nil
		}
		if err := PollUntil(context.Background(), time.Millisecond, check); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if n != 3 {
			t.Fatalf("expected exactly 3 checks, got %d", n)
		}
	})

	t.Run("returns check's error immediately", func(t *testing.T) {
		wantErr := fmt.Errorf("boom")
		check := func(context.Context) (bool, error) { return false, wantErr }
		if err := PollUntil(context.Background(), time.Millisecond, check); err != wantErr {
			t.Fatalf("expected the raw check error, got %v", err)
		}
	})

	t.Run("deadline returns DeadlineExceeded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		check := func(context.Context) (bool, error) { return false, nil }
		err := PollUntil(ctx, time.Millisecond, check)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded, got %v", err)
		}
		if PollDeadlineVerb(ctx) != "timed out" {
			t.Fatalf("expected verb %q, got %q", "timed out", PollDeadlineVerb(ctx))
		}
	})

	t.Run("explicit cancel returns Canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		check := func(context.Context) (bool, error) { return false, nil }
		err := PollUntil(ctx, time.Millisecond, check)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled, got %v", err)
		}
		if PollDeadlineVerb(ctx) != "canceled while" {
			t.Fatalf("expected verb %q, got %q", "canceled while", PollDeadlineVerb(ctx))
		}
	})
}

func TestRunWait(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.SetString(flagkey.WaitFor, "condition=Ready")
		in.SetDuration(flagkey.WaitTimeout, 2*time.Second)
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionTrue), nil }
		if err := runWait(in, "Function", "hello", get); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
	})

	t.Run("invalid --for", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.SetString(flagkey.WaitFor, "Ready")
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionTrue), nil }
		if err := runWait(in, "Function", "hello", get); err == nil {
			t.Fatal("expected an error for invalid --for")
		}
	})

	t.Run("timeout wraps kind/name", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.SetString(flagkey.WaitFor, "condition=Ready")
		in.SetDuration(flagkey.WaitTimeout, 20*time.Millisecond)
		get := func(context.Context) ([]metav1.Condition, error) { return conds(metav1.ConditionFalse), nil }
		err := runWait(in, "Function", "hello", get)
		if err == nil || !strings.Contains(err.Error(), "Function/hello") {
			t.Fatalf("expected timeout error mentioning Function/hello, got %v", err)
		}
	})
}

// TestPollUntilTerminalErrorRacingCtxExpiry pins PollEnded's contract: a
// terminal check error unrelated to ctx surfaces verbatim and classifies as
// NOT poll-ended even when ctx expires in the same instant; PollUntil's own
// Done branch returns a *PollEndedError that does classify.
func TestPollUntilTerminalErrorRacingCtxExpiry(t *testing.T) {
	terminal := errors.New("genuine terminal failure")
	ctx, cancel := context.WithCancel(t.Context())
	err := PollUntil(ctx, time.Millisecond, func(context.Context) (bool, error) {
		cancel()
		return false, terminal
	})
	if !errors.Is(err, terminal) {
		t.Fatalf("terminal error must surface verbatim, got: %v", err)
	}
	if PollEnded(err) {
		t.Fatalf("terminal error racing ctx expiry must NOT classify as poll-ended")
	}

	expired, cancel2 := context.WithCancel(t.Context())
	cancel2()
	err = PollUntil(expired, time.Millisecond, func(context.Context) (bool, error) { return false, nil })
	var ended *PollEndedError
	if !errors.As(err, &ended) || !PollEnded(err) {
		t.Fatalf("ctx-ended poll must return *PollEndedError and classify as poll-ended, got: %v", err)
	}
}
