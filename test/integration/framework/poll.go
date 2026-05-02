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
func Eventually(
	t *testing.T,
	ctx context.Context,
	timeout, interval time.Duration,
	cond func(ctx context.Context) (done bool, err error),
	failMsg string, failArgs ...any,
) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, cond)
	if err != nil {
		t.Fatalf("Eventually: %s: %v", fmt.Sprintf(failMsg, failArgs...), err)
	}
}
