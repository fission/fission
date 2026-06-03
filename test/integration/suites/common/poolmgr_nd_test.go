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
	appsv1 "k8s.io/api/apps/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestPoolmgrNewdeployToggle is the Go port of test_fn_update/test_poolmgr_nd.sh.
// Creates a function with default (poolmgr) executor, updates it to newdeploy,
// then back to poolmgr — at each step the router should still serve traffic.
func TestPoolmgrNewdeployToggle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-pmnd-" + ns.ID
	fnName := "fn-pmnd-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "python/hello/hello.py")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
	require.Equal(t, "poolmgr", string(ns.GetFunction(t, ctx, fnName).Spec.InvokeStrategy.ExecutionStrategy.ExecutorType))

	// → newdeploy: the shared Function reconciler (pkg/executor/funcreconciler)
	// sees the executor type change, tears the poolmgr incarnation down, and
	// creates the function under newdeploy — which must materialize a per-function
	// Deployment. Assert on the live object, not just that traffic flows.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codePath,
		"--minscale", "1", "--maxscale", "4", "--executortype", "newdeploy")
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
	require.Equal(t, "newdeploy", string(ns.GetFunction(t, ctx, fnName).Spec.InvokeStrategy.ExecutionStrategy.ExecutorType))
	ns.WaitForFunctionDeployment(t, ctx, fnName, func(*appsv1.Deployment) bool { return true },
		"newdeploy executor created a per-function Deployment", 90*time.Second)

	// → poolmgr: traffic must keep flowing after switching back. (The newdeploy
	// Deployment is not torn down promptly on this transition — cleanup is
	// deferred to the executor's periodic orphan reaper — so we don't assert on
	// its removal here.)
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codePath, "--executortype", "poolmgr")
	f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("world"))
	require.Equal(t, "poolmgr", string(ns.GetFunction(t, ctx, fnName).Spec.InvokeStrategy.ExecutionStrategy.ExecutorType))
}
