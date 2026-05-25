// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// defaultWaitTimeout is used when --timeout is unset or non-positive.
const defaultWaitTimeout = 60 * time.Second

// ParseForCondition parses a --for value of the form "condition=<Type>" or
// "condition=<Type>=<Status>". The status defaults to True. It mirrors
// `kubectl wait --for=condition=...`.
func ParseForCondition(s string) (condType string, want metav1.ConditionStatus, err error) {
	rest, ok := strings.CutPrefix(s, "condition=")
	if !ok || rest == "" {
		return "", "", fmt.Errorf("invalid --for %q: expected condition=<Type> or condition=<Type>=<Status>", s)
	}
	typ, status, hasStatus := strings.Cut(rest, "=")
	if typ == "" {
		return "", "", fmt.Errorf("invalid --for %q: empty condition type", s)
	}
	want = metav1.ConditionTrue
	if hasStatus {
		switch metav1.ConditionStatus(status) {
		case metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown:
			want = metav1.ConditionStatus(status)
		default:
			return "", "", fmt.Errorf("invalid --for %q: status must be True, False or Unknown", s)
		}
	}
	return typ, want, nil
}

// WaitForCondition polls get until the named condition reaches want, or ctx is
// done. A not-found result keeps polling (the resource may appear within the
// deadline); a get error caused by ctx ending is folded into the wait result so
// the last-seen status is still reported; any other get error is returned
// immediately. interval is the poll period.
func WaitForCondition(ctx context.Context, get func(context.Context) ([]metav1.Condition, error), condType string, want metav1.ConditionStatus, interval time.Duration) error {
	lastSeen := NoneValue
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		conds, err := get(ctx)
		switch {
		case err == nil:
			if c := conditions.Find(conds, condType); c != nil {
				lastSeen = string(c.Status)
				if c.Status == want {
					return nil
				}
			}
		case IsNotFound(err):
			lastSeen = "NotFound"
		case ctx.Err() != nil:
			// The wait deadline/cancellation interrupted the in-flight get;
			// fall through to the ctx.Done() branch so we report the outcome
			// (and last-seen status) consistently rather than the raw error.
		default:
			return err
		}

		select {
		case <-ctx.Done():
			return waitTimeoutError(ctx, condType, want, lastSeen)
		case <-ticker.C:
		}
	}
}

// waitTimeoutError formats the terminal wait error, distinguishing a deadline
// from an explicit cancellation so the message is accurate.
func waitTimeoutError(ctx context.Context, condType string, want metav1.ConditionStatus, lastSeen string) error {
	verb := "timed out"
	if errors.Is(ctx.Err(), context.Canceled) {
		verb = "canceled while"
	}
	return fmt.Errorf("%s waiting for condition %q=%q (last seen %q): %w", verb, condType, want, lastSeen, ctx.Err())
}

// RunWait is the shared glue for every resource's `wait` subcommand: it parses
// --for / --timeout, polls get until the condition is met (or the deadline),
// and prints the outcome. get fetches the target resource's Status.Conditions.
func RunWait(input cli.Input, kind, name string, get func(context.Context) ([]metav1.Condition, error)) error {
	condType, want, err := ParseForCondition(input.String(flagkey.WaitFor))
	if err != nil {
		return err
	}
	timeout := input.Duration(flagkey.WaitTimeout)
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}

	ctx, cancel := context.WithTimeout(input.Context(), timeout)
	defer cancel()

	if err := WaitForCondition(ctx, get, condType, want, time.Second); err != nil {
		return fmt.Errorf("%s/%s: %w", kind, name, err)
	}
	fmt.Printf("%s/%s condition met: %s=%s\n", kind, name, condType, want)
	return nil
}
