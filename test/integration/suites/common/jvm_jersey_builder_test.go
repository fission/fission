// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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

// TestJVMJerseyBuilder exercises the jvm-jersey environment via its *builder*
// image — no pre-built jar required (unlike TestJVMJerseyEnv, which deploys a
// jar and stays skipped without JVM_JERSEY_JAR_PATH).
//
// We zip the vendored Jersey hello-world Maven project (pom.xml + src/main/...,
// test sources omitted so the in-cluster `mvn package` has nothing to run),
// upload it as a source archive, and let the Jersey builder pod produce the fat
// jar. Both poolmgr and newdeploy functions then reference the resulting
// package and return io.fission.HelloWorld's "Hello World!".
//
// t.Skips unless JVM_JERSEY_RUNTIME_IMAGE *and* JVM_JERSEY_BUILDER_IMAGE are
// set. When both are set, the entire build runs inside the cluster; the test
// runner needs neither maven nor docker. The first build downloads Maven
// dependencies, so budget a generous ctx.
func TestJVMJerseyBuilder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVMJersey(t)
	builder := f.Images().RequireJVMJerseyBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "jersey-bld-" + ns.ID
	pkgName := "jerseypkg-" + ns.ID
	fnP := "jerseybld-pm-" + ns.ID
	fnND := "jerseybld-nd-" + ns.ID

	// CreateEnv auto-waits for the builder pod + EndpointSlice to publish.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder,
	})

	// Source archive — pom.xml at top level + src/main/java/io/fission/HelloWorld.java.
	// ZipTestDataTree preserves the `src/main/java/...` path so Maven finds the
	// source where it expects it.
	srcZip := framework.ZipTestDataTree(t, "jvm_jersey/hello_world", "jersey-src-pkg.zip")

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
