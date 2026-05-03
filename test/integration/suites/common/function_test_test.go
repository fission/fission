//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionTest is the Go port of test_function_test/test_fn_test.sh.
// (was bash-disabled per the original CI flakiness around invalid functions
// — see https://github.com/fission/fission/issues/653).
//
// `fission fn test` just GETs /fission-function/<fn> on the router, so we
// hit the same internal route directly. Two cases:
//
//   - valid hello.js → returns "Hello, Fission"
//   - invalid errhello.js (syntax error: `aasync function`) → the runtime
//     fails to load the module and surfaces the JS SyntaxError in the body.
//
// The invalid-fn case is the one that historically flaked; we use
// PostEventually-style polling so a transient empty response retries.
func TestFunctionTest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-fntest-" + ns.ID
	validFn := "fnt-valid-" + ns.ID
	invalidFn := "fnt-invalid-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	// Valid function — usual path.
	validCode := framework.WriteTestData(t, "nodejs/fn_test/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: validFn, Env: envName, Code: validCode,
	})
	body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+validFn,
		framework.BodyContains("Hello, Fission"))
	require.True(t, strings.Contains(body, "Hello, Fission"),
		"valid function body missing 'Hello, Fission': %q", body)

	// Invalid function — bash flagged this as flaky; the runtime takes a
	// few attempts to surface SyntaxError reliably. Polling via
	// GetEventually with a body-contains check matches the bash retry loop.
	invalidCode := framework.WriteTestData(t, "nodejs/fn_test/errhello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: invalidFn, Env: envName, Code: invalidCode,
	})
	got := f.Router(t).GetEventually(t, ctx, "/fission-function/"+invalidFn,
		errorBodyCheck())
	require.True(t, strings.Contains(got, "SyntaxError") ||
		strings.Contains(strings.ToLower(got), "error"),
		"invalid function body should mention an error, got %q", got)
}

// errorBodyCheck succeeds on any non-empty response that mentions an error.
// Status code may be 5xx (function failed to load) or 2xx (runtime swallowed
// the error and returned a stub) — we just need *something* showing the
// SyntaxError message.
func errorBodyCheck() framework.ResponseCheck {
	return func(status int, body string) bool {
		if body == "" {
			return false
		}
		low := strings.ToLower(body)
		return strings.Contains(low, "syntaxerror") ||
			strings.Contains(low, "error")
	}
}
