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

// TestJavaEnv is the Go port of test_environments/test_java_env.sh.
//
// Like jvm_jersey, the bash version builds a fat jar via Docker+Maven
// at test time. We don't replicate that from a Go test; instead, the
// test t.Skips unless both JVM_RUNTIME_IMAGE *and* JAVA_HELLO_JAR_PATH
// are set in the environment. Local devs (or a future CI step) build
// the jar once and point JAVA_HELLO_JAR_PATH at the artifact:
//
//	cd examples/java/hello-world
//	docker run --rm -v "$PWD":/usr/src/mymaven -w /usr/src/mymaven \
//	    maven:3.5-jdk-8 mvn -q clean package
//	export JVM_RUNTIME_IMAGE=ghcr.io/fission/jvm-env
//	export JAVA_HELLO_JAR_PATH=$PWD/target/hello-world-1.0-SNAPSHOT-jar-with-dependencies.jar
//
// Coverage when enabled: poolmgr GET + newdeploy GET, both expecting
// the "Hello" body io.fission.HelloWorld returns.
func TestJavaEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVM(t)

	jarPath := os.Getenv("JAVA_HELLO_JAR_PATH")
	if jarPath == "" {
		t.Skip("JAVA_HELLO_JAR_PATH is not set; skipping (build the hello-world jar via mvn package and point the env var at the .jar)")
	}
	if _, err := os.Stat(jarPath); err != nil {
		t.Skipf("JAVA_HELLO_JAR_PATH=%q not accessible: %v", jarPath, err)
	}

	ns := f.NewTestNamespace(t)
	envName := "java-" + ns.ID
	fnP := "java-pm-" + ns.ID
	fnND := "java-nd-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: runtime})

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
}
