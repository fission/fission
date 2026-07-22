// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestJVMJerseyEnv covers the jvm-jersey environment via two paths, merged into
// one test so the Jersey coverage lives in a single file:
//
//   - builder: zip the vendored Jersey hello-world Maven project (pom.xml +
//     src/main/..., test sources omitted so the in-cluster `mvn package` has
//     nothing to run) and let the Jersey builder pod produce the fat jar.
//     Needs JVM_JERSEY_RUNTIME_IMAGE + JVM_JERSEY_BUILDER_IMAGE.
//   - deploy_jar: deploy a pre-built fat jar directly (no build) and additionally
//     exercise a newdeploy POST echo. Needs JVM_JERSEY_RUNTIME_IMAGE +
//     JVM_JERSEY_JAR_PATH (CI builds the jar inside the builder image; see the
//     "Go integration tests" step in .github/workflows/push_pr.yaml).
//
// io.fission.HelloWorld returns "Hello World!" on GET and "Echo: <body>" on
// other methods.
func TestJVMJerseyEnv(t *testing.T) {
	t.Parallel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVMJersey(t)
	ns := f.NewTestNamespace(t)

	t.Run("builder", func(t *testing.T) {
		builder := f.Images().RequireJVMJerseyBuilder(t)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		envName := "jersey-bld-" + ns.ID
		pkgName := "jerseypkg-" + ns.ID
		fnP := "jerseybld-pm-" + ns.ID
		fnND := "jerseybld-nd-" + ns.ID

		// CreateEnv auto-waits for the builder pod + EndpointSlice to publish.
		// KeepArchive is required for JVM: the builder ships a .jar (a zip), and
		// without it the fetcher unzips it into a directory that the runtime
		// can't open as a JarFile.
		ns.CreateEnv(t, ctx, framework.EnvOptions{
			Name: envName, Image: runtime, Builder: builder, KeepArchive: true,
		})

		// pom.xml at top level + src/main/java/io/fission/HelloWorld.java;
		// ZipTestDataTree preserves the layout so Maven finds the source.
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
	})

	t.Run("deploy_jar", func(t *testing.T) {
		jarPath := os.Getenv("JVM_JERSEY_JAR_PATH")
		if jarPath == "" {
			t.Skip("JVM_JERSEY_JAR_PATH is not set; skipping (build the jersey jar via maven and point the env var at the .jar)")
		}
		if _, err := os.Stat(jarPath); err != nil {
			t.Skipf("JVM_JERSEY_JAR_PATH=%q not accessible: %v", jarPath, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		envName := "jersey-dep-" + ns.ID
		fnP := "jersey-pm-" + ns.ID
		fnND := "jersey-nd-" + ns.ID
		fnPost := "jersey-post-" + ns.ID

		// KeepArchive keeps the .jar a single file (the fetcher would otherwise
		// unzip it into a directory the runtime can't open as a JarFile).
		ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime, KeepArchive: true})

		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnP, Env: envName, Deploy: jarPath, Entrypoint: "io.fission.HelloWorld",
		})
		ns.CreateFunction(t, ctx, framework.FunctionOptions{
			Name: fnND, Env: envName, Deploy: jarPath, Entrypoint: "io.fission.HelloWorld",
			ExecutorType: "newdeploy",
		})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnP, URL: "/" + fnP, Method: "GET"})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, URL: "/" + fnND, Method: "GET"})
		ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnND, Name: fnPost, URL: "/" + fnPost, Method: "POST"})

		bodyP := f.Router(t).GetEventually(t, ctx, "/"+fnP, framework.BodyContains("Hello"))
		require.True(t, strings.Contains(bodyP, "Hello"),
			"poolmgr fn %q response missing 'Hello': %q", fnP, bodyP)
		bodyND := f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Hello"))
		require.True(t, strings.Contains(bodyND, "Hello"),
			"newdeploy fn %q response missing 'Hello': %q", fnND, bodyND)

		xml := []byte(`<?xml version="1.0"?><catalog><book id="bk101"><title>XML Developer's Guide</title></book></catalog>`)
		echo := f.Router(t).PostEventually(t, ctx, "/"+fnPost, "application/xml", xml,
			framework.BodyContains("Echo"))
		require.True(t, strings.Contains(echo, "Echo"),
			"jersey newdeploy POST echo missing 'Echo': %q", echo)
	})
}
