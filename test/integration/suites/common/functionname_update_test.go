// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestFunctionNameUpdate covers newdeploy updateFunction's Package.FunctionName
// change branch (deployChanged when oldFn.Spec.Package.FunctionName !=
// newFn.Spec.Package.FunctionName), which no other test exercised.
//
// A single deploy archive ships two handlers in one module. The function points
// at handler_a, then `fission fn update --entrypoint handlers.handler_b` flips
// only the entrypoint (FunctionName) — same package, same env. The executor
// must roll the Deployment so new pods specialize against the new handler, and
// the route's response must switch from "alpha" to "beta".
func TestFunctionNameUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-fnname-" + ns.ID
	fnName := "fn-fnname-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: image,
		MinCPU: 20, MaxCPU: 100, MinMemory: 128, MaxMemory: 256,
	})

	// One deploy archive, two zero-arg handlers in the same module.
	deployDir := filepath.Join(t.TempDir(), "handlers")
	require.NoError(t, os.MkdirAll(deployDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "__init__.py"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(deployDir, "handlers.py"),
		[]byte("def handler_a():\n    return \"alpha\"\n\ndef handler_b():\n    return \"beta\"\n"), 0o644))
	zipPath := zipDirContents(t, deployDir, "handlers-deploy.zip")

	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: fnName, Env: envName, Deploy: zipPath, Entrypoint: "handlers.handler_a",
		ExecutorType: "newdeploy", MinScale: 1, MaxScale: 2,
	})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("alpha"))

	// Flip only the entrypoint — this changes Package.FunctionName, nothing else.
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--entrypoint", "handlers.handler_b")
	require.Equal(t, "handlers.handler_b",
		ns.GetFunction(t, ctx, fnName).Spec.Package.FunctionName, "FunctionName should be updated")

	// The Deployment must roll so the route now serves handler_b's output.
	body := f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("beta"))
	require.Contains(t, body, "beta", "route should serve handler_b after the FunctionName change")
	require.NotContains(t, body, "alpha", "route should no longer serve handler_a")
}
