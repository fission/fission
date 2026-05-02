//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestBuilderMgr is the Go port of test/tests/test_buildermgr.sh. It exercises
// the buildermgr / source-package path: create a Python env with builder,
// upload a zipped source package, wait for the build, hit the route, then
// trigger a rebuild via `fn update --src` and re-verify.
func TestBuilderMgr(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	pyImage := f.Images().RequirePython(t)
	builderImage := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)

	envName := "python-" + ns.ID
	fnName := "python-srcbuild-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name:    envName,
		Image:   pyImage,
		Builder: builderImage,
	})
	ns.WaitForBuilderReady(t, ctx, envName)

	srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg.zip")

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:       fnName,
		Env:        envName,
		Src:        srcZip,
		Entrypoint: "user.main",
		BuildCmd:   "./build.sh",
	})

	ns.CreateRoute(t, ctx, framework.RouteOptions{
		Function: fnName,
		URL:      routePath,
		Method:   "GET",
	})

	pkgName := ns.FunctionPackageName(t, ctx, fnName)
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
	require.Contains(t, body, "a: 1", "router response should contain rendered yaml document")
	require.Contains(t, body, "c: 3", "router response should contain rendered yaml document")
	require.Contains(t, body, "d: 4", "router response should contain rendered yaml document")

	t.Run("rebuild_on_update", func(t *testing.T) {
		// fn update --src patches the same Package CR in place — the name
		// stays stable. Capture the build timestamp before the patch so
		// WaitForPackageRebuiltSince can ignore the stale `succeeded` state
		// from the initial build.
		prevBuildTs := ns.PackageBuildTimestamp(t, ctx, pkgName)
		ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--src", srcZip)

		ns.WaitForPackageRebuiltSince(t, ctx, pkgName, prevBuildTs)

		body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
		require.Contains(t, body, "a: 1", "router response should still contain rendered yaml document after rebuild")
	})
}
