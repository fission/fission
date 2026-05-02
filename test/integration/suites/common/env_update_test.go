//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestEnvUpdate is the Go port of test_fn_update/test_env_update.sh.
// Creates a function against env-old, then updates the function to use a
// freshly-created env-new and asserts it still serves traffic.
func TestEnvUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envOld := "python-old-" + ns.ID
	envNew := "python-new-" + ns.ID
	fnName := "fn-envupdate-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envOld, Image: image})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envOld, Code: codePath,
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 4,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envNew, Image: image})
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--env", envNew, "--code", codePath,
		"--executortype", "newdeploy", "--minscale", "1", "--maxscale", "4",
		"--mincpu", "20", "--maxcpu", "100", "--minmemory", "128", "--maxmemory", "256")

	// Spec.Environment.Name should reflect the new env.
	fn := ns.GetFunction(t, ctx, fnName)
	require.Equal(t, envNew, fn.Spec.Environment.Name)

	// Function still serves traffic.
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
}
