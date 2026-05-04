//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestCreateFunctionWithURL is the Go port of test/tests/test_create_fn_with_url.sh.
// It exercises the CLI's `--code <url>` path, where the CLI fetches the URL,
// base64-encodes the content, and stores it as a Package literal. We don't
// re-verify the literal byte-for-byte (bash did, but that's a CLI internal);
// we verify the function is invocable end-to-end.
//
// The URL is the same one bash used (raw GitHub from fission/examples). If
// the URL becomes unreachable, this test will fail at fn-create — that's
// real signal for the user, not flakiness.
func TestCreateFunctionWithURL(t *testing.T) {
	t.Parallel()

	const codeURL = "https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-urlfn-" + ns.ID
	fnName := "fn-url-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: codeURL, // CLI accepts a URL here and inlines the response.
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
}
