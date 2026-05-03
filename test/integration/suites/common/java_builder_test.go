//go:build integration

package common_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestJavaBuilder is the Go port of test_environments/test_java_builder.sh.
//
// Unlike test_java_env, this exercises the *Java builder image* path —
// no pre-built jar required. We zip the vendored hello-world Maven
// project (pom.xml + src/...), upload as a source archive, and let the
// JVM builder pod run `mvn package` to produce the fat jar. Then both
// poolmgr and newdeploy functions reference the resulting package.
//
// t.Skips unless JVM_RUNTIME_IMAGE *and* JVM_BUILDER_IMAGE are set.
// When both are set, the entire build runs inside the cluster; the
// test runner doesn't need maven or docker.
//
// Build is slow (Maven downloads dependencies on first run); 8-minute
// ctx timeout matches the bash version's `timeout 400 bash -c waitBuild`
// plus per-fn HTTP polling.
func TestJavaBuilder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVM(t)
	builder := f.Images().RequireJVMBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "java-bld-" + ns.ID
	pkgName := "javapkg-" + ns.ID
	fnP := "javabld-pm-" + ns.ID
	fnND := "javabld-nd-" + ns.ID

	// CreateEnv auto-waits for the builder pod + EndpointSlice to
	// publish; the JVM env spec sets keeparchive=true in the bash
	// version (so the builder ships the jar through the storage
	// service), which is the framework-default behavior here.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder,
	})

	// Source archive — pom.xml at top level + src/main/java/io/fission/HelloWorld.java.
	// ZipTestDataTree preserves the `src/main/java/...` path so Maven
	// finds the source where it expects it.
	srcZip := framework.ZipTestDataTree(t, "java/hello_world", "java-src-pkg.zip")

	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkgName, Env: envName, Src: srcZip,
	})
	ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnP, Env: envName, Pkg: pkgName, Entrypoint: "io.fission.HelloWorld",
	})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnND, Env: envName, Pkg: pkgName, Entrypoint: "io.fission.HelloWorld",
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 1,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnP, URL: "/" + fnP, Method: "GET"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "GET"})

	bodyP := f.Router(t).GetEventually(t, ctx, "/"+fnP, framework.BodyContains("Hello"))
	require.True(t, strings.Contains(bodyP, "Hello"),
		"poolmgr fn %q response missing 'Hello': %q", fnP, bodyP)

	bodyND := f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Hello"))
	require.True(t, strings.Contains(bodyND, "Hello"),
		"newdeploy fn %q response missing 'Hello': %q", fnND, bodyND)
}
