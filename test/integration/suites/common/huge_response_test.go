//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestHugeResponse is the Go port of test/tests/test_huge_response/. It
// verifies that the router can carry a large response body — POSTs a
// pre-generated 240KB JSON document to a Go function that echoes the body
// back, and asserts the echo matches byte-for-byte. The bash version
// retried on truncation (a real bug class historically); we do the same via
// PostEventually with a body-equality check.
func TestHugeResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireGo(t)
	builder := f.Images().RequireGoBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "go-huge-" + ns.ID
	fnName := "echo-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name:        envName,
		Image:       runtime,
		Builder:     builder,
		GracePeriod: 5,
	})
	ns.WaitForBuilderReady(t, ctx, envName)

	codePath := framework.WriteTestData(t, "go/echo/hello.go")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:       fnName,
		Env:        envName,
		Src:        codePath,
		Entrypoint: "Handler",
	})
	pkgName := ns.FunctionPackageName(t, ctx, fnName)
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	ns.CreateRoute(t, ctx, framework.RouteOptions{
		Function: fnName,
		URL:      routePath,
		Method:   "POST",
	})

	body, err := testdata.FS.ReadFile("misc/huge_response/generated.json")
	require.NoError(t, err, "read embedded huge_response/generated.json")
	require.Greaterf(t, len(body), 100*1024, "fixture should be >100KB; got %d bytes", len(body))

	got := f.Router(t).PostEventually(t, ctx, routePath, "application/json", body,
		bodyEqualsLength(len(body)))
	require.Equal(t, string(body), got, "router POST response should echo the request body verbatim")
}

// bodyEqualsLength returns a ResponseCheck that succeeds when the response is
// 2xx and the body length matches `want`. The full byte-for-byte equality
// check happens once outside the polling loop after this filter passes —
// keeps polling cheap (length compare only) and the final assertion crisp.
func bodyEqualsLength(want int) framework.ResponseCheck {
	return func(status int, body string) bool {
		return status >= 200 && status < 300 && len(body) == want
	}
}
