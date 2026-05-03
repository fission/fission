//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestBackendNewdeploy is the Go port of test_backend_newdeploy.sh. Two
// functions on the newdeploy executor — one with MinScale=0 (cold start),
// one with MinScale=1 (warm start). Each should return hello over HTTP.
func TestBackendNewdeploy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-ndbk-" + ns.ID
	fnCold := "fn-nd-cold-" + ns.ID
	fnWarm := "fn-nd-warm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")

	t.Run("cold_start", func(t *testing.T) {
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnCold, Env: envName, Code: codePath,
			ExecutorType: "newdeploy", MinScale: 0, MaxScale: 4,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnCold, URL: "/" + fnCold, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnCold, framework.BodyContains("hello"))
		require.Contains(t, body, "hello")
	})

	t.Run("warm_start", func(t *testing.T) {
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnWarm, Env: envName, Code: codePath,
			ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnWarm, URL: "/" + fnWarm, Method: "GET"})
		body := f.Router(t).GetEventually(t, ctx, "/"+fnWarm, framework.BodyContains("hello"))
		require.Contains(t, body, "hello")
	})
}
