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

	pkg1 := ns.FunctionPackageName(t, ctx, fnName)
	ns.WaitForPackageBuildSucceeded(t, ctx, pkg1)

	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
	require.Contains(t, body, "a: 1", "router response should contain rendered yaml document")
	require.Contains(t, body, "c: 3", "router response should contain rendered yaml document")
	require.Contains(t, body, "d: 4", "router response should contain rendered yaml document")

	t.Run("rebuild_on_update", func(t *testing.T) {
		ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--src", srcZip)

		// fn update writes a new package; poll until the function points at
		// it (different from pkg1) and that new package builds successfully.
		framework.Eventually(t, ctx, 30*time.Second, 1*time.Second, func(c context.Context) (bool, error) {
			cur := ns.FunctionPackageName(t, c, fnName)
			return cur != pkg1, nil
		}, "function %q never switched to a fresh package after update", fnName)

		pkg2 := ns.FunctionPackageName(t, ctx, fnName)
		ns.WaitForPackageBuildSucceeded(t, ctx, pkg2)

		body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("a: 1"))
		require.Contains(t, body, "a: 1", "router response should still contain rendered yaml document after rebuild")
	})
}
