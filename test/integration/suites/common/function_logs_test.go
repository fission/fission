//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionLogs is the Go port of test/tests/test_logging/test_function_logs.sh.
// The function logs "log test" via console.log on each invocation; after 4
// GETs we verify `fission function logs --detail` reports 4 occurrences. The
// bash test fixed-sleeps 60s for log aggregation; we poll instead.
func TestFunctionLogs(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-logs-" + ns.ID
	fnName := "logtest-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	codePath := framework.WriteTestData(t, "nodejs/log/log.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// Wait for the function to be reachable, then drive 4 invocations.
	r := f.Router(t)
	r.GetEventually(t, ctx, routePath, framework.BodyContains("log test"))
	for i := 0; i < 3; i++ { // GetEventually above already counts as one
		status, _, err := r.Get(ctx, routePath)
		require.NoErrorf(t, err, "extra GET #%d", i)
		require.Equalf(t, 200, status, "extra GET #%d should be 200", i)
	}

	// Poll function pod logs until 4 "log test" lines are observed. We use
	// the dedicated FunctionLogs helper (kubeClient-driven) rather than
	// `fission function logs` via ns.CLI: the CLI streams pod logs to
	// os.Stdout directly, which our in-process CLI helper does not capture
	// (it only routes cobra's SetOut/SetErr).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		out := ns.FunctionLogs(t, ctx, fnName)
		count := strings.Count(out, "log test")
		assert.GreaterOrEqualf(c, count, 4,
			"function logs should report >= 4 'log test' lines, got %d:\n%s", count, out)
	}, 1*time.Minute, 2*time.Second)
}
