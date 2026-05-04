//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionUpdate is the Go port of test/tests/test_function_update.sh. It
// verifies the router updates its cache when a function's code changes —
// while sustained traffic is hitting the route. The bash version backgrounds
// `watch -n1 curl` to emulate online traffic; we use the framework's LoadLoop
// instead.
func TestFunctionUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-fnupdate-" + ns.ID
	fnName := "fn-update-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fooPath := writeNodeReturning(t, "foo", "foo!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: fooPath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("foo"))
	require.Contains(t, body, "foo")

	// Sustained traffic during the update so we exercise the router's
	// cache-update path under load (the original purpose of the bash test's
	// background `watch -n1 curl`).
	loadCtx, stopLoad := context.WithCancel(ctx)
	t.Cleanup(stopLoad)
	go f.Router(t).LoadLoop(loadCtx, routePath)

	barPath := writeNodeReturning(t, "bar", "bar!\n")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", barPath)

	body = f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("bar"))
	require.Contains(t, body, "bar", "router should serve updated function code")
}

// writeNodeReturning writes a Node.js function that returns the given body
// with HTTP 200, and returns the on-disk path. The unique-name parameter
// prevents file collisions if the helper is called twice in one test.
func writeNodeReturning(t *testing.T, fileName, body string) string {
	t.Helper()
	src := "module.exports = function(context, callback) { callback(200, " + jsString(body) + "); };\n"
	p := filepath.Join(t.TempDir(), fileName+".js")
	require.NoErrorf(t, os.WriteFile(p, []byte(src), 0o644), "write %q", p)
	return p
}

// jsString is a tiny escaper for short literal payloads in JS source.
// Tests pass simple ASCII; we just quote-and-escape backslash + double-quote.
func jsString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '\\', '"':
			out = append(out, '\\', byte(r))
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, byte(r))
		}
	}
	out = append(out, '"')
	return string(out)
}
