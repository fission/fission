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

// TestFunctionTest is the Go port of test_function_test/test_fn_test.sh
// (was bash-disabled per https://github.com/fission/fission/issues/653,
// which describes the invalid-function specialize hang we hit again
// during the first migration attempt).
//
// `fission fn test` just GETs /fission-function/<fn> on the router, so
// we hit that internal route directly. Only the valid-function case is
// asserted: the invalid-function variant's runtime never returns a
// SyntaxError body — the specialize step fails and the router request
// hangs until http.Client.Timeout, which makes the test unstable
// regardless of poll budget. The invalid case is the bug fission#653
// was filed against; once that's fixed, restore the second assertion.
func TestFunctionTest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "node-fntest-" + ns.ID
	validFn := "fnt-valid-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

	validCode := framework.WriteTestData(t, "nodejs/fn_test/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: validFn, Env: envName, Code: validCode,
	})
	body := f.Router(t).GetEventually(t, ctx, "/fission-function/"+validFn,
		framework.BodyContains("Hello, Fission"))
	require.True(t, strings.Contains(body, "Hello, Fission"),
		"valid function body missing 'Hello, Fission': %q", body)
}
