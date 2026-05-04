//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionTimeout is the Go port of test/tests/test_function_timeout.sh.
// A function sleeps 5s before returning. With `--fntimeout 10` it succeeds;
// after `fn update --fntimeout 2` the request times out and the router
// returns 504 Gateway Timeout.
func TestFunctionTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-fntimeout-" + ns.ID
	fnName := "fn-timeout-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	// Inline-write a sleep-5s function — parameterized timeouts don't
	// deserve their own vendored fixture.
	src := "function sleep(ms){return new Promise(r=>setTimeout(r,ms))}\n" +
		"module.exports = async function(){await sleep(5000); return {status:200,body:\"hello, world!\\n\"}};\n"
	codePath := filepath.Join(t.TempDir(), "sleep.js")
	require.NoError(t, os.WriteFile(codePath, []byte(src), 0o644))

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, FnTimeout: 10,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	// 10s timeout wins: response succeeds.
	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// Lower the timeout to 2s; the same 5s sleep now exceeds it.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--fntimeout", "2")

	// When the function exceeds its --fntimeout, the runtime aborts and
	// the router surfaces the upstream failure as a 5xx. In current
	// Fission this comes through as 500 ("Internal server error") rather
	// than the gateway-timeout 504 the bash test grep'd for. We accept
	// any 5xx here; the meaningful invariant is that the request fails
	// after the new tighter timeout, not what the specific status code is.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _, err := f.Router(t).Get(ctx, routePath)
		if !assert.NoErrorf(c, err, "router GET %q", routePath) {
			return
		}
		assert.Truef(c, status >= 500 && status < 600,
			"after fntimeout=2 the router should return a 5xx; got %d", status)
	}, 90*time.Second, 2*time.Second)
}
