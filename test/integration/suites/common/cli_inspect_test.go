// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestCLICommands covers fission CLI commands that had no integration coverage:
// the `env update` mutation path and a sweep of read-only inspection commands
// (get / getmeta / list / pods / info, plus the trigger list paths and
// version). One env + function + route + package serves as the shared fixture,
// so this adds broad pkg/fission-cli coverage cheaply.
//
// ns.CLI / ns.CLICaptureStdout both t.Fatal on a non-zero CLI exit, so even a
// bare call asserts the command's code path runs cleanly. CLICaptureStdout
// (used only where we assert on printed output) also captures os.Stdout, since
// several subcommands print via fmt rather than cobra's writer; it serializes
// against other CLI calls, so the no-output checks use ns.CLI to stay parallel.
func TestCLICommands(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-cli-" + ns.ID
	fnName := "fn-cli-" + ns.ID
	routeName := "route-cli-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image, Poolsize: 1})
	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Name: routeName, Function: fnName, URL: routePath, Method: "GET"})

	// Invoke once so a specialized pod exists for `fn pods` / `env pods`.
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("world"))
	pkgName := ns.FunctionPackageName(t, ctx, fnName)

	// --- env update (mutating CLI path): bump poolsize 1 → 2 and confirm the
	// CLI wrote it to the Environment CR. ---
	ns.CLI(t, ctx, "env", "update", "--name", envName, "--poolsize", "2")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		env, err := f.FissionClient().CoreV1().Environments(ns.Name).Get(ctx, envName, metav1.GetOptions{})
		if assert.NoError(c, err) {
			assert.Equal(c, 2, env.Spec.Poolsize, "env poolsize after `env update`")
		}
	}, 30*time.Second, 1*time.Second)

	// --- read-only inspections that name a known resource (assert on output) ---
	assert.Contains(t, ns.CLICaptureStdout(t, ctx, "fn", "list"), fnName, "fn list should name the function")
	assert.Contains(t, ns.CLICaptureStdout(t, ctx, "env", "list"), envName, "env list should name the env")
	assert.Contains(t, ns.CLICaptureStdout(t, ctx, "pkg", "list"), pkgName, "pkg list should name the package")
	pkgInfo := ns.CLICaptureStdout(t, ctx, "pkg", "info", "--name", pkgName)
	assert.Contains(t, pkgInfo, pkgName, "pkg info should name the package")
	assert.Contains(t, pkgInfo, "Deployment:", "pkg info should describe how the code is delivered")

	// fission check: the health checker (including the oci-delivery
	// category) must run clean against a live cluster.
	checkOut := ns.CLICaptureStdout(t, ctx, "check")
	assert.Contains(t, checkOut, "oci-delivery", "fission check should run the oci-delivery category")
	assert.Contains(t, ns.CLICaptureStdout(t, ctx, "httptrigger", "list"), routeName, "httptrigger list should name the route")

	// --- read-only inspections we run for code-path coverage (exit-code only) ---
	ns.CLI(t, ctx, "fn", "get", "--name", fnName)
	ns.CLI(t, ctx, "fn", "getmeta", "--name", fnName)
	ns.CLI(t, ctx, "fn", "pods", "--name", fnName)
	ns.CLI(t, ctx, "env", "get", "--name", envName)
	ns.CLI(t, ctx, "env", "pods", "--name", envName)
	ns.CLI(t, ctx, "httptrigger", "get", "--name", routeName)

	// Empty-list paths for the trigger types with no resources in this namespace.
	ns.CLI(t, ctx, "timetrigger", "list")
	ns.CLI(t, ctx, "mqtrigger", "list")
	ns.CLI(t, ctx, "canary", "list")
	ns.CLI(t, ctx, "watch", "list")

	// Client + server version.
	ns.CLI(t, ctx, "version")

	// -----------------------------------------------------------------
	// Mutating + lifecycle CLI paths (update / delete / create) that the
	// Create* builders don't exercise. These fill the largest remaining
	// gaps in pkg/fission-cli/cmd/* (the trigger update/delete run paths).
	// ns.CLI t.Fatals on a non-zero exit, so running a command already
	// asserts its code path executes cleanly.
	// -----------------------------------------------------------------

	// fn update: change the execution timeout and confirm it persisted.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--fntimeout", "90")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		fn, err := f.FissionClient().CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
		if assert.NoError(c, err) {
			assert.Equal(c, 90, fn.Spec.FunctionTimeout, "fn timeout after `fn update`")
		}
	}, 30*time.Second, 1*time.Second)

	// httptrigger get + update on the shared route.
	ns.CLI(t, ctx, "httptrigger", "get", "--name", routeName)
	ns.CLI(t, ctx, "httptrigger", "update", "--name", routeName, "--url", routePath+"-v2")

	// timetrigger lifecycle: create (builder), update its cron, print a
	// schedule preview, then delete.
	ttName := "tt-cli-" + ns.ID
	ns.CreateTimeTrigger(t, ctx, framework.TimeTriggerOptions{Name: ttName, Function: fnName, Cron: "@every 1h"})
	ns.CLI(t, ctx, "timetrigger", "update", "--name", ttName, "--cron", "@every 2h")
	ns.CLICaptureStdout(t, ctx, "timetrigger", "showschedule", "--cron", "@every 5m")
	ns.CLI(t, ctx, "timetrigger", "delete", "--name", ttName)

	// kubewatch lifecycle: create (builder) then delete via the CLI.
	kwName := "kw-cli-" + ns.ID
	ns.CreateKubernetesWatchTrigger(t, ctx, framework.KubernetesWatchTriggerOptions{
		Name: kwName, Function: fnName, ObjType: "service", WatchNamespace: ns.Name,
	})
	ns.CLI(t, ctx, "watch", "delete", "--name", kwName)

	// mqtrigger lifecycle: create + update + delete. The KEDA kind needs no
	// running broker, so the CR round-trips purely through the API server.
	mqtName := "mqt-cli-" + ns.ID
	ns.CLI(t, ctx, "mqt", "create", "--name", mqtName, "--function", fnName, "--topic", "cli-topic", "--mqtype", "kafka")
	ns.CLI(t, ctx, "mqt", "update", "--name", mqtName, "--maxretries", "3")
	ns.CLI(t, ctx, "mqt", "delete", "--name", mqtName)
}
