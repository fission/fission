//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestGoEnv is the Go port of test/tests/test_environments/test_go_env.sh.
// Two scenarios share one Environment + builder:
//
//  1. Single-file source: hello.go returns "Hello, world!". Build it,
//     wire up both a poolmgr fn and a newdeploy fn pointing at the same
//     package, hit each via HTTP and assert "Hello".
//  2. Multi-file Go module: zip up module-example/ (main.go + go.mod +
//     go.sum, depending on golang.org/x/example), build it, swap both
//     fns to the new package via `fn update --pkg`, and assert "Vendor".
//
// Two consecutive package builds + a go-mod download in CI; budget the
// test ctx accordingly.
func TestGoEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireGo(t)
	builder := f.Images().RequireGoBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "go-" + ns.ID
	fnPM := "fn-go-pm-" + ns.ID
	fnND := "fn-go-nd-" + ns.ID

	// CreateEnv pre-waits for builder + runtime pods Ready when Builder
	// is set, so the immediate-next package build won't race the fetcher.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder, Period: 5,
	})

	// Phase 1 — single-file source.
	pkgV1 := "go-pkg-v1-" + ns.ID
	helloPath := framework.WriteTestData(t, "go/hello_world/hello.go")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgV1, Env: envName, Src: helloPath,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgV1)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnPM, Env: envName, Pkg: pkgV1, Entrypoint: "Handler",
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnND, Env: envName, Pkg: pkgV1, Entrypoint: "Handler",
		ExecutorType: "newdeploy",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnPM, URL: "/" + fnPM, Method: "GET"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "GET"})

	bodyPM := f.Router(t).GetEventually(t, ctx, "/"+fnPM, framework.BodyContains("Hello"))
	require.Contains(t, bodyPM, "Hello")
	bodyND := f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Hello"))
	require.Contains(t, bodyND, "Hello")

	// Phase 2 — multi-file go module. main.go + go.mod + go.sum at the
	// archive root (no top-level dir; mirrors `cd module-example && zip -r out *`).
	pkgV2 := "go-pkg-v2-" + ns.ID
	modZip := framework.ZipTestDataDir(t, "go/module_example", "module.zip")
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgV2, Env: envName, Src: modZip,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgV2)

	ns.CLI(t, ctx, "fn", "update", "--name", fnPM, "--pkg", pkgV2)
	ns.CLI(t, ctx, "fn", "update", "--name", fnND, "--pkg", pkgV2)

	bodyPM = f.Router(t).GetEventually(t, ctx, "/"+fnPM, framework.BodyContains("Vendor"))
	require.Contains(t, bodyPM, "Vendor")
	bodyND = f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Vendor"))
	require.Contains(t, bodyND, "Vendor")
}
