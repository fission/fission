//go:build integration

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// Eventually polls the condition function until it returns true or the timeout
// elapses. The condition is run immediately. Failure messages support fmt-style
// formatting.
//
// This is a thin wrapper over wait.PollUntilContextTimeout with t-aware
// failure semantics: timeout or condition error becomes t.Fatal.
//
// Note: failArgs are evaluated at call time, not at failure time. If the
// failure message must include polling-loop state captured by the closure,
// use EventuallyLazy instead.
func Eventually(
	t *testing.T,
	ctx context.Context,
	timeout, interval time.Duration,
	cond func(ctx context.Context) (done bool, err error),
	failMsg string, failArgs ...any,
) {
	t.Helper()
	EventuallyLazy(t, ctx, timeout, interval, cond, func() string {
		return fmt.Sprintf(failMsg, failArgs...)
	})
}

// EventuallyLazy is like Eventually but defers the failure message
// construction until the timeout fires. Use this when the message needs to
// reference state mutated inside the polling loop (e.g. "last observed
// weight"); fmt-style args evaluated at call time would capture only the
// initial values.
func EventuallyLazy(
	t *testing.T,
	ctx context.Context,
	timeout, interval time.Duration,
	cond func(ctx context.Context) (done bool, err error),
	failMsg func() string,
) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, cond)
	if err != nil {
		t.Fatalf("Eventually: %s: %v", failMsg(), err)
	}
}
