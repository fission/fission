// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
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
// deadline); any other get error is returned immediately. On timeout it reports
// the last observed status. interval is the poll period.
func WaitForCondition(ctx context.Context, get func(context.Context) ([]metav1.Condition, error), condType string, want metav1.ConditionStatus, interval time.Duration) error {
	lastSeen := "<none>"
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
		default:
			return err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for condition %q=%q (last seen %q): %w", condType, want, lastSeen, ctx.Err())
		case <-time.After(interval):
		}
	}
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
