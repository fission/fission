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

// TestJavaEnv covers the JVM (Spring Boot) environment via two paths, merged
// into one test so the Java coverage lives in a single file:
//
//   - builder: zip the vendored hello-world Maven project (pom.xml + src/...),
//     upload it as a source archive, and let the JVM builder pod run
//     `mvn package` to produce the fat jar. Needs JVM_RUNTIME_IMAGE +
//     JVM_BUILDER_IMAGE.
//   - deploy_jar: deploy a pre-built fat jar directly (no build), exercising
//     the runtime loading a deployment archive. Needs JVM_RUNTIME_IMAGE +
//     JAVA_HELLO_JAR_PATH (CI builds the jar inside the builder image; see the
//     "Go integration tests" step in .github/workflows/push_pr.yaml).
//
// Both paths deploy io.fission.HelloWorld and expect "Hello World!" on poolmgr
// and newdeploy. Builds are slow (Maven downloads dependencies on first run),
// so each subtest budgets its own ctx.
func TestJavaEnv(t *testing.T) {
	t.Parallel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVM(t)
	ns := f.NewTestNamespace(t)

	t.Run("builder", func(t *testing.T) {
		builder := f.Images().RequireJVMBuilder(t)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		envName := "java-bld-" + ns.ID
		pkgName := "javapkg-" + ns.ID
		fnP := "javabld-pm-" + ns.ID
		fnND := "javabld-nd-" + ns.ID

		// CreateEnv auto-waits for the builder pod + EndpointSlice to publish.
		// KeepArchive is required for JVM: the builder ships a .jar (a zip), and
		// without it the fetcher unzips it into a directory that the runtime
		// can't open as a JarFile.
		ns.CreateEnv(t, ctx, framework.EnvOptions{
			Name: envName, Image: runtime, Builder: builder, KeepArchive: true,
		})

		// Source archive — pom.xml at top level + src/main/java/io/fission/HelloWorld.java.
		// ZipTestDataTree preserves the `src/main/java/...` path so Maven finds
		// the source where it expects it.
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
	})

	t.Run("deploy_jar", func(t *testing.T) {
		jarPath := os.Getenv("JAVA_HELLO_JAR_PATH")
		if jarPath == "" {
			t.Skip("JAVA_HELLO_JAR_PATH is not set; skipping (build the hello-world jar via mvn package and point the env var at the .jar)")
		}
		if _, err := os.Stat(jarPath); err != nil {
			t.Skipf("JAVA_HELLO_JAR_PATH=%q not accessible: %v", jarPath, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		envName := "java-dep-" + ns.ID
		fnP := "java-pm-" + ns.ID
		fnND := "java-nd-" + ns.ID

		// No builder needed — the jar is deployed directly. KeepArchive keeps
		// the .jar a single file (the fetcher would otherwise unzip it).
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

		bodyP := f.Router(t).GetEventually(t, ctx, "/"+fnP, framework.BodyContains("Hello"))
		require.True(t, strings.Contains(bodyP, "Hello"),
			"poolmgr fn %q response missing 'Hello': %q", fnP, bodyP)
		bodyND := f.Router(t).GetEventually(t, ctx, "/"+fnND, framework.BodyContains("Hello"))
		require.True(t, strings.Contains(bodyND, "Hello"),
			"newdeploy fn %q response missing 'Hello': %q", fnND, bodyND)
	})
}
