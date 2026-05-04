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

// TestJVMJerseyEnv is the Go port of test_environments/test_jvm_jersey_env.sh.
//
// The bash version runs `docker run maven:... mvn package` at test time
// to produce a fat jar; we don't replicate that from a Go test. Instead,
// the test t.Skips unless both JVM_JERSEY_RUNTIME_IMAGE *and*
// JVM_JERSEY_JAR_PATH are set in the environment. Local devs (or a
// future CI step) can build the jar once and point JVM_JERSEY_JAR_PATH
// at the artifact:
//
//	docker run --rm -v "$PWD":/usr/src/mymaven -w /usr/src/mymaven \
//	    maven:3.5-jdk-8 mvn -q clean package
//	export JVM_JERSEY_RUNTIME_IMAGE=ghcr.io/fission/jvm-jersey-env
//	export JVM_JERSEY_JAR_PATH=$PWD/target/jersey-hello-world-0.0.1-jar-with-dependencies.jar
//
// Coverage when enabled: poolmgr GET, newdeploy GET, and newdeploy POST
// of an XML body (echo-back) — the same three assertions the bash makes.
func TestJVMJerseyEnv(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequireJVMJersey(t)

	jarPath := os.Getenv("JVM_JERSEY_JAR_PATH")
	if jarPath == "" {
		t.Skip("JVM_JERSEY_JAR_PATH is not set; skipping (build the jersey jar via maven and point the env var at the .jar)")
	}
	if _, err := os.Stat(jarPath); err != nil {
		t.Skipf("JVM_JERSEY_JAR_PATH=%q not accessible: %v", jarPath, err)
	}

	ns := f.NewTestNamespace(t)
	envName := "jersey-" + ns.ID
	fnP := "jersey-pm-" + ns.ID
	fnND := "jersey-nd-" + ns.ID
	fnPost := "jersey-post-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime,
	})

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
}
