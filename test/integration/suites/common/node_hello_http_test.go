//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestNodeHelloHTTP is the Go port of test/tests/test_node_hello_http.sh.
// It creates a Node.js environment, deploys a hello-world function, attaches
// an HTTP route, and verifies that GETting the route returns "hello".
func TestNodeHelloHTTP(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)

	envName := "nodejs-" + ns.ID
	fnName := "nodejs-hello-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name:  envName,
		Image: image,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName,
		Env:  envName,
		Code: codePath,
	})

	ns.CreateRoute(t, ctx, framework.RouteOptions{
		Function: fnName,
		URL:      routePath,
		Method:   "GET",
	})

	ns.WaitForFunction(t, ctx, fnName)

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	require.Contains(t, body, "hello", "router response did not contain expected substring")
}
