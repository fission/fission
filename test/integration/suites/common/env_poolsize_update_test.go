// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestEnvPoolsizeUpdate exercises an in-place Environment spec change for a
// poolmgr environment: it creates a pool-backed function, updates the
// Environment's poolsize, and asserts the function keeps serving.
//
// This guards the executor's shared Environment reconciler
// (pkg/executor/envreconciler), which replaced the per-executor-type Environment
// reconcilers with one dispatcher. The poolsize bump is a spec change that the
// dispatcher must route to the pool manager's ReconcileEnvironment without
// wedging the pool — a regression there would surface as the function no longer
// serving after the update. The reconciler's newdeploy image-propagation branch
// is covered by unit tests (an in-place runtime-image swap needs a second
// interchangeable image that CI does not provide).
func TestEnvPoolsizeUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-poolup-" + ns.ID
	fnName := "fn-poolup-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image, Poolsize: 1,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// In-place Environment spec change: the shared reconciler must process this
	// Environment update event and re-reconcile the warm pool.
	ns.CLI(t, ctx, "env", "update", "--name", envName, "--poolsize", "3")

	// The function must keep serving after the env reconcile.
	body = f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")
}
