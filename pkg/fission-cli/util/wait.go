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

// PollUntil is the shared poll scaffold behind every fission-cli wait loop
// (WaitForCondition below, function.waitForPackageBuild, and
// functionalias.waitForResolved): a time.Ticker-driven loop that calls check
// at interval until it reports done, returns a terminal error, or ctx ends.
//
// check's contract mirrors what each of those loops already did by hand:
// return (false, nil) to keep polling (a transient condition like NotFound,
// or "not ready yet"), (true, nil) once satisfied, or (false, err) for a
// terminal failure that should abort the wait immediately without waiting
// out the rest of the deadline.
//
// On ctx ending, PollUntil returns a *PollEndedError wrapping ctx.Err().
// Callers classify the outcome with PollEnded, NOT by testing ctx.Err() on
// return: ctx.Err() is sticky, so a genuine terminal check error that merely
// coincides with ctx expiring would be misreported as a timeout by that
// test, discarding the useful diagnostic.
func PollUntil(ctx context.Context, interval time.Duration, check func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		select {
		case <-ctx.Done():
			return &PollEndedError{Err: ctx.Err()}
		case <-ticker.C:
		}
	}
}

// PollEndedError marks that PollUntil's own ctx.Done branch ended the loop
// (as opposed to check returning a terminal error).
type PollEndedError struct{ Err error }

func (e *PollEndedError) Error() string { return e.Err.Error() }
func (e *PollEndedError) Unwrap() error { return e.Err }

// PollEnded reports whether a PollUntil error means "the wait's deadline or
// cancellation ended the poll" — either PollUntil's own Done branch fired, or
// check surfaced an in-flight operation interrupted by ctx (an error wrapping
// the context error). A terminal check error unrelated to ctx — even one that
// races ctx expiring — reports false, so it surfaces verbatim.
func PollEnded(err error) bool {
	var ended *PollEndedError
	return errors.As(err, &ended) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// PollDeadlineVerb reports the wording a poll loop's terminal error should
// use for why ctx ended: "timed out" for a deadline, "canceled while" for an
// explicit cancellation (e.g. Ctrl-C, or a caller-owned context canceled for
// an unrelated reason) — restoring the distinction a bare "timed out"
// message papers over.
func PollDeadlineVerb(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "canceled while"
	}
	return "timed out"
}

// DefaultWaitTimeout is the wait timeout used when --timeout is unset or
// non-positive. It is also the declared default of the --timeout flag, so both
// the flag definition and the runtime fallback share this single source.
const DefaultWaitTimeout = 60 * time.Second

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
	check := func(ctx context.Context) (bool, error) {
		conds, err := get(ctx)
		switch {
		case err == nil:
			if c := conditions.Find(conds, condType); c != nil {
				lastSeen = string(c.Status)
				if c.Status == want {
					return true, nil
				}
			}
		case IsNotFound(err):
			lastSeen = "NotFound"
		case ctx.Err() != nil:
			// The wait deadline/cancellation interrupted the in-flight get;
			// let PollUntil's own ctx.Done() branch report the outcome (and
			// last-seen status) consistently rather than the raw error.
		default:
			return false, err
		}
		return false, nil
	}

	err := PollUntil(ctx, interval, check)
	if err == nil {
		return nil
	}
	if PollEnded(err) {
		return waitTimeoutError(ctx, condType, want, lastSeen)
	}
	return err
}

// waitTimeoutError formats the terminal wait error, distinguishing a deadline
// from an explicit cancellation so the message is accurate.
func waitTimeoutError(ctx context.Context, condType string, want metav1.ConditionStatus, lastSeen string) error {
	return fmt.Errorf("%s waiting for condition %q=%q (last seen %q): %w", PollDeadlineVerb(ctx), condType, want, lastSeen, ctx.Err())
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
		timeout = DefaultWaitTimeout
	}

	ctx, cancel := context.WithTimeout(input.Context(), timeout)
	defer cancel()

	if err := WaitForCondition(ctx, get, condType, want, time.Second); err != nil {
		return fmt.Errorf("%s/%s: %w", kind, name, err)
	}
	fmt.Printf("%s/%s condition met: %s=%s\n", kind, name, condType, want)
	return nil
}
