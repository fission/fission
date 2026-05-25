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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestBackendPoolmgr is the Go port of test_backend_poolmgr.sh — a
// simple smoke that the poolmgr executor backend serves a hello function.
// (TestNodeHelloHTTP already covers this implicitly with default poolmgr;
// here we keep it explicit to mirror the bash CI surface.)
func TestBackendPoolmgr(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-pmbk-" + ns.ID
	fnName := "fn-pm-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Code: codePath, ExecutorType: "poolmgr",
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: "/" + fnName, Method: "GET"})

	body := f.Router(t).GetEventually(t, ctx, "/"+fnName, framework.BodyContains("hello"))
	require.Contains(t, body, "hello")

	// Once a request has been served end-to-end the controllers that own
	// status conditions for this PR should have populated Ready:
	//   - the buildermgr marks the backing Package as Ready (via the
	//     normal pending→succeeded path) — but only for source-archive
	//     packages. Literal/deploy packages set BuildStatus client-side
	//     and currently never get backfilled, so we don't assert on
	//     Package conditions here.
	//   - the poolmgr executor marks the Function as Ready after
	//     specializing a pool pod.
	// Environment conditions are intentionally not written by any
	// controller in this PR — status writes would bump env.RV which
	// the buildermgr embeds in the builder service hostname. See
	// pkg/buildermgr/envwatcher.go.AddUpdateBuilder for the note.
	ns.WaitForFunctionConditionTrue(t, ctx, fnName, fv1.FunctionConditionReady, 30*time.Second)
}
